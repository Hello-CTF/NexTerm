package profiles_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestLegacyMigrationPersistsStableActiveProfile(t *testing.T) {
	ctx := context.Background()
	database := openStore(t)
	legacy := `{"baseUrl":" https://example.test/v1/// ","apiKey":" secret ","model":" legacy-model ","fallbackModel":" legacy-fallback ","contextWindow":100}`
	if err := database.SettingSet(ctx, profiles.LegacySettingKey, legacy); err != nil {
		t.Fatal(err)
	}
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	overview := manager.Overview()
	if len(overview.Profiles) != 1 || overview.ActiveID == nil {
		t.Fatalf("unexpected migrated overview: %+v", overview)
	}
	profile := overview.Profiles[0]
	if !ids.Valid(profile.ID) || *overview.ActiveID != profile.ID {
		t.Fatalf("migration did not allocate a stable active ID: %+v", overview)
	}
	if profile.Name != "legacy-model" || profile.BaseURL != "https://example.test/v1" || profile.APIKey != profiles.MaskedAPIKey || profile.FallbackModel != "legacy-fallback" {
		t.Fatalf("migration did not sanitize profile: %+v", profile)
	}
	if profile.Temperature != provider.DefaultTemperature || !profile.Stream || profile.ContextWindow != 1000 {
		t.Fatalf("migration defaults were not applied: %+v", profile)
	}

	reloaded, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Overview().Profiles[0].ID; got != profile.ID {
		t.Fatalf("reloaded ID = %s, want %s", got, profile.ID)
	}
	raw, found, err := database.SettingGet(ctx, profiles.SettingKey)
	if err != nil || !found {
		t.Fatalf("migrated setting missing: found=%v err=%v", found, err)
	}
	var persisted map[string]any
	if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted["version"] != float64(profiles.StoreVersion) {
		t.Fatalf("stored version = %v", persisted["version"])
	}
}

func TestMalformedCurrentStoreDoesNotMigrateLegacy(t *testing.T) {
	ctx := context.Background()
	database := openStore(t)
	if err := database.SettingSet(ctx, profiles.SettingKey, `{broken`); err != nil {
		t.Fatal(err)
	}
	if err := database.SettingSet(ctx, profiles.LegacySettingKey, `{"model":"must-not-return"}`); err != nil {
		t.Fatal(err)
	}
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	overview := manager.Overview()
	if len(overview.Profiles) != 0 || overview.ActiveID != nil {
		t.Fatalf("malformed current store should open an empty profile list: %+v", overview)
	}
}

func TestFutureStoreVersionFailsWithoutOverwrite(t *testing.T) {
	ctx := context.Background()
	database := openStore(t)
	raw := `{"version":99,"profiles":[],"activeId":null}`
	if err := database.SettingSet(ctx, profiles.SettingKey, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := profiles.NewManager(ctx, database); err == nil {
		t.Fatal("future store version was accepted")
	}
	got, _, _ := database.SettingGet(ctx, profiles.SettingKey)
	if got != raw {
		t.Fatal("failed load overwrote the future-version setting")
	}
}

func TestProfileLifecycleAndActiveRuntimeSnapshots(t *testing.T) {
	ctx := context.Background()
	manager, err := profiles.NewManager(ctx, openStore(t))
	if err != nil {
		t.Fatal(err)
	}
	first, _ := profiles.PresetProfile("openai")
	first.Name = " first "
	first.APIKey = " key "
	overview, err := manager.Save(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Profiles) != 1 || overview.ActiveID == nil {
		t.Fatalf("first profile was not activated: %+v", overview)
	}
	first = overview.Profiles[0]
	if first.Name != "first" || first.APIKey != profiles.MaskedAPIKey {
		t.Fatalf("profile was not normalized: %+v", first)
	}
	firstClient, err := manager.ActiveClient()
	if err != nil {
		t.Fatal(err)
	}

	second, _ := profiles.PresetProfile("deepseek")
	overview, err = manager.Save(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	second = overview.Profiles[1]
	if *overview.ActiveID != first.ID {
		t.Fatal("saving an inactive profile changed active runtime")
	}
	if _, err = manager.Activate(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := manager.ActiveConfig(); got.Model != "deepseek-chat" {
		t.Fatalf("active model = %s", got.Model)
	}
	if firstClient.Config().Model != "gpt-4o-mini" {
		t.Fatal("existing client snapshot changed after activation")
	}

	second.BaseURL = "https://replacement.test/v2/"
	if _, err = manager.Save(ctx, second); err != nil {
		t.Fatal(err)
	}
	if got, _ := manager.ActiveConfig(); got.BaseURL != "https://replacement.test/v2" {
		t.Fatalf("active replacement was not applied: %s", got.BaseURL)
	}
	if _, err = manager.Activate(ctx, "missing"); !errors.Is(err, profiles.ErrProfileNotFound) {
		t.Fatalf("unknown activation error = %v", err)
	}

	overview, err = manager.Delete(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if overview.ActiveID == nil || *overview.ActiveID != first.ID {
		t.Fatalf("deleting active profile did not select the first remainder: %+v", overview)
	}
	if _, err = manager.Delete(ctx, "missing"); err != nil {
		t.Fatal(err)
	}
	overview, err = manager.Delete(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Profiles) != 0 || overview.ActiveID != nil {
		t.Fatalf("deleting final profile left state behind: %+v", overview)
	}
	if _, err = manager.ActiveClient(); !errors.Is(err, profiles.ErrNoActiveProfile) {
		t.Fatalf("deleted provider is still usable: %v", err)
	}
	if firstClient.Config().Model != "gpt-4o-mini" {
		t.Fatal("deleting profiles mutated an in-flight client snapshot")
	}
}

func TestLoadRepairsIDsOrderAndActive(t *testing.T) {
	ctx := context.Background()
	database := openStore(t)
	raw := `{"profiles":[` +
		`{"id":"same","name":" A ","baseUrl":"https://a.test/","model":"a","temperature":9,"contextWindow":0},` +
		`{"id":"same","model":"duplicate"},` +
		`{"id":"","model":"new-id","stream":false}],"activeId":"missing"}`
	if err := database.SettingSet(ctx, profiles.SettingKey, raw); err != nil {
		t.Fatal(err)
	}
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	overview := manager.Overview()
	if len(overview.Profiles) != 2 {
		t.Fatalf("profiles = %+v", overview.Profiles)
	}
	if overview.Profiles[0].ID != "same" || overview.Profiles[0].Name != "A" || overview.Profiles[0].Temperature != 2 || overview.Profiles[0].ContextWindow != 1000 {
		t.Fatalf("first profile was not repaired: %+v", overview.Profiles[0])
	}
	if !ids.Valid(overview.Profiles[1].ID) || overview.Profiles[1].Stream {
		t.Fatalf("empty ID or explicit stream=false was not preserved: %+v", overview.Profiles[1])
	}
	if overview.ActiveID == nil || *overview.ActiveID != "same" {
		t.Fatalf("active ID was not repaired: %+v", overview.ActiveID)
	}
	reloaded, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Overview().Profiles[1].ID != overview.Profiles[1].ID {
		t.Fatal("repair was not persisted")
	}
}

func TestConcurrentSaveDoesNotLoseProfiles(t *testing.T) {
	ctx := context.Background()
	database := openStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	const count = 24
	var wait sync.WaitGroup
	errorsCh := make(chan error, count)
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			profile, _ := profiles.PresetProfile("openai")
			profile.ID = ""
			profile.Model = fmt.Sprintf("model-%02d", index)
			_, err := manager.Save(ctx, profile)
			errorsCh <- err
		}(index)
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	overview := manager.Overview()
	if len(overview.Profiles) != count {
		t.Fatalf("concurrent saves left %d profiles, want %d", len(overview.Profiles), count)
	}
	seen := make(map[string]bool, count)
	for _, profile := range overview.Profiles {
		if seen[profile.ID] {
			t.Fatalf("duplicate ID %s", profile.ID)
		}
		seen[profile.ID] = true
	}
	reloaded, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reloaded.Overview().Profiles); got != count {
		t.Fatalf("persisted profile count = %d, want %d", got, count)
	}
}

func TestPresetProfiles(t *testing.T) {
	for _, preset := range provider.Presets() {
		profile, err := profiles.PresetProfile(preset.ID)
		if err != nil {
			t.Fatal(err)
		}
		if profile.ID != "" || profile.APIKey != "" || profile.Name != preset.ID || profile.Model != preset.Config.Model {
			t.Fatalf("invalid preset profile %s: %+v", preset.ID, profile)
		}
	}
	if _, err := profiles.PresetProfile("missing"); !errors.Is(err, provider.ErrUnknownPreset) {
		t.Fatalf("unknown preset error = %v", err)
	}
}
