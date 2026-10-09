package profiles_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/vault"
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

func TestPlaintextProfileKeysRejectedUntilResaved(t *testing.T) {
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
	if got := storedSetting(t, database, profiles.SettingKey); got != raw {
		t.Fatal("load must not rewrite a plaintext-keyed store")
	}
	if _, err := manager.ActiveClient(); err == nil || !strings.Contains(err.Error(), "重新保存") {
		t.Fatalf("plaintext key must be rejected with re-save guidance: %v", err)
	}
	config, ok := manager.ActiveConfig()
	if !ok || config.APIKey != "" {
		t.Fatalf("active config must not carry the plaintext key: %+v", config)
	}

	saved := manager.Overview().Profiles[0]
	if _, err := manager.Save(ctx, saved); err != nil {
		t.Fatal(err)
	}
	stored := storedSetting(t, database, profiles.SettingKey)
	if strings.Contains(stored, "plaintext-key") {
		t.Fatal("save left the plaintext key in settings")
	}
	if !strings.Contains(stored, store.SecretEnvelopePrefix) {
		t.Fatalf("save wrote no envelope: %s", stored)
	}
	reloaded, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	requireClientKey(t, reloaded, "plaintext-key")
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

func TestLockedStartupWithPlaintextProfile(t *testing.T) {
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
	if _, err := manager.ActiveClient(); err == nil || !strings.Contains(err.Error(), "重新保存") {
		t.Fatalf("locked plaintext profile must report re-save guidance: %v", err)
	}
	if got := manager.Overview().Profiles[0].APIKey; got != profiles.MaskedAPIKey {
		t.Fatalf("locked overview key = %q", got)
	}
	if got := storedSetting(t, database, profiles.SettingKey); got != raw {
		t.Fatal("locked startup must leave the plaintext row untouched")
	}

	if err := credentialVault.UnlockMaster(ctx, "correct-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ActiveClient(); err == nil || !strings.Contains(err.Error(), "重新保存") {
		t.Fatalf("unlocked plaintext profile must still require re-save: %v", err)
	}
	if got := storedSetting(t, database, profiles.SettingKey); got != raw {
		t.Fatal("unlock must not rewrite the plaintext row without an explicit save")
	}
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

func TestLockedVaultBlocksConnectivityUntilUnlock(t *testing.T) {
	ctx := context.Background()
	database, credentialVault := openVaultStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	saveKeyedProfile(t, manager, "autolock", "sk-autolock")
	credentialVault.Lock()
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

func TestReloadAdoptsExternalEncryptedSave(t *testing.T) {
	ctx := context.Background()
	database, _ := openVaultStore(t)
	writer, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reader.Overview().Profiles); got != 0 {
		t.Fatalf("reader starts with %d profiles", got)
	}
	saveKeyedProfile(t, writer, "external", "sk-external")
	if err := reader.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(reader.Overview().Profiles); got != 1 {
		t.Fatalf("Reload did not adopt the external save: %d profiles", got)
	}
	requireClientKey(t, reader, "sk-external")
}

func TestReloadAdoptsExternalSaveWithoutVault(t *testing.T) {
	ctx := context.Background()
	database := openStore(t)
	writer, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	saveKeyedProfile(t, writer, "external", "sk-plaintext")
	if err := reader.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(reader.Overview().Profiles); got != 1 {
		t.Fatalf("Reload did not adopt the external save: %d profiles", got)
	}
	requireClientKey(t, reader, "sk-plaintext")
}
