package vault

import (
	"context"
	"encoding/base64"
	"log/slog"
	"regexp"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

const (
	settingMode         = "vault.mode"
	settingEnvelope     = "vault.dek_envelope"
	settingSalt         = "vault.master_salt"
	settingAutolock     = "vault.autolock_ms"
	settingPasswordless = "vault.passwordless"
	settingKeyCheck     = "vault.key_check"
	defaultAutolock     = uint64(0)
	maxAutoLockMS       = uint64(1440 * 60 * 1000)
)

type Mode string

const (
	ModeNotInit Mode = "not_init"
	ModeDPAPI   Mode = "dpapi"
	ModeMaster  Mode = "master"
)

type Status struct {
	Initialized      bool   `json:"initialized"`
	Mode             string `json:"mode"`
	Unlocked         bool   `json:"unlocked"`
	AutoLockMinutes  uint64 `json:"autoLockMinutes"`
	Passwordless     bool   `json:"passwordless"`
	SystemProtection bool   `json:"systemProtection"`
}

type Option func(*Vault)

type autoLockTickerFactory func(time.Duration) (<-chan time.Time, func())

type backgroundContextKey struct{}

func WithBackground(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundContextKey{}, true)
}

func WithProtector(protector Protector) Option {
	return func(v *Vault) {
		if protector != nil {
			v.protector = protector
		}
	}
}

type Vault struct {
	store     *store.Store
	protector Protector
	derive    func(string, []byte) (*secretKey, error)
	now       func() int64
	loadErr   error
	// unlockErr is why the automatic system-protection unlock in Load failed; it replaces the
	// misleading "not initialized" answer until an init or unlock succeeds.
	unlockErr error

	mu           sync.Mutex
	mode         Mode
	dek          *secretKey
	kek          *secretKey
	salt         []byte
	passwordless bool
	lastUsedAt   int64
	autoLockMS   uint64
	listeners    []func()

	autoLockMu        sync.Mutex
	autoLockStoreHook func()
	newAutoLockTicker autoLockTickerFactory
}

func Load(ctx context.Context, db *store.Store, options ...Option) *Vault {
	v := &Vault{
		store: db, protector: systemProtector{}, derive: deriveMasterKey,
		now: ids.NowMS, mode: ModeNotInit, autoLockMS: defaultAutolock,
		newAutoLockTicker: func(interval time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(interval)
			return ticker.C, ticker.Stop
		},
	}
	for _, option := range options {
		option(v)
	}
	v.lastUsedAt = v.now()

	switch mode, err := readSetting(ctx, db, settingMode); {
	case err != nil:
		v.noteLoadError(settingMode, err)
	case mode == "dpapi":
		v.mode = ModeDPAPI
	case mode == "master":
		v.mode = ModeMaster
	case mode == "":
	default:
		slog.Warn("unrecognized vault mode; treating vault as uninitialized")
	}
	if raw, err := readSetting(ctx, db, settingAutolock); err != nil {
		v.noteLoadError(settingAutolock, err)
	} else if raw != "" {
		if milliseconds, err := strconv.ParseUint(raw, 10, 64); err == nil && milliseconds <= maxAutoLockMS && milliseconds%60_000 == 0 {
			v.autoLockMS = milliseconds
		}
	}
	if raw, err := readSetting(ctx, db, settingSalt); err != nil {
		v.noteLoadError(settingSalt, err)
	} else if raw != "" {
		if salt, err := base64.StdEncoding.DecodeString(raw); err == nil {
			v.salt = salt
		} else {
			slog.Warn("failed to decode master password salt; continuing without salt", "error", err)
		}
	}
	if raw, err := readSetting(ctx, db, settingPasswordless); err != nil {
		v.noteLoadError(settingPasswordless, err)
	} else {
		v.passwordless = raw == "1"
	}
	if v.mode == ModeMaster && v.passwordless {
		v.mu.Lock()
		if err := v.unlockMasterLocked(ctx, ""); err != nil {
			slog.Warn("failed to auto-unlock passwordless vault; leaving vault locked", "error", err)
		}
		v.mu.Unlock()
	}
	if v.mode == ModeDPAPI {
		v.mu.Lock()
		if err := v.unlockDPAPILocked(ctx); err != nil {
			slog.Warn("failed to auto-unlock vault; treating vault as uninitialized", "error", err)
			v.unlockErr = err
			v.mode = ModeNotInit
			v.clearKeysLocked()
		}
		v.mu.Unlock()
	}
	db.SetSecretProtector(v)
	return v
}

func readSetting(ctx context.Context, db *store.Store, key string) (string, error) {
	value, _, err := db.SettingGet(ctx, key)
	if err != nil {
		return "", err
	}
	return value, nil
}

// noteLoadError records a setting read failure: the vault state is now unknown, and the init guard refuses a DEK rotation
// so "cannot read" is never misread as "not initialized", which would make old ciphertext permanently undecryptable.
func (v *Vault) noteLoadError(key string, err error) {
	slog.Warn("failed to read vault setting; initialization will be rejected", "setting", key, "error", err)
	if v.loadErr == nil {
		v.loadErr = err
	}
}

func (v *Vault) guardLoadError() error {
	if v.loadErr != nil {
		return ipc.NewError(ipc.CodeCrypto, "加密错误：读取凭据库设置失败，已拒绝初始化；请检查数据库")
	}
	return nil
}

// hasUndecryptableSecrets reports whether ciphertext remains in the vault: besides credential rows,
// enc:v1: envelopes in the setting table (AI keys, etc.) are also encrypted with the current DEK, and a DEK rotation would make them permanently undecryptable.
func (v *Vault) hasUndecryptableSecrets(ctx context.Context) (bool, error) {
	existing, err := v.store.CredentialList(ctx)
	if err != nil {
		return false, err
	}
	if len(existing) > 0 {
		return true, nil
	}
	return v.store.SettingContainsValue(ctx, store.SecretEnvelopePrefix)
}

func (v *Vault) Status() Status {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, usesSystemProtector := v.protector.(systemProtector)
	return Status{
		Initialized: v.mode != ModeNotInit, Mode: string(v.mode), Unlocked: v.dek != nil,
		AutoLockMinutes:  v.autoLockMS / 60_000,
		Passwordless:     v.mode == ModeMaster && v.passwordless,
		SystemProtection: systemProtectionAvailable && usesSystemProtector,
	}
}

func (v *Vault) AddUnlockListener(listener func()) {
	if listener == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.listeners = append(v.listeners, listener)
}

func (v *Vault) fireUnlocked() {
	v.mu.Lock()
	listeners := append([]func(){}, v.listeners...)
	v.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
}

func (v *Vault) InitMaster(ctx context.Context, password string) error {
	v.mu.Lock()
	err := v.initMasterLocked(ctx, password)
	v.mu.Unlock()
	if err == nil {
		v.fireUnlocked()
	}
	return err
}

func (v *Vault) initMasterLocked(ctx context.Context, password string) error {
	if v.mode != ModeNotInit {
		return ipc.NewError(ipc.CodeVaultAlreadyInit, "凭据库已初始化，不能重复初始化")
	}
	if err := v.guardLoadError(); err != nil {
		return err
	}
	if password != "" && len(password) < 8 {
		return ipc.BadParam(errString("保护密码至少 8 位；留空则不设置密码"))
	}
	hasSecrets, err := v.hasUndecryptableSecrets(ctx)
	if err != nil {
		return err
	}
	if hasSecrets {
		return ipc.NewError(ipc.CodeCrypto, "加密错误：库内仍有密文，已拒绝重新初始化，以免替换密钥后无法解密")
	}
	salt := make([]byte, 16)
	if err := randomBytes(salt); err != nil {
		return err
	}
	kek, err := v.derive(password, salt)
	if err != nil {
		return err
	}
	dek, err := generateKey()
	if err != nil {
		kek.destroy()
		return err
	}
	envelope, err := sealDEK(kek, dek)
	if err != nil {
		kek.destroy()
		dek.destroy()
		return err
	}
	keyCheck, err := sealKeyCheck(dek)
	if err != nil {
		kek.destroy()
		dek.destroy()
		return err
	}
	if err := v.store.SettingSetMany(ctx, map[string]string{
		settingEnvelope:     base64.StdEncoding.EncodeToString(envelope),
		settingKeyCheck:     keyCheck,
		settingSalt:         base64.StdEncoding.EncodeToString(salt),
		settingMode:         string(ModeMaster),
		settingPasswordless: passwordlessFlag(password),
	}); err != nil {
		kek.destroy()
		dek.destroy()
		return err
	}
	v.clearKeysLocked()
	v.mode = ModeMaster
	v.salt = salt
	v.passwordless = password == ""
	v.kek = kek
	v.dek = dek
	v.unlockErr = nil
	v.lastUsedAt = v.now()
	return nil
}

func passwordlessFlag(password string) string {
	if password == "" {
		return "1"
	}
	return "0"
}

func (v *Vault) InitDPAPI(ctx context.Context, password string) error {
	v.mu.Lock()
	err := v.initDPAPILocked(ctx, password)
	v.mu.Unlock()
	if err == nil {
		v.fireUnlocked()
	}
	return err
}

func (v *Vault) initDPAPILocked(ctx context.Context, password string) error {
	if v.mode == ModeDPAPI {
		return ipc.NewError(ipc.CodeVaultAlreadyInit, "凭据库已初始化，不能重复初始化")
	}
	if err := v.guardLoadError(); err != nil {
		return err
	}
	if v.mode == ModeMaster {
		return v.migrateMasterToDPAPILocked(ctx, password)
	}
	v.mode = ModeDPAPI
	if err := v.unlockDPAPILocked(ctx); err != nil {
		v.mode = ModeNotInit
		v.clearKeysLocked()
		return err
	}
	if err := v.store.SettingSet(ctx, settingMode, string(ModeDPAPI)); err != nil {
		v.mode = ModeNotInit
		v.clearKeysLocked()
		return err
	}
	return nil
}

// discardEnvelope releases the external entry behind an envelope whose persistence failed.
func (v *Vault) discardEnvelope(envelope []byte) {
	discarder, ok := v.protector.(envelopeDiscarder)
	if !ok {
		return
	}
	if err := discarder.Discard(envelope); err != nil {
		slog.Warn("failed to discard unused system-protection envelope", "error", err)
	}
}

func (v *Vault) migrateMasterToDPAPILocked(ctx context.Context, password string) error {
	if err := v.unlockMasterLocked(ctx, password); err != nil {
		return err
	}
	protected, err := v.protector.Protect(v.dek.bytes[:])
	if err != nil {
		return err
	}
	if err := v.store.SettingSetMany(ctx, map[string]string{
		settingEnvelope:     base64.StdEncoding.EncodeToString(protected),
		settingMode:         string(ModeDPAPI),
		settingPasswordless: "0",
	}); err != nil {
		v.discardEnvelope(protected)
		return err
	}
	v.kek.destroy()
	v.kek = nil
	v.passwordless = false
	v.mode = ModeDPAPI
	v.lastUsedAt = v.now()
	return nil
}

func (v *Vault) unlockDPAPILocked(ctx context.Context) error {
	encoded, found, err := v.store.SettingGet(ctx, settingEnvelope)
	if err != nil {
		return err
	}
	var dek *secretKey
	if !found || encoded == "" {
		hasSecrets, err := v.hasUndecryptableSecrets(ctx)
		if err != nil {
			return err
		}
		if hasSecrets {
			return ipc.NewError(ipc.CodeCrypto, "加密错误：密钥信封已丢失且库内仍有密文，已拒绝重建密钥")
		}
		dek, err = generateKey()
		if err != nil {
			return err
		}
		protected, err := v.protector.Protect(dek.bytes[:])
		if err != nil {
			dek.destroy()
			return err
		}
		keyCheck, err := sealKeyCheck(dek)
		if err != nil {
			v.discardEnvelope(protected)
			dek.destroy()
			return err
		}
		if err := v.store.SettingSetMany(ctx, map[string]string{
			settingEnvelope: base64.StdEncoding.EncodeToString(protected),
			settingKeyCheck: keyCheck,
		}); err != nil {
			v.discardEnvelope(protected)
			dek.destroy()
			return err
		}
	} else {
		envelope, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return ipc.WrapError(ipc.CodeCrypto, "加密错误: 信封解码失败", err)
		}
		plaintext, err := v.protector.Unprotect(envelope)
		if err != nil {
			return err
		}
		dek, err = decodeDEK(plaintext)
		for i := range plaintext {
			plaintext[i] = 0
		}
		if err != nil {
			return err
		}
		if err := v.verifyOrAdoptKeyCheckLocked(ctx, dek, ModeDPAPI); err != nil {
			return err
		}
		v.migrateLegacyEnvelopeLocked(ctx, envelope, dek)
	}
	v.dek.destroy()
	v.dek = dek
	v.unlockErr = nil
	v.lastUsedAt = v.now()
	return nil
}

// migrateLegacyEnvelopeLocked rewrites an envelope in a superseded platform format (for
// example the fixed macOS Keychain account) once the DEK has been verified against this
// database. Old entries stay readable until then and are never deleted, because they may
// still back another data directory. Failures only log: the vault stays unlocked and the
// next start retries.
func (v *Vault) migrateLegacyEnvelopeLocked(ctx context.Context, envelope []byte, dek *secretKey) {
	detector, ok := v.protector.(legacyEnvelopeDetector)
	if !ok || !detector.IsLegacyEnvelope(envelope) {
		return
	}
	protected, err := v.protector.Protect(dek.bytes[:])
	if err != nil {
		slog.Warn("failed to migrate legacy system-protection envelope; keeping the old one", "error", err)
		return
	}
	if err := v.store.SettingSet(ctx, settingEnvelope, base64.StdEncoding.EncodeToString(protected)); err != nil {
		slog.Warn("failed to store migrated system-protection envelope; keeping the old one", "error", err)
		v.discardEnvelope(protected)
	}
}

var secretEnvelopePattern = regexp.MustCompile(regexp.QuoteMeta(store.SecretEnvelopePrefix) + `[A-Za-z0-9+/=]+`)

func errKeyMismatch(mode Mode) error {
	if mode == ModeDPAPI {
		return ipc.NewError(ipc.CodeCrypto, "加密错误：系统保护的数据密钥与本凭据库不匹配（macOS 上可能是钥匙串条目被其他数据目录覆盖），已拒绝解锁；如无法找回原数据密钥，请删除无法解密的凭据和 AI Key 后重新初始化")
	}
	return ipc.NewError(ipc.CodeCrypto, "加密错误：数据密钥与本凭据库的密钥校验值不匹配（数据库可能被部分恢复或修改），已拒绝解锁")
}

// verifyOrAdoptKeyCheckLocked proves dek belongs to this database. It destroys dek when it
// does not. Databases without a key-check are probed against existing ciphertext, and the
// check is recorded once the key is proven (or there is nothing to prove it against).
func (v *Vault) verifyOrAdoptKeyCheckLocked(ctx context.Context, dek *secretKey, mode Mode) error {
	err := v.checkDEKLocked(ctx, dek, mode)
	if err != nil {
		dek.destroy()
	}
	return err
}

func (v *Vault) checkDEKLocked(ctx context.Context, dek *secretKey, mode Mode) error {
	stored, found, err := v.store.SettingGet(ctx, settingKeyCheck)
	if err != nil {
		return err
	}
	if found && stored != "" {
		if err := verifyKeyCheck(dek, stored); err != nil {
			slog.Warn("vault key-check rejected the data key", "error", err)
			return errKeyMismatch(mode)
		}
		return nil
	}
	matches, hasCiphertext, err := v.probeCiphertext(ctx, dek)
	if err != nil {
		return err
	}
	if hasCiphertext && !matches {
		return errKeyMismatch(mode)
	}
	keyCheck, err := sealKeyCheck(dek)
	if err == nil {
		err = v.store.SettingSet(ctx, settingKeyCheck, keyCheck)
	}
	if err != nil {
		slog.Warn("failed to record vault key-check; will retry on next unlock", "error", err)
	}
	return nil
}

// probeCiphertext reports whether any existing ciphertext opens with dek. One damaged
// entry must not block an otherwise valid vault, so any success counts as a match.
func (v *Vault) probeCiphertext(ctx context.Context, dek *secretKey) (matches, hasCiphertext bool, err error) {
	rows, err := v.store.CredentialList(ctx)
	if err != nil {
		return false, false, err
	}
	for _, row := range rows {
		if row.Cipher != store.CipherAES256GCM {
			continue
		}
		hasCiphertext = true
		if plaintext, err := openCredential(dek, row.Nonce, row.Blob); err == nil {
			clear(plaintext)
			return true, true, nil
		}
	}
	values, err := v.store.SettingValuesContaining(ctx, store.SecretEnvelopePrefix)
	if err != nil {
		return false, false, err
	}
	for _, value := range values {
		for _, envelope := range secretEnvelopePattern.FindAllString(value, -1) {
			hasCiphertext = true
			raw, err := base64.StdEncoding.DecodeString(envelope[len(store.SecretEnvelopePrefix):])
			if err != nil || len(raw) <= nonceLength {
				continue
			}
			if plaintext, err := open(dek, raw[:nonceLength], raw[nonceLength:], secretAAD); err == nil {
				clear(plaintext)
				return true, true, nil
			}
		}
	}
	return false, hasCiphertext, nil
}

func (v *Vault) UnlockMaster(ctx context.Context, password string) error {
	v.mu.Lock()
	err := v.unlockMasterLocked(ctx, password)
	v.mu.Unlock()
	if err == nil {
		v.fireUnlocked()
	}
	return err
}

func (v *Vault) unlockMasterLocked(ctx context.Context, password string) error {
	if v.mode == ModeNotInit {
		return ipc.NewError(ipc.CodeVaultNotInit, "凭据库尚未初始化，请先在\"设置 → 凭据保护\"中完成初始化")
	}
	if v.mode != ModeMaster {
		return ipc.NewError(ipc.CodeUnsupported, "不支持的操作: 当前凭据库未使用保护密码")
	}
	if len(v.salt) == 0 {
		return ipc.NewError(ipc.CodeVaultNotInit, "凭据库尚未初始化")
	}
	kek, err := v.derive(password, v.salt)
	if err != nil {
		return err
	}
	encoded, found, err := v.store.SettingGet(ctx, settingEnvelope)
	if err != nil {
		kek.destroy()
		return err
	}
	if !found {
		kek.destroy()
		return ipc.NewError(ipc.CodeVaultNotInit, "凭据库尚未初始化")
	}
	envelope, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		kek.destroy()
		return ipc.WrapError(ipc.CodeCrypto, "加密错误: 信封解码失败", err)
	}
	dek, err := openDEK(kek, envelope)
	if err != nil {
		kek.destroy()
		return err
	}
	if err := v.verifyOrAdoptKeyCheckLocked(ctx, dek, ModeMaster); err != nil {
		kek.destroy()
		return err
	}
	v.clearKeysLocked()
	v.kek = kek
	v.dek = dek
	v.unlockErr = nil
	v.passwordless = password == ""
	v.lastUsedAt = v.now()
	return nil
}

func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.clearKeysLocked()
}

func (v *Vault) clearKeysLocked() {
	v.dek.destroy()
	v.kek.destroy()
	v.dek = nil
	v.kek = nil
}

func (v *Vault) SetAutoLock(ctx context.Context, minutes uint64) error {
	if minutes > maxAutoLockMS/60_000 {
		return ipc.BadParam(errString("自动锁定时长需在 0（禁用）至 1440 分钟之间"))
	}
	milliseconds := minutes * 60_000
	v.autoLockMu.Lock()
	defer v.autoLockMu.Unlock()
	if err := v.store.SettingSet(ctx, settingAutolock, strconv.FormatUint(milliseconds, 10)); err != nil {
		return err
	}
	if v.autoLockStoreHook != nil {
		v.autoLockStoreHook()
	}
	v.mu.Lock()
	v.autoLockMS = milliseconds
	v.mu.Unlock()
	return nil
}

func (v *Vault) currentDEK(ctx context.Context) (*secretKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if background, _ := ctx.Value(backgroundContextKey{}).(bool); !background {
		v.lastUsedAt = v.now()
	}
	if v.dek == nil {
		if v.unlockErr != nil {
			return nil, v.unlockErr
		}
		if v.mode == ModeNotInit {
			return nil, ipc.NewError(ipc.CodeVaultNotInit, "凭据库尚未初始化，请先在\"设置 → 凭据保护\"中完成初始化")
		}
		return nil, ipc.NewError(ipc.CodeVaultLocked, "凭据库已锁定，请先解锁")
	}
	return v.dek.clone(), nil
}

func (v *Vault) AutoLockIfIdle() {
	v.mu.Lock()
	defer v.mu.Unlock()
	idle := v.now() - v.lastUsedAt
	if v.autoLockMS > 0 && idle >= 0 && uint64(idle) > v.autoLockMS && v.mode == ModeMaster && !v.passwordless {
		v.clearKeysLocked()
	}
}

func (v *Vault) RunAutoLock(ctx context.Context) {
	ticks, stopTicker := v.newAutoLockTicker(time.Minute)
	defer stopTicker()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			v.AutoLockIfIdle()
		}
	}
}

func (v *Vault) EncryptCredential(ctx context.Context, plaintext string) ([]byte, []byte, error) {
	dek, err := v.currentDEK(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer dek.destroy()
	return sealCredential(dek, []byte(plaintext))
}

func (v *Vault) DecryptCredential(ctx context.Context, row store.CredentialRow) ([]byte, error) {
	if row.Cipher != store.CipherAES256GCM {
		return nil, ipc.NewError(ipc.CodeUnsupported, "不支持的操作: 未知凭据加密格式 "+row.Cipher)
	}
	dek, err := v.currentDEK(ctx)
	if err != nil {
		return nil, err
	}
	defer dek.destroy()
	return openCredential(dek, row.Nonce, row.Blob)
}

func (v *Vault) DecryptCredentialString(ctx context.Context, row store.CredentialRow) (string, error) {
	plaintext, err := v.DecryptCredential(ctx, row)
	if err != nil {
		return "", err
	}
	result := string(plaintext)
	if !utf8.Valid(plaintext) {
		result = string([]rune(result))
	}
	for i := range plaintext {
		plaintext[i] = 0
	}
	return result, nil
}

func (v *Vault) KEKHint() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.mode == ModeDPAPI {
		return "dpapi"
	}
	return "master:0"
}

func (v *Vault) ChangeMasterPassword(ctx context.Context, oldPassword, newPassword string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.mode != ModeMaster {
		return ipc.NewError(ipc.CodeUnsupported, "不支持的操作: 当前凭据库未使用保护密码")
	}
	if newPassword != "" && len(newPassword) < 8 {
		return ipc.BadParam(errString("保护密码至少 8 位；留空则不设置密码"))
	}
	if len(v.salt) == 0 {
		return ipc.NewError(ipc.CodeVaultNotInit, "凭据库尚未初始化")
	}
	oldKEK, err := v.derive(oldPassword, v.salt)
	if err != nil {
		return err
	}
	encoded, found, err := v.store.SettingGet(ctx, settingEnvelope)
	if err != nil {
		oldKEK.destroy()
		return err
	}
	if !found {
		oldKEK.destroy()
		return ipc.NewError(ipc.CodeVaultNotInit, "凭据库尚未初始化")
	}
	envelope, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		oldKEK.destroy()
		return ipc.WrapError(ipc.CodeCrypto, "加密错误: 信封解码失败", err)
	}
	dek, err := openDEK(oldKEK, envelope)
	oldKEK.destroy()
	if err != nil {
		return err
	}
	defer dek.destroy()
	newSalt := make([]byte, 16)
	if err := randomBytes(newSalt); err != nil {
		return err
	}
	newKEK, err := v.derive(newPassword, newSalt)
	if err != nil {
		return err
	}
	newEnvelope, err := sealDEK(newKEK, dek)
	if err != nil {
		newKEK.destroy()
		return err
	}
	if err := v.store.SettingSetMany(ctx, map[string]string{
		settingEnvelope:     base64.StdEncoding.EncodeToString(newEnvelope),
		settingSalt:         base64.StdEncoding.EncodeToString(newSalt),
		settingPasswordless: passwordlessFlag(newPassword),
	}); err != nil {
		newKEK.destroy()
		return err
	}
	v.kek.destroy()
	v.kek = newKEK
	v.salt = newSalt
	v.passwordless = newPassword == ""
	v.lastUsedAt = v.now()
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }
