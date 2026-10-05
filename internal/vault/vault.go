package vault

import (
	"context"
	"encoding/base64"
	"log/slog"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

const (
	settingMode     = "vault.mode"
	settingEnvelope = "vault.dek_envelope"
	settingSalt     = "vault.master_salt"
	settingAutolock = "vault.autolock_ms"
	defaultAutolock = uint64(30 * 60 * 1000)
	maxAutoLockMS   = uint64(1440 * 60 * 1000)
)

type Mode string

const (
	ModeNotInit Mode = "not_init"
	ModeDPAPI   Mode = "dpapi"
	ModeMaster  Mode = "master"
)

type Status struct {
	Initialized     bool   `json:"initialized"`
	Mode            string `json:"mode"`
	Unlocked        bool   `json:"unlocked"`
	AutoLockMinutes uint64 `json:"autoLockMinutes"`
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

	mu         sync.Mutex
	mode       Mode
	dek        *secretKey
	kek        *secretKey
	salt       []byte
	lastUsedAt int64
	autoLockMS uint64
	listeners  []func()

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

	switch readSetting(ctx, db, settingMode, "凭据库模式") {
	case "dpapi":
		v.mode = ModeDPAPI
	case "master":
		v.mode = ModeMaster
	case "":
	default:
		slog.Warn("凭据库模式无法识别，本次按未初始化处理")
	}
	if raw := readSetting(ctx, db, settingAutolock, "自动锁时长"); raw != "" {
		if milliseconds, err := strconv.ParseUint(raw, 10, 64); err == nil && milliseconds <= maxAutoLockMS && milliseconds%60_000 == 0 {
			v.autoLockMS = milliseconds
		}
	}
	if raw := readSetting(ctx, db, settingSalt, "主密码盐"); raw != "" {
		if salt, err := base64.StdEncoding.DecodeString(raw); err == nil {
			v.salt = salt
		} else {
			slog.Warn("主密码盐解码失败，按无盐处理", "error", err)
		}
	}
	if v.mode == ModeDPAPI {
		v.mu.Lock()
		if err := v.unlockDPAPILocked(ctx); err != nil {
			slog.Warn("凭据库自动解锁失败，本次按未初始化处理", "error", err)
			v.mode = ModeNotInit
			v.clearKeysLocked()
		}
		v.mu.Unlock()
	}
	db.SetSecretProtector(v)
	return v
}

func readSetting(ctx context.Context, db *store.Store, key, what string) string {
	value, _, err := db.SettingGet(ctx, key)
	if err != nil {
		slog.Warn("读取凭据库设置失败，按缺省处理", "setting", key, "what", what, "error", err)
		return ""
	}
	return value
}

func (v *Vault) Status() Status {
	v.mu.Lock()
	defer v.mu.Unlock()
	return Status{
		Initialized: v.mode != ModeNotInit, Mode: string(v.mode), Unlocked: v.dek != nil,
		AutoLockMinutes: v.autoLockMS / 60_000,
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
	if len(password) < 8 {
		return ipc.BadParam(errString("主密码至少 8 位"))
	}
	existing, err := v.store.CredentialList(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return ipc.NewError(ipc.CodeCrypto, "加密错误: 库内仍有密文，重新初始化会替换密钥并使其永远无法解密，已拒绝")
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
		settingEnvelope: base64.StdEncoding.EncodeToString(envelope),
		settingSalt:     base64.StdEncoding.EncodeToString(salt),
		settingMode:     string(ModeMaster),
	}); err != nil {
		kek.destroy()
		dek.destroy()
		return err
	}
	v.clearKeysLocked()
	v.mode = ModeMaster
	v.salt = salt
	v.kek = kek
	v.dek = dek
	v.lastUsedAt = v.now()
	return nil
}

func (v *Vault) InitDPAPI(ctx context.Context) error {
	v.mu.Lock()
	err := v.initDPAPILocked(ctx)
	v.mu.Unlock()
	if err == nil {
		v.fireUnlocked()
	}
	return err
}

func (v *Vault) initDPAPILocked(ctx context.Context) error {
	if v.mode != ModeNotInit {
		return ipc.NewError(ipc.CodeVaultAlreadyInit, "凭据库已初始化，不能重复初始化")
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

func (v *Vault) unlockDPAPILocked(ctx context.Context) error {
	encoded, found, err := v.store.SettingGet(ctx, settingEnvelope)
	if err != nil {
		return err
	}
	var dek *secretKey
	if !found || encoded == "" {
		existing, err := v.store.CredentialList(ctx)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			return ipc.NewError(ipc.CodeCrypto, "加密错误: 凭据库的密钥信封已丢失，但库内仍有密文；重建密钥会让它们永远无法解密，已拒绝")
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
		return nil, ipc.NewError(ipc.CodeVaultLocked, "凭据库已锁定，请先解锁")
	}
	return v.dek.clone(), nil
}

func (v *Vault) AutoLockIfIdle() {
	v.mu.Lock()
	defer v.mu.Unlock()
	idle := v.now() - v.lastUsedAt
	if v.autoLockMS > 0 && idle >= 0 && uint64(idle) > v.autoLockMS && v.mode == ModeMaster {
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
	if len(newPassword) < 8 {
		return ipc.BadParam(errString("主密码至少 8 位"))
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
		settingEnvelope: base64.StdEncoding.EncodeToString(newEnvelope),
		settingSalt:     base64.StdEncoding.EncodeToString(newSalt),
	}); err != nil {
		newKEK.destroy()
		return err
	}
	v.kek.destroy()
	v.kek = newKEK
	v.salt = newSalt
	v.lastUsedAt = v.now()
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }
