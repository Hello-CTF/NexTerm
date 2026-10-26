package vault

import (
	"context"
	"reflect"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	dataupgrade "github.com/Hello-CTF/NexTerm/internal/upgrade"
)

func TestUpgradeVaultPreservesSecretsWhileLockedDatabase(t *testing.T) {
	ctx := context.Background()
	database := dbtest.NewFixture(t).OpenStore(t)
	if err := database.GORM().Create(&store.AppUser{
		ID: "admin", Username: "admin", Role: "superadmin", PasswordHash: "synthetic-unused-hash",
		State: "active", CreatedAt: 1, UpdatedAt: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	credentialVault := loadTestVault(database, &fakeProtector{})
	const masterPassword = "synthetic-vault-password"
	if err := credentialVault.InitMaster(ctx, masterPassword); err != nil {
		t.Fatal(err)
	}
	const credentialSecret = "synthetic-credential-plaintext"
	nonce, blob, err := credentialVault.EncryptCredential(ctx, credentialSecret)
	if err != nil {
		t.Fatal(err)
	}
	credentialID, err := database.CredentialPut(ctx, store.CredentialInput{
		ID: "legacy-credential", Name: "legacy", Kind: "password", Nonce: nonce, Blob: blob, KEKHint: credentialVault.KEKHint(),
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeCredential, err := database.CredentialGetRow(ctx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	beforeVaultSettings := map[string]string{}
	for _, key := range []string{settingMode, settingEnvelope, settingSalt} {
		value, found, err := database.SettingGet(ctx, key)
		if err != nil || !found {
			t.Fatalf("read vault setting %s: found=%v err=%v", key, found, err)
		}
		beforeVaultSettings[key] = value
	}

	credentialVault.Lock()
	report, err := dataupgrade.Migrate(ctx, database.GORM(), false)
	if err != nil {
		t.Fatal(err)
	}
	if report.OwnerID != "admin" {
		t.Fatalf("vault upgrade report = %+v", report)
	}
	afterCredential, err := database.CredentialGetRow(ctx, credentialID)
	if err != nil {
		t.Fatal(err)
	}
	if afterCredential.OwnerID != "admin" || afterCredential.Cipher != beforeCredential.Cipher ||
		!reflect.DeepEqual(afterCredential.Nonce, beforeCredential.Nonce) || !reflect.DeepEqual(afterCredential.Blob, beforeCredential.Blob) {
		t.Fatalf("credential changed beyond ownership:\nbefore=%+v\nafter=%+v", beforeCredential, afterCredential)
	}
	if _, err := credentialVault.DecryptCredentialString(ctx, afterCredential); ipc.NormalizeError(err).Code != ipc.CodeVaultLocked {
		t.Fatalf("locked credential decrypt error = %v", err)
	}
	for key, before := range beforeVaultSettings {
		after, found, err := database.SettingGet(ctx, key)
		if err != nil || !found || after != before {
			t.Fatalf("vault setting %s changed during upgrade: found=%v err=%v", key, found, err)
		}
	}

	if err := credentialVault.UnlockMaster(ctx, masterPassword); err != nil {
		t.Fatal(err)
	}
	plaintext, err := credentialVault.DecryptCredentialString(ctx, afterCredential)
	if err != nil || plaintext != credentialSecret {
		t.Fatalf("credential after unlock = %q, err=%v", plaintext, err)
	}
	if _, err := dataupgrade.Migrate(ctx, database.GORM(), false); err != nil {
		t.Fatal(err)
	}
}
