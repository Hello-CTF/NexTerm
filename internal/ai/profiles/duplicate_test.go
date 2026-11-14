package profiles_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
)

// A copy keeps the original's key without the key passing through the caller,
// and copying changes neither the original nor the default profile.
func TestDuplicateCopiesTheKeyUnderANewID(t *testing.T) {
	ctx := context.Background()
	database, _ := openVaultStore(t)
	manager, err := profiles.NewManager(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	overview, err := manager.Save(ctx, profiles.Profile{Name: "Work", BaseURL: "https://a.example/v1", APIKey: "secret-key", Model: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	original := overview.Profiles[0]
	copied, err := manager.Duplicate(ctx, original.ID, "Work copy")
	if err != nil {
		t.Fatal(err)
	}
	if copied.ID == "" || copied.ID == original.ID || copied.Name != "Work copy" || copied.BaseURL != original.BaseURL || copied.Model != "m1" {
		t.Fatalf("copy = %+v", copied)
	}
	if copied.APIKey != profiles.MaskedAPIKey {
		t.Fatalf("copy key = %q, want it masked", copied.APIKey)
	}
	overview, err = manager.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Profiles) != 2 || overview.ActiveID == nil || *overview.ActiveID != original.ID {
		t.Fatalf("overview after copy = %+v", overview)
	}
	if _, err := manager.Activate(ctx, copied.ID); err != nil {
		t.Fatal(err)
	}
	config, ok, err := manager.ActiveConfig(ctx)
	if err != nil || !ok || config.APIKey != "secret-key" {
		t.Fatalf("copy config key = %q, %v, %v", config.APIKey, ok, err)
	}
	if _, err := manager.Duplicate(ctx, "missing", ""); !errors.Is(err, profiles.ErrProfileNotFound) {
		t.Fatalf("copy of a missing profile = %v", err)
	}
}
