package profiles_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func saveSceneProfile(t *testing.T, manager *profiles.Manager, key, name string) profiles.Profile {
	t.Helper()
	profile := profiles.Profile{Name: name, BaseURL: "https://example.test/v1", APIKey: key, Model: "scene-model"}.Normalized()
	overview, err := manager.Save(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, saved := range overview.Profiles {
		if saved.Name == profile.Name {
			return saved
		}
	}
	t.Fatalf("saved profile %q not found", profile.Name)
	return profiles.Profile{}
}

func TestClientForResolvesExplicitProfile(t *testing.T) {
	ctx := context.Background()
	manager, err := profiles.NewManager(ctx, openStore(t))
	if err != nil {
		t.Fatal(err)
	}
	active := saveSceneProfile(t, manager, "active-key", "active")
	scene := saveSceneProfile(t, manager, "scene-key", "scene")
	if _, err := manager.ClientFor(active.ID); err != nil {
		t.Fatalf("active profile client: %v", err)
	}
	if _, err := manager.ClientFor(scene.ID); err != nil {
		t.Fatalf("scene profile client: %v", err)
	}
}

func TestClientForEmptyIDFallsBackToActive(t *testing.T) {
	ctx := context.Background()
	manager, err := profiles.NewManager(ctx, openStore(t))
	if err != nil {
		t.Fatal(err)
	}
	saveSceneProfile(t, manager, "active-key", "active")
	if _, err := manager.ClientFor(""); err != nil {
		t.Fatalf("fallback client: %v", err)
	}
}

func TestClientForUnknownProfileFails(t *testing.T) {
	ctx := context.Background()
	manager, err := profiles.NewManager(ctx, openStore(t))
	if err != nil {
		t.Fatal(err)
	}
	saveSceneProfile(t, manager, "active-key", "active")
	if _, err := manager.ClientFor("missing-profile"); !errors.Is(err, profiles.ErrProfileNotFound) {
		t.Fatalf("expected ErrProfileNotFound, got %v", err)
	}
}

func TestClientForRejectsMaskedSentinelKey(t *testing.T) {
	ctx := context.Background()
	database := openStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	saveSceneProfile(t, manager, "active-key", "active")
	masked := saveSceneProfile(t, manager, "masked-key", "masked")
	raw := storedSetting(t, database, profiles.SettingKey)
	tampered := strings.Replace(raw, `"apiKey":"masked-key"`, `"apiKey":"`+profiles.MaskedAPIKey+`"`, 1)
	if tampered == raw {
		t.Fatal("failed to tamper stored profile key")
	}
	if err := database.SettingSet(ctx, profiles.SettingKey, tampered); err != nil {
		t.Fatal(err)
	}
	reloaded, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.ClientFor(masked.ID); !errors.Is(err, profiles.ErrProfileKeyUnavailable) {
		t.Fatalf("expected ErrProfileKeyUnavailable, got %v", err)
	}
}

func TestClientForDecryptsEnvelopeLikeActiveClient(t *testing.T) {
	ctx := context.Background()
	database, _ := openVaultStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	overview := saveKeyedProfile(t, manager, "scene", "super-secret-key")
	if len(overview.Profiles) != 1 {
		t.Fatalf("overview = %+v", overview)
	}
	profile := overview.Profiles[0]
	stored := storedSetting(t, database, profiles.SettingKey)
	if !strings.Contains(stored, store.SecretEnvelopePrefix) || strings.Contains(stored, "super-secret-key") {
		t.Fatalf("stored profile is not envelope-encrypted: %s", stored)
	}
	client, err := manager.ClientFor(profile.ID)
	if err != nil {
		t.Fatalf("explicit profile must resolve through the vault like ActiveClient: %v", err)
	}
	if client == nil {
		t.Fatal("ClientFor returned nil client")
	}
}

func TestClientForPropagatesVaultLocked(t *testing.T) {
	ctx := context.Background()
	database, credentialVault := openVaultStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	overview := saveKeyedProfile(t, manager, "locked-scene", "locked-secret")
	if len(overview.Profiles) != 1 {
		t.Fatalf("overview = %+v", overview)
	}
	credentialVault.Lock()
	_, err = manager.ClientFor(overview.Profiles[0].ID)
	requireIPCCode(t, err, ipc.CodeVaultLocked)
}
