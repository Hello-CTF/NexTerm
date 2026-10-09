package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

type fakeProtector struct {
	mu           sync.Mutex
	key          []byte
	protectCalls int
}

func (p *fakeProtector) Protect(data []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.protectCalls++
	p.key = append([]byte(nil), data...)
	return []byte("fake:v1"), nil
}

func (p *fakeProtector) Unprotect(envelope []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if string(envelope) != "fake:v1" || len(p.key) == 0 {
		return nil, errors.New("fake key unavailable")
	}
	return append([]byte(nil), p.key...), nil
}

func fastDerive(password string, salt []byte) (*secretKey, error) {
	hash := sha256.New()
	_, _ = hash.Write(salt)
	_, _ = hash.Write([]byte(password))
	key := &secretKey{}
	copy(key.bytes[:], hash.Sum(nil))
	return key, nil
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func loadTestVault(db *store.Store, protector Protector, options ...Option) *Vault {
	options = append(options, WithProtector(protector), func(v *Vault) { v.derive = fastDerive })
	return Load(context.Background(), db, options...)
}

func requireVaultCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %s", code)
	}
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("expected code %s, got %v", code, err)
	}
}

func stringPtr(value string) *string { return &value }

func TestMasterLifecyclePasswordChangeAndReload(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	v := loadTestVault(db, &fakeProtector{})
	if v.Status().Initialized {
		t.Fatal("new vault must be uninitialized")
	}
	requireVaultCode(t, v.InitMaster(ctx, "short"), ipc.CodeBadParam)
	if err := v.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if status := v.Status(); status.Mode != "master" || !status.Unlocked || status.AutoLockMinutes != 30 {
		t.Fatalf("unexpected status %+v", status)
	}
	nonce, blob, err := v.EncryptCredential(ctx, "p@ss 中文")
	if err != nil {
		t.Fatal(err)
	}
	credentialID, err := db.CredentialPut(ctx, store.CredentialInput{Name: "login", Kind: "password", Nonce: nonce, Blob: blob, KEKHint: v.KEKHint()})
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.CredentialGetRow(ctx, credentialID)
	if err != nil || row.Cipher != store.CipherAES256GCM {
		t.Fatalf("credential=%+v err=%v", row, err)
	}
	plaintext, err := v.DecryptCredential(ctx, row)
	if err != nil || string(plaintext) != "p@ss 中文" {
		t.Fatalf("plaintext=%q err=%v", plaintext, err)
	}

	v.Lock()
	_, err = v.DecryptCredential(ctx, row)
	requireVaultCode(t, err, ipc.CodeVaultLocked)
	requireVaultCode(t, v.UnlockMaster(ctx, "wrong-password"), ipc.CodeBadMasterPass)
	if err := v.UnlockMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if err := v.ChangeMasterPassword(ctx, "correct-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	v.Lock()
	requireVaultCode(t, v.UnlockMaster(ctx, "correct-password"), ipc.CodeBadMasterPass)
	if err := v.UnlockMaster(ctx, "new-password"); err != nil {
		t.Fatal(err)
	}
	plaintext, err = v.DecryptCredential(ctx, row)
	if err != nil || string(plaintext) != "p@ss 中文" {
		t.Fatalf("password change re-encrypted data: %q err=%v", plaintext, err)
	}

	reloaded := loadTestVault(db, &fakeProtector{})
	if reloaded.Status().Unlocked {
		t.Fatal("master vault must start locked")
	}
	if err := reloaded.UnlockMaster(ctx, "new-password"); err != nil {
		t.Fatal(err)
	}
	plaintext, err = reloaded.DecryptCredential(ctx, row)
	if err != nil || string(plaintext) != "p@ss 中文" {
		t.Fatalf("reload plaintext=%q err=%v", plaintext, err)
	}
}

func TestMasterAutoLockUsesActivity(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	now := int64(1000)
	v := loadTestVault(db, &fakeProtector{}, func(v *Vault) { v.now = func() int64 { return now } })
	if err := v.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if err := v.SetAutoLock(ctx, 1); err != nil {
		t.Fatal(err)
	}
	now += 60_001
	v.AutoLockIfIdle()
	if v.Status().Unlocked {
		t.Fatal("vault should lock after idle timeout")
	}
	if err := v.UnlockMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	now += 30_000
	if _, _, err := v.EncryptCredential(ctx, "activity"); err != nil {
		t.Fatal(err)
	}
	now += 60_000
	v.AutoLockIfIdle()
	if !v.Status().Unlocked {
		t.Fatal("idle exactly at threshold must remain unlocked")
	}
	now++
	v.AutoLockIfIdle()
	if v.Status().Unlocked {
		t.Fatal("vault should lock beyond threshold")
	}
}

func TestSystemModeFirstRunReloadAndNoAutoLock(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	protector := &fakeProtector{}
	now := int64(1000)
	v := loadTestVault(db, protector, func(v *Vault) { v.now = func() int64 { return now } })
	if err := v.InitDPAPI(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if status := v.Status(); status.Mode != "dpapi" || !status.Unlocked || status.SystemProtection {
		t.Fatalf("unexpected status %+v", status)
	}
	nonce, blob, err := v.EncryptCredential(ctx, "system-secret")
	if err != nil {
		t.Fatal(err)
	}
	row := store.CredentialRow{Cipher: store.CipherAES256GCM, Nonce: nonce, Blob: blob}
	if err := v.SetAutoLock(ctx, 0); err != nil {
		t.Fatal(err)
	}
	now += 1000
	v.AutoLockIfIdle()
	if !v.Status().Unlocked {
		t.Fatal("system mode must not use master idle autolock")
	}

	reloaded := loadTestVault(db, protector)
	if !reloaded.Status().Unlocked {
		t.Fatal("system vault should automatically unlock")
	}
	plaintext, err := reloaded.DecryptCredential(ctx, row)
	if err != nil || string(plaintext) != "system-secret" {
		t.Fatalf("plaintext=%q err=%v", plaintext, err)
	}

	protector.mu.Lock()
	protector.key = nil
	protector.mu.Unlock()
	degraded := loadTestVault(db, protector)
	if degraded.Status().Initialized {
		t.Fatal("unreadable system key should degrade to uninitialized")
	}
	if mode, _, _ := db.SettingGet(ctx, settingMode); mode != "dpapi" {
		t.Fatalf("load must not rewrite stored mode, got %q", mode)
	}
}

func TestSystemModeHealsOnlyWhenNoSecretsExist(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	protector := &fakeProtector{}
	if err := db.SettingSet(ctx, settingMode, "dpapi"); err != nil {
		t.Fatal(err)
	}
	healed := loadTestVault(db, protector)
	if !healed.Status().Unlocked {
		t.Fatal("empty system vault should heal a missing envelope")
	}

	dbWithSecret := testStore(t)
	protectorWithSecret := &fakeProtector{}
	if err := dbWithSecret.SettingSet(ctx, settingMode, "dpapi"); err != nil {
		t.Fatal(err)
	}
	if _, err := dbWithSecret.CredentialPut(ctx, store.CredentialInput{Name: "existing", Kind: "password", Nonce: make([]byte, 12), Blob: []byte{1}, KEKHint: "dpapi"}); err != nil {
		t.Fatal(err)
	}
	degraded := loadTestVault(dbWithSecret, protectorWithSecret)
	if degraded.Status().Initialized {
		t.Fatal("vault with ciphertext and no key must not regenerate")
	}
	err := degraded.InitDPAPI(ctx, "")
	requireVaultCode(t, err, ipc.CodeCrypto)
	if protectorWithSecret.protectCalls != 0 {
		t.Fatal("protector must not be called when existing secrets would be destroyed")
	}
}

func TestMasterInitRefusesToReplaceEnvelopeOverSecrets(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	if err := db.SettingSet(ctx, settingEnvelope, "do-not-replace"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CredentialPut(ctx, store.CredentialInput{Name: "existing", Kind: "password", Nonce: make([]byte, 12), Blob: []byte{1}, KEKHint: "dpapi"}); err != nil {
		t.Fatal(err)
	}
	v := loadTestVault(db, &fakeProtector{})
	err := v.InitMaster(ctx, "new-password")
	requireVaultCode(t, err, ipc.CodeCrypto)
	if envelope, _, _ := db.SettingGet(ctx, settingEnvelope); envelope != "do-not-replace" {
		t.Fatalf("envelope was replaced: %q", envelope)
	}
	if _, found, _ := db.SettingGet(ctx, settingMode); found {
		t.Fatal("failed initialization must not write a new mode")
	}
}

func TestLoadToleratesUnknownMode(t *testing.T) {
	db := testStore(t)
	if err := db.SettingSet(context.Background(), settingMode, "future-mode"); err != nil {
		t.Fatal(err)
	}
	if v := loadTestVault(db, &fakeProtector{}); v.Status().Initialized {
		t.Fatal("unknown mode must not block startup or appear initialized")
	}
}

func TestCryptoRoundTripAndTamper(t *testing.T) {
	key := &secretKey{}
	for i := range key.bytes {
		key.bytes[i] = byte(i)
	}
	dek := generateKey()
	envelope, err := sealDEK(key, dek)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := openDEK(key, envelope)
	if err != nil || !bytes.Equal(opened.bytes[:], dek.bytes[:]) {
		t.Fatalf("DEK round trip failed: %v", err)
	}
	other := generateKey()
	_, err = openDEK(other, envelope)
	requireVaultCode(t, err, ipc.CodeBadMasterPass)

	nonce, blob, err := sealCredential(dek, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	blob[0] ^= 1
	_, err = openCredential(dek, nonce, blob)
	requireVaultCode(t, err, ipc.CodeDecrypt)
}

func TestProductionMasterDerivation(t *testing.T) {
	key, err := deriveMasterKey("correct horse", []byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(key.bytes[:], make([]byte, DEKLength)) {
		t.Fatal("derived key is all zero")
	}
	if _, err := deriveMasterKey("password", []byte("short")); err == nil {
		t.Fatal("short salt must fail")
	}
}
