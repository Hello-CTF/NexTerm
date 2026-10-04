package profiles_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

func openVaultStore(t *testing.T) (*store.Store, *vault.Vault) {
	t.Helper()
	ctx := context.Background()
	database := openStore(t)
	credentialVault := vault.Load(ctx, database)
	if err := credentialVault.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	return database, credentialVault
}

func requireIPCCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %s", code)
	}
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("expected code %s, got %v", code, err)
	}
}

func storedSetting(t *testing.T, database *store.Store, key string) string {
	t.Helper()
	raw, found, err := database.SettingGet(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("setting %s: found=%v err=%v", key, found, err)
	}
	return raw
}

func TestSaveEncryptsAPIKeyAtRest(t *testing.T) {
	ctx := context.Background()
	database, _ := openVaultStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	profile := profiles.DefaultProfile()
	profile.Name = "encrypted"
	profile.BaseURL = "https://ai.example/v1"
	profile.APIKey = "sk-live-secret"
	profile.Model = "model-a"
	overview, err := manager.Save(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Profiles[0].APIKey != profiles.MaskedAPIKey {
		t.Fatalf("overview leaked api key: %+v", overview.Profiles[0])
	}
	raw := storedSetting(t, database, profiles.SettingKey)
	if strings.Contains(raw, "sk-live-secret") {
		t.Fatal("settings table still holds the plaintext key")
	}
	if !strings.Contains(raw, store.SecretEnvelopePrefix) {
		t.Fatalf("settings table holds no envelope: %s", raw)
	}
	config, ok := manager.ActiveConfig()
	if !ok || config.APIKey != "sk-live-secret" {
		t.Fatalf("active config lost the key: %+v", config)
	}
	reloaded, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	client, err := reloaded.ActiveClient()
	if err != nil {
		t.Fatal(err)
	}
	if client.Config().APIKey != "sk-live-secret" {
		t.Fatalf("provider client key = %q", client.Config().APIKey)
	}
	if reloaded.Overview().Profiles[0].APIKey != profiles.MaskedAPIKey {
		t.Fatal("reloaded overview leaked api key")
	}
}

func TestPlaintextProfileKeysMigrateOnRead(t *testing.T) {
	ctx := context.Background()
	database := openStore(t)
	raw := `{"version":1,"profiles":[{"id":"p1","name":"legacy","baseUrl":"https://a.test/v1","apiKey":"plaintext-key","model":"m","temperature":0.3,"contextWindow":1000,"proxy":null,"stream":true}],"activeId":"p1"}`
	if err := database.SettingSet(ctx, profiles.SettingKey, raw); err != nil {
		t.Fatal(err)
	}
	credentialVault := vault.Load(ctx, database)
	if err := credentialVault.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	stored := storedSetting(t, database, profiles.SettingKey)
	if strings.Contains(stored, "plaintext-key") {
		t.Fatal("migration left the plaintext key in settings")
	}
	if !strings.Contains(stored, store.SecretEnvelopePrefix) {
		t.Fatalf("migration wrote no envelope: %s", stored)
	}
	config, ok := manager.ActiveConfig()
	if !ok || config.APIKey != "plaintext-key" {
		t.Fatalf("active config lost the migrated key: %+v", config)
	}
}

func TestMaskedRoundTripKeepsStoredKey(t *testing.T) {
	ctx := context.Background()
	database, _ := openVaultStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	profile := profiles.DefaultProfile()
	profile.Name = "round-trip"
	profile.BaseURL = "https://ai.example/v1"
	profile.APIKey = "sk-original"
	profile.Model = "model-a"
	overview, err := manager.Save(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	saved := overview.Profiles[0]
	saved.Name = "renamed"
	if _, err := manager.Save(ctx, saved); err != nil {
		t.Fatal(err)
	}
	config, ok := manager.ActiveConfig()
	if !ok || config.APIKey != "sk-original" {
		t.Fatalf("masked round trip replaced the key: %+v", config)
	}
	reloaded, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if config, ok = reloaded.ActiveConfig(); !ok || config.APIKey != "sk-original" {
		t.Fatalf("reloaded key = %+v", config)
	}
}

func TestLockedVaultKeepsEnvelopesAndBlocksConnectivity(t *testing.T) {
	ctx := context.Background()
	database, credentialVault := openVaultStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	profile := profiles.DefaultProfile()
	profile.Name = "locked"
	profile.BaseURL = "https://ai.example/v1"
	profile.APIKey = "sk-locked"
	profile.Model = "model-a"
	if _, err := manager.Save(ctx, profile); err != nil {
		t.Fatal(err)
	}
	credentialVault.Lock()

	locked, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := locked.ActiveClient(); err == nil {
		t.Fatal("locked vault still produced a provider client")
	} else {
		requireIPCCode(t, err, ipc.CodeVaultLocked)
	}
	overview := locked.Overview()
	if overview.Profiles[0].APIKey != profiles.MaskedAPIKey {
		t.Fatalf("locked overview must still mask the stored key: %+v", overview.Profiles[0])
	}
	edited := overview.Profiles[0]
	edited.Name = "edited-while-locked"
	if _, err := locked.Save(ctx, edited); err != nil {
		t.Fatal(err)
	}
	stored := storedSetting(t, database, profiles.SettingKey)
	if !strings.Contains(stored, store.SecretEnvelopePrefix) || strings.Contains(stored, "sk-locked") {
		t.Fatalf("locked save corrupted the envelope: %s", stored)
	}
	fresh := profiles.DefaultProfile()
	fresh.Name = "new-while-locked"
	fresh.BaseURL = "https://ai.example/v1"
	fresh.APIKey = "sk-fresh"
	fresh.Model = "model-b"
	if _, err := locked.Save(ctx, fresh); err == nil {
		t.Fatal("saving a new plaintext key while locked must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeVaultLocked)
	}

	if err := credentialVault.UnlockMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if err := locked.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	client, err := locked.ActiveClient()
	if err != nil {
		t.Fatal(err)
	}
	if client.Config().APIKey != "sk-locked" {
		t.Fatalf("unlocked client key = %q", client.Config().APIKey)
	}
}

func TestLegacyPlaintextProviderMigratesAndDeletesLegacyKey(t *testing.T) {
	ctx := context.Background()
	database := openStore(t)
	legacy := `{"baseUrl":"https://legacy.test/v1","apiKey":"legacy-secret","model":"legacy-model","temperature":0.3,"contextWindow":1000,"proxy":null,"stream":true}`
	if err := database.SettingSet(ctx, profiles.LegacySettingKey, legacy); err != nil {
		t.Fatal(err)
	}
	credentialVault := vault.Load(ctx, database)
	if err := credentialVault.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := database.SettingGet(ctx, profiles.LegacySettingKey); err != nil || found {
		t.Fatalf("legacy setting survived migration: found=%v err=%v", found, err)
	}
	stored := storedSetting(t, database, profiles.SettingKey)
	if strings.Contains(stored, "legacy-secret") {
		t.Fatal("migrated profile still exposes the legacy plaintext key")
	}
	config, ok := manager.ActiveConfig()
	if !ok || config.APIKey != "legacy-secret" || config.Model != "legacy-model" {
		t.Fatalf("migrated config = %+v", config)
	}
}

func TestClearingAPIKeyRemovesKeyMaterial(t *testing.T) {
	ctx := context.Background()
	database, _ := openVaultStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	profile := profiles.DefaultProfile()
	profile.Name = "clear"
	profile.BaseURL = "https://ai.example/v1"
	profile.APIKey = "sk-clear-me"
	profile.Model = "model-a"
	overview, err := manager.Save(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	cleared := overview.Profiles[0]
	cleared.APIKey = ""
	if _, err := manager.Save(ctx, cleared); err != nil {
		t.Fatal(err)
	}
	stored := storedSetting(t, database, profiles.SettingKey)
	if strings.Contains(stored, store.SecretEnvelopePrefix) || strings.Contains(stored, "sk-clear-me") {
		t.Fatalf("cleared key survived: %s", stored)
	}
	if got := manager.Overview().Profiles[0].APIKey; got != "" {
		t.Fatalf("cleared overview key = %q", got)
	}
}
