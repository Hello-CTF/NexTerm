package profiles_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func scanFileBytes(t *testing.T, path, token string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte(token)) {
		t.Fatalf("%s still contains plaintext token %q", path, token)
	}
}

func requireFileTokenPresent(t *testing.T, path, token string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte(token)) {
		t.Fatalf("%s does not contain expected token %q", path, token)
	}
}

func saveKeyedProfile(t *testing.T, manager *profiles.Manager, name, key string) profiles.Overview {
	t.Helper()
	profile := profiles.DefaultProfile()
	profile.Name = name
	profile.BaseURL = "https://ai.example/v1"
	profile.APIKey = key
	profile.Model = "model-a"
	overview, err := manager.Save(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	return overview
}

func requireClientKey(t *testing.T, manager *profiles.Manager, want string) {
	t.Helper()
	client, err := manager.ActiveClient()
	if err != nil {
		t.Fatal(err)
	}
	if client.Config().APIKey != want {
		t.Fatalf("client key = %q, want %q", client.Config().APIKey, want)
	}
}

func requireLockedClient(t *testing.T, manager *profiles.Manager) {
	t.Helper()
	if _, err := manager.ActiveClient(); err == nil {
		t.Fatal("locked vault still produced a provider client")
	} else {
		requireIPCCode(t, err, ipc.CodeVaultLocked)
	}
}

func TestSaveEncryptsAPIKeyAtRest(t *testing.T) {
	ctx := context.Background()
	database, _ := openVaultStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	overview := saveKeyedProfile(t, manager, "encrypted", "sk-live-secret")
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
	requireClientKey(t, reloaded, "sk-live-secret")
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
	overview := saveKeyedProfile(t, manager, "round-trip", "sk-original")
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

func TestLockedStartupWithPlaintextDefersMigration(t *testing.T) {
	ctx := context.Background()
	database := openStore(t)
	raw := `{"version":1,"profiles":[{"id":"p1","name":"legacy","baseUrl":"https://a.test/v1","apiKey":"locked-plaintext","model":"m","temperature":0.3,"contextWindow":1000,"proxy":null,"stream":true}],"activeId":"p1"}`
	if err := database.SettingSet(ctx, profiles.SettingKey, raw); err != nil {
		t.Fatal(err)
	}
	credentialVault := vault.Load(ctx, database)
	if err := credentialVault.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	credentialVault.Lock()

	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatalf("locked startup with plaintext keys must not fail: %v", err)
	}
	requireLockedClient(t, manager)
	if got := manager.Overview().Profiles[0].APIKey; got != profiles.MaskedAPIKey {
		t.Fatalf("locked overview key = %q", got)
	}
	if stored := storedSetting(t, database, profiles.SettingKey); !strings.Contains(stored, "locked-plaintext") {
		t.Fatal("locked startup must leave the plaintext row untouched")
	}

	if err := credentialVault.UnlockMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	stored := storedSetting(t, database, profiles.SettingKey)
	if strings.Contains(stored, "locked-plaintext") || !strings.Contains(stored, store.SecretEnvelopePrefix) {
		t.Fatalf("unlock did not migrate the plaintext key: %s", stored)
	}
	requireClientKey(t, manager, "locked-plaintext")
}

func TestLockedStartupWithLegacyProviderDefersMigration(t *testing.T) {
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
	credentialVault.Lock()

	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatalf("locked startup with legacy provider must not fail: %v", err)
	}
	requireLockedClient(t, manager)
	if _, found, err := database.SettingGet(ctx, profiles.LegacySettingKey); err != nil || !found {
		t.Fatalf("locked startup must keep the legacy row: found=%v err=%v", found, err)
	}

	if err := credentialVault.UnlockMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := database.SettingGet(ctx, profiles.LegacySettingKey); err != nil || found {
		t.Fatalf("legacy setting survived unlocked migration: found=%v err=%v", found, err)
	}
	stored := storedSetting(t, database, profiles.SettingKey)
	if strings.Contains(stored, "legacy-secret") || !strings.Contains(stored, store.SecretEnvelopePrefix) {
		t.Fatalf("legacy migration left plaintext: %s", stored)
	}
	requireClientKey(t, manager, "legacy-secret")
}

func TestManagerTracksVaultLockLifecycle(t *testing.T) {
	ctx := context.Background()
	database, credentialVault := openVaultStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	saveKeyedProfile(t, manager, "lifecycle", "sk-lifecycle")
	requireClientKey(t, manager, "sk-lifecycle")

	credentialVault.Lock()
	requireLockedClient(t, manager)
	if config, ok := manager.ActiveConfig(); !ok || config.APIKey != "" {
		t.Fatalf("locked active config leaked key material: %+v", config)
	}

	if err := credentialVault.UnlockMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	requireClientKey(t, manager, "sk-lifecycle")
}

func TestRestartedManagerRecoversOnUnlock(t *testing.T) {
	ctx := context.Background()
	database, credentialVault := openVaultStore(t)
	first, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	saveKeyedProfile(t, first, "restart", "sk-restart")
	credentialVault.Lock()

	restarted, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	requireLockedClient(t, restarted)
	if err := credentialVault.UnlockMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	requireClientKey(t, restarted, "sk-restart")
}

func TestAutoLockBlocksConnectivityUntilUnlock(t *testing.T) {
	ctx := context.Background()
	database, credentialVault := openVaultStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	saveKeyedProfile(t, manager, "autolock", "sk-autolock")
	if err := credentialVault.SetAutoLock(ctx, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	credentialVault.AutoLockIfIdle()
	requireLockedClient(t, manager)
	if err := credentialVault.UnlockMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	requireClientKey(t, manager, "sk-autolock")
}

func TestSaveWhileLocked(t *testing.T) {
	ctx := context.Background()
	database, credentialVault := openVaultStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	overview := saveKeyedProfile(t, manager, "locked-save", "sk-locked")
	credentialVault.Lock()

	edited := overview.Profiles[0]
	edited.Name = "edited-while-locked"
	if _, err := manager.Save(ctx, edited); err != nil {
		t.Fatalf("saving a masked round trip while locked must keep the envelope: %v", err)
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
	if _, err := manager.Save(ctx, fresh); err == nil {
		t.Fatal("saving a new plaintext key while locked must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeVaultLocked)
	}
	if err := credentialVault.UnlockMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	requireClientKey(t, manager, "sk-locked")
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
	overview := saveKeyedProfile(t, manager, "clear", "sk-clear-me")
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

func TestMigrationScrubsDatabaseFileBytes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	database, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"version":1,"profiles":[{"id":"p1","name":"legacy","baseUrl":"https://a.test/v1","apiKey":"pt-remnant-token-xyz","model":"m","temperature":0.3,"contextWindow":1000,"proxy":null,"stream":true}],"activeId":"p1"}`
	if err := database.SettingSet(ctx, profiles.SettingKey, raw); err != nil {
		t.Fatal(err)
	}
	requireFileTokenPresent(t, path+"-wal", "pt-remnant-token-xyz")
	credentialVault := vault.Load(ctx, database)
	if err := credentialVault.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if config, ok := manager.ActiveConfig(); !ok || config.APIKey != "pt-remnant-token-xyz" {
		t.Fatalf("migrated config = %+v", config)
	}
	stored := storedSetting(t, database, profiles.SettingKey)
	if strings.Contains(stored, "pt-remnant-token-xyz") || !strings.Contains(stored, store.SecretEnvelopePrefix) {
		t.Fatalf("migration left plaintext at SQL level: %s", stored)
	}
	scanFileBytes(t, path, "pt-remnant-token-xyz")
	scanFileBytes(t, path+"-wal", "pt-remnant-token-xyz")
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	scanFileBytes(t, path, "pt-remnant-token-xyz")
}

func TestLegacyMigrationScrubsDatabaseFileBytes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	database, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `{"baseUrl":"https://legacy.test/v1","apiKey":"lg-remnant-token-xyz","model":"legacy-model","temperature":0.3,"contextWindow":1000,"proxy":null,"stream":true}`
	if err := database.SettingSet(ctx, profiles.LegacySettingKey, legacy); err != nil {
		t.Fatal(err)
	}
	requireFileTokenPresent(t, path+"-wal", "lg-remnant-token-xyz")
	credentialVault := vault.Load(ctx, database)
	if err := credentialVault.InitMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	requireClientKey(t, manager, "lg-remnant-token-xyz")
	if _, found, err := database.SettingGet(ctx, profiles.LegacySettingKey); err != nil || found {
		t.Fatalf("legacy setting survived migration: found=%v err=%v", found, err)
	}
	scanFileBytes(t, path, "lg-remnant-token-xyz")
	scanFileBytes(t, path+"-wal", "lg-remnant-token-xyz")
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	scanFileBytes(t, path, "lg-remnant-token-xyz")
}
