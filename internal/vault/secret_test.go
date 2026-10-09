package vault

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestSecretEnvelopeRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	v := loadTestVault(db, &fakeProtector{})
	if err := v.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	envelope, err := v.EncryptSecret(ctx, "sk-live-中文-key")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(envelope, store.SecretEnvelopePrefix) {
		t.Fatalf("envelope %q lacks prefix %q", envelope, store.SecretEnvelopePrefix)
	}
	if strings.Contains(envelope, "sk-live") {
		t.Fatal("envelope leaks plaintext")
	}
	plaintext, err := v.DecryptSecret(ctx, envelope)
	if err != nil || plaintext != "sk-live-中文-key" {
		t.Fatalf("round trip = %q, %v", plaintext, err)
	}
	if empty, err := v.EncryptSecret(ctx, ""); err != nil || empty != "" {
		t.Fatalf("empty encrypt = %q, %v", empty, err)
	}
	reloaded := loadTestVault(db, &fakeProtector{})
	if err := reloaded.UnlockMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	plaintext, err = reloaded.DecryptSecret(ctx, envelope)
	if err != nil || plaintext != "sk-live-中文-key" {
		t.Fatalf("reloaded round trip = %q, %v", plaintext, err)
	}
}

func TestSecretEnvelopeLockedAndMalformed(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	v := loadTestVault(db, &fakeProtector{})
	if err := v.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	envelope, err := v.EncryptSecret(ctx, "secret")
	if err != nil {
		t.Fatal(err)
	}
	v.Lock()
	requireVaultCode(t, func() error { _, err := v.EncryptSecret(ctx, "secret"); return err }(), ipc.CodeVaultLocked)
	requireVaultCode(t, func() error { _, err := v.DecryptSecret(ctx, envelope); return err }(), ipc.CodeVaultLocked)
	if err := v.UnlockMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	requireVaultCode(t, func() error { _, err := v.DecryptSecret(ctx, "plaintext"); return err }(), ipc.CodeDecrypt)
	requireVaultCode(t, func() error { _, err := v.DecryptSecret(ctx, store.SecretEnvelopePrefix+"!!!"); return err }(), ipc.CodeDecrypt)
	requireVaultCode(t, func() error { _, err := v.DecryptSecret(ctx, store.SecretEnvelopePrefix+"AA=="); return err }(), ipc.CodeDecrypt)
	tampered := []byte(envelope)
	tampered[len(tampered)-1] ^= 0x01
	requireVaultCode(t, func() error { _, err := v.DecryptSecret(ctx, string(tampered)); return err }(), ipc.CodeDecrypt)
}

func TestLoadAttachesSecretProtector(t *testing.T) {
	db := testStore(t)
	if db.SecretProtector() != nil {
		t.Fatal("fresh store must not carry a protector")
	}
	v := loadTestVault(db, &fakeProtector{})
	if db.SecretProtector() != store.SecretProtector(v) {
		t.Fatal("Load must attach the vault as secret protector")
	}
}
