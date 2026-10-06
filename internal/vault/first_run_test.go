package vault

import (
	"context"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func TestNotInitVaultGuidesInitializationInsteadOfUnlock(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	v := loadTestVault(db, &fakeProtector{})

	_, _, err := v.EncryptCredential(ctx, "secret")
	requireVaultCode(t, err, ipc.CodeVaultNotInit)
	if !strings.Contains(err.Error(), "初始化") {
		t.Fatalf("first-run error should guide initialization, got %v", err)
	}
	row := store.CredentialRow{Cipher: store.CipherAES256GCM, Nonce: make([]byte, 12), Blob: []byte{1}}
	_, err = v.DecryptCredential(ctx, row)
	requireVaultCode(t, err, ipc.CodeVaultNotInit)
	requireVaultCode(t, v.UnlockMaster(ctx, "whatever-password"), ipc.CodeVaultNotInit)
	if status := v.Status(); status.Initialized || status.Unlocked {
		t.Fatalf("failed unlock must not change status: %+v", status)
	}
}

func TestLockedMasterVaultStillReportsLocked(t *testing.T) {
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

	v.Lock()
	_, _, err = v.EncryptCredential(ctx, "secret")
	requireVaultCode(t, err, ipc.CodeVaultLocked)
	if _, err := v.DecryptCredential(ctx, row); err != nil {
		requireVaultCode(t, err, ipc.CodeVaultLocked)
	}
	if err := v.UnlockMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	plaintext, err := v.DecryptCredential(ctx, row)
	if err != nil || string(plaintext) != "secret" {
		t.Fatalf("plaintext=%q err=%v", plaintext, err)
	}
}
