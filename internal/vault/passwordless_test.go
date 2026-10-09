package vault

import (
	"context"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestMasterEmptyPasswordLifecycle(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	v := loadTestVault(db, &fakeProtector{})
	if err := v.InitMaster(ctx, ""); err != nil {
		t.Fatal(err)
	}
	status := v.Status()
	if status.Mode != "master" || !status.Unlocked || !status.Passwordless {
		t.Fatalf("unexpected status %+v", status)
	}
	nonce, blob, err := v.EncryptCredential(ctx, "no-password-secret")
	if err != nil {
		t.Fatal(err)
	}
	row := store.CredentialRow{Cipher: store.CipherAES256GCM, Nonce: nonce, Blob: blob}
	plaintext, err := v.DecryptCredential(ctx, row)
	if err != nil || string(plaintext) != "no-password-secret" {
		t.Fatalf("plaintext=%q err=%v", plaintext, err)
	}

	reloaded := loadTestVault(db, &fakeProtector{})
	reloadedStatus := reloaded.Status()
	if !reloadedStatus.Unlocked || !reloadedStatus.Passwordless {
		t.Fatalf("passwordless vault must auto-unlock after reload: %+v", reloadedStatus)
	}
	plaintext, err = reloaded.DecryptCredential(ctx, row)
	if err != nil || string(plaintext) != "no-password-secret" {
		t.Fatalf("reload plaintext=%q err=%v", plaintext, err)
	}
	reloaded.Lock()
	requireVaultCode(t, reloaded.UnlockMaster(ctx, "wrong-password"), ipc.CodeBadMasterPass)
	if err := reloaded.UnlockMaster(ctx, ""); err != nil {
		t.Fatal(err)
	}
}

func TestMasterEmptyPasswordStillRejectsShortNonEmpty(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	v := loadTestVault(db, &fakeProtector{})
	requireVaultCode(t, v.InitMaster(ctx, "short"), ipc.CodeBadParam)
	if err := v.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if v.Status().Passwordless {
		t.Fatal("password vault must not report passwordless")
	}
	requireVaultCode(t, v.ChangeMasterPassword(ctx, "correct-password", "short"), ipc.CodeBadParam)
}

func TestChangePasswordToEmptyDisablesProtection(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	v := loadTestVault(db, &fakeProtector{})
	if err := v.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	nonce, blob, err := v.EncryptCredential(ctx, "secret")
	if err != nil {
		t.Fatal(err)
	}
	row := store.CredentialRow{Cipher: store.CipherAES256GCM, Nonce: nonce, Blob: blob}

	if err := v.ChangeMasterPassword(ctx, "correct-password", ""); err != nil {
		t.Fatal(err)
	}
	if !v.Status().Passwordless {
		t.Fatal("emptying the password must disable password protection")
	}
	v.Lock()
	requireVaultCode(t, v.UnlockMaster(ctx, "correct-password"), ipc.CodeBadMasterPass)
	if err := v.UnlockMaster(ctx, ""); err != nil {
		t.Fatal(err)
	}
	plaintext, err := v.DecryptCredential(ctx, row)
	if err != nil || string(plaintext) != "secret" {
		t.Fatalf("plaintext=%q err=%v", plaintext, err)
	}

	reloaded := loadTestVault(db, &fakeProtector{})
	if !reloaded.Status().Unlocked || !reloaded.Status().Passwordless {
		t.Fatalf("emptied vault must auto-unlock after reload: %+v", reloaded.Status())
	}

	if err := reloaded.ChangeMasterPassword(ctx, "", "new-password"); err != nil {
		t.Fatal(err)
	}
	if reloaded.Status().Passwordless {
		t.Fatal("setting a password must re-enable password protection")
	}
	reloaded.Lock()
	requireVaultCode(t, reloaded.UnlockMaster(ctx, ""), ipc.CodeBadMasterPass)
	if err := reloaded.UnlockMaster(ctx, "new-password"); err != nil {
		t.Fatal(err)
	}
}

func TestPasswordlessVaultSkipsAutoLock(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	now := int64(1000)
	v := loadTestVault(db, &fakeProtector{}, func(v *Vault) { v.now = func() int64 { return now } })
	if err := v.InitMaster(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := v.SetAutoLock(ctx, 1); err != nil {
		t.Fatal(err)
	}
	now += 60_001
	v.AutoLockIfIdle()
	if !v.Status().Unlocked {
		t.Fatal("passwordless vault must not auto-lock")
	}
}

func TestPasswordlessFlagDefaultsFalseForPasswordVault(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	v := loadTestVault(db, &fakeProtector{})
	if err := v.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if raw, _, err := db.SettingGet(ctx, settingPasswordless); err != nil || raw != "0" {
		t.Fatalf("stored passwordless flag = %q err=%v", raw, err)
	}
	reloaded := loadTestVault(db, &fakeProtector{})
	if reloaded.Status().Passwordless || reloaded.Status().Unlocked {
		t.Fatalf("password vault must stay locked and password-protected after reload: %+v", reloaded.Status())
	}
}

func TestMasterToDPAPIMigration(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	protector := &fakeProtector{}
	v := loadTestVault(db, protector)
	if err := v.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	nonce, blob, err := v.EncryptCredential(ctx, "secret")
	if err != nil {
		t.Fatal(err)
	}
	row := store.CredentialRow{Cipher: store.CipherAES256GCM, Nonce: nonce, Blob: blob}

	v.Lock()
	requireVaultCode(t, v.InitDPAPI(ctx, "wrong-password"), ipc.CodeBadMasterPass)
	if mode, _, _ := db.SettingGet(ctx, settingMode); mode != "master" {
		t.Fatalf("failed migration must not change stored mode: %q", mode)
	}
	if err := v.InitDPAPI(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	status := v.Status()
	if status.Mode != "dpapi" || !status.Unlocked || status.Passwordless {
		t.Fatalf("unexpected status after migration: %+v", status)
	}
	plaintext, err := v.DecryptCredential(ctx, row)
	if err != nil || string(plaintext) != "secret" {
		t.Fatalf("migration must keep the DEK: plaintext=%q err=%v", plaintext, err)
	}
	requireVaultCode(t, v.InitDPAPI(ctx, ""), ipc.CodeVaultAlreadyInit)

	reloaded := loadTestVault(db, protector)
	if !reloaded.Status().Unlocked {
		t.Fatal("migrated vault must auto-unlock through the system protector")
	}
	plaintext, err = reloaded.DecryptCredential(ctx, row)
	if err != nil || string(plaintext) != "secret" {
		t.Fatalf("reload plaintext=%q err=%v", plaintext, err)
	}
}
