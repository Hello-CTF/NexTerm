package profiles_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
)

type failingSettings struct {
	base profiles.Settings
	fail bool
}

func (s *failingSettings) SettingGet(ctx context.Context, key string) (string, bool, error) {
	return s.base.SettingGet(ctx, key)
}

func (s *failingSettings) SettingSet(ctx context.Context, key, value string) error {
	if s.fail {
		return errors.New("injected setting write failure")
	}
	return s.base.SettingSet(ctx, key, value)
}

func TestFailedSaveDoesNotCommitMemoryOrRuntime(t *testing.T) {
	ctx := context.Background()
	settings := &failingSettings{base: openStore(t)}
	manager, err := profiles.NewManager(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := profiles.PresetProfile("moonshot")
	overview, err := manager.Save(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	saved := overview.Profiles[0]

	settings.fail = true
	saved.Model = "must-not-commit"
	if _, err := manager.Save(ctx, saved); err == nil {
		t.Fatal("injected write failure was ignored")
	}
	active, ok := manager.ActiveProfile()
	if !ok || active.Model != "kimi-k3" {
		t.Fatalf("failed save changed runtime: %+v, active=%v", active, ok)
	}
	settings.fail = false
	reloaded, err := profiles.NewManager(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Overview().Profiles[0].Model; got != "kimi-k3" {
		t.Fatalf("failed save reached persistence: %s", got)
	}
}
