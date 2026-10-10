package vault

import (
	"context"
	"encoding/base64"
	"log/slog"
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
	defaultAutolock     = uint64(30 * 60 * 1000)
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
}

func Load(ctx context.Context, db *store.Store, options ...Option) *Vault {
	v := &Vault{
		store: db, protector: systemProtector{}, derive: deriveMasterKey,
		now: ids.NowMS, mode: ModeNotInit, autoLockMS: defaultAutolock,
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
		slog.Warn("凭据库模式无法识别，按未初始化处理")
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
			slog.Warn("主密码盐解码失败，按无盐处理", "error", err)
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
			slog.Warn("无密码凭据库自动解锁失败，按锁定处理", "error", err)
		}
		v.mu.Unlock()
	}
	if v.mode == ModeDPAPI {
		v.mu.Lock()
		if err := v.unlockDPAPILocked(ctx); err != nil {
			slog.Warn("凭据库自动解锁失败，按未初始化处理", "error", err)
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

// noteLoadError 记录设置读取失败: 此时库状态未知, 初始化守卫据此拒绝换新 DEK,
// 避免把"读不到"误判为"未初始化"而让旧密文永久不可解密。
func (v *Vault) noteLoadError(key string, err error) {
	slog.Warn("读取凭据库设置失败，初始化将被拒绝", "setting", key, "error", err)
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

// hasUndecryptableSecrets 报告库内是否仍有密文: 凭据行之外, setting 表里的
// enc:v1: 信封(AI 密钥等)同样由当前 DEK 加密, 换新 DEK 会让它们永远无法解密。
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
		return ipc.BadParam(errString("主密码至少 8 位；留空则表示不设置密码"))
	}
	hasSecrets, err := v.hasUndecryptableSecrets(ctx)
	if err != nil {
		return err
	}
	if hasSecrets {
		return ipc.NewError(ipc.CodeCrypto, "加密错误：库内仍有密文，已拒绝重新初始化，以免替换密钥后无法解密")
	}
	salt := make([]byte, 16)
	randomBytes(salt)
	kek, err := v.derive(password, salt)
	if err != nil {
		return err
	}
	dek := generateKey()
	envelope, err := sealDEK(kek, dek)
	if err != nil {
		kek.destroy()
		dek.destroy()
		return err
	}
	if err := v.store.SettingSetMany(ctx, map[string]string{
		settingEnvelope:     base64.StdEncoding.EncodeToString(envelope),
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
		dek = generateKey()
		protected, err := v.protector.Protect(dek.bytes[:])
		if err != nil {
			dek.destroy()
			return err
		}
		if err := v.store.SettingSet(ctx, settingEnvelope, base64.StdEncoding.EncodeToString(protected)); err != nil {
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
	}
	v.dek.destroy()
	v.dek = dek
	v.lastUsedAt = v.now()
	return nil
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
		return ipc.NewError(ipc.CodeUnsupported, "不支持的操作: 凭据库不是主密码模式")
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
	v.clearKeysLocked()
	v.kek = kek
	v.dek = dek
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
		return ipc.BadParam(errString("自动锁时长需在 0（禁用）至 1440 分钟之间"))
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

func (v *Vault) currentDEK() (*secretKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.lastUsedAt = v.now()
	if v.dek == nil {
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
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			v.AutoLockIfIdle()
		}
	}
}

func (v *Vault) EncryptCredential(_ context.Context, plaintext string) ([]byte, []byte, error) {
	dek, err := v.currentDEK()
	if err != nil {
		return nil, nil, err
	}
	defer dek.destroy()
	return sealCredential(dek, []byte(plaintext))
}

func (v *Vault) DecryptCredential(_ context.Context, row store.CredentialRow) ([]byte, error) {
	if row.Cipher != store.CipherAES256GCM {
		return nil, ipc.NewError(ipc.CodeUnsupported, "不支持的操作: 未知凭据加密格式 "+row.Cipher)
	}
	dek, err := v.currentDEK()
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
		return ipc.NewError(ipc.CodeUnsupported, "不支持的操作: 凭据库不是主密码模式")
	}
	if newPassword != "" && len(newPassword) < 8 {
		return ipc.BadParam(errString("主密码至少 8 位；留空则表示不设置密码"))
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
	randomBytes(newSalt)
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
