package profiles_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
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
	manager, err := profiles.NewManager(ctx, openStore(t))
	if err != nil {
		t.Fatal(err)
	}
	saveSceneProfile(t, manager, "active-key", "active")
	masked := saveSceneProfile(t, manager, "********", "masked")
	if _, err := manager.ClientFor(masked.ID); !errors.Is(err, profiles.ErrProfileKeyUnavailable) {
		t.Fatalf("expected ErrProfileKeyUnavailable, got %v", err)
	}
	dotted := saveSceneProfile(t, manager, "••••••••", "dotted")
	if _, err := manager.ClientFor(dotted.ID); !errors.Is(err, profiles.ErrProfileKeyUnavailable) {
		t.Fatalf("expected ErrProfileKeyUnavailable for dotted mask, got %v", err)
	}
}
