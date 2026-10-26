package vault

import (
	"context"
	"encoding/json"
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
	const aiSecret = "synthetic-ai-key-plaintext"
	aiEnvelope, err := credentialVault.EncryptSecret(ctx, aiSecret)
	if err != nil {
		t.Fatal(err)
	}
	legacyAI, err := json.Marshal(map[string]any{
		"version":  1,
		"profiles": []map[string]any{{"id": "legacy-profile", "name": "legacy", "apiKey": aiEnvelope, "model": "synthetic-model"}},
		"activeId": "legacy-profile",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GORM().Create(&store.Setting{Key: store.AIProfilesSettingKey, Value: string(legacyAI), UpdatedAt: 100}).Error; err != nil {
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
	if report.OwnerID != "admin" || report.AISettings.Profiles.Migrated != 1 {
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
	targetKey := store.AIProfilesSettingKeyForUser("admin")
	targetAI, found, err := database.SettingGet(ctx, targetKey)
	if err != nil || !found {
		t.Fatalf("read migrated AI setting while locked: found=%v err=%v", found, err)
	}
	var migrated struct {
		Profiles []struct {
			ID     string `json:"id"`
			APIKey string `json:"apiKey"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(targetAI), &migrated); err != nil {
		t.Fatal(err)
	}
	if len(migrated.Profiles) != 1 || migrated.Profiles[0].ID != "legacy-profile" || migrated.Profiles[0].APIKey != aiEnvelope {
		t.Fatal("AI migration changed the profile identity or encrypted key envelope")
	}
	if _, err := credentialVault.DecryptCredentialString(ctx, afterCredential); ipc.NormalizeError(err).Code != ipc.CodeVaultLocked {
		t.Fatalf("locked credential decrypt error = %v", err)
	}
	if _, err := credentialVault.DecryptSecret(ctx, aiEnvelope); ipc.NormalizeError(err).Code != ipc.CodeVaultLocked {
		t.Fatalf("locked AI key decrypt error = %v", err)
	}
	afterLockedAI, found, err := database.SettingGet(ctx, targetKey)
	if err != nil || !found || afterLockedAI != targetAI {
		t.Fatalf("locked decrypt attempts reconciled AI setting: found=%v err=%v", found, err)
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
	var decoded struct {
		Profiles []struct {
			APIKey string `json:"apiKey"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(targetAI), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Profiles) != 1 {
		t.Fatalf("migrated AI profiles = %+v", decoded.Profiles)
	}
	aiPlaintext, err := credentialVault.DecryptSecret(ctx, decoded.Profiles[0].APIKey)
	if err != nil || aiPlaintext != aiSecret {
		t.Fatalf("AI key after unlock = %q, err=%v", aiPlaintext, err)
	}
	repeated, err := dataupgrade.Migrate(ctx, database.GORM(), false)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.AI.Changed() || !repeated.AISettings.Empty() {
		t.Fatalf("unlocked reentry repeated AI migration: %+v", repeated)
	}
}
