package profiles_test

import (
	"context"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
)

func TestFallbackProfileRoundTrip(t *testing.T) {
	ctx := context.Background()
	database := openStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := manager.Save(ctx, profiles.Profile{
		BaseURL: "https://example.test/v1", Model: "primary", FallbackModel: " fallback ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Profiles) != 1 || saved.Profiles[0].FallbackModel != "fallback" {
		t.Fatalf("saved fallback profile = %+v", saved)
	}
	reloaded, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	config, ok := reloaded.ActiveConfig()
	if !ok || config.Model != "primary" || config.FallbackModel != "fallback" {
		t.Fatalf("reloaded fallback config = %+v, active=%v", config, ok)
	}
	profile := saved.Profiles[0]
	profile.FallbackModel = " primary "
	updated, err := reloaded.Save(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Profiles[0].FallbackModel != "" {
		t.Fatalf("non-alternate fallback persisted = %+v", updated.Profiles[0])
	}
}
