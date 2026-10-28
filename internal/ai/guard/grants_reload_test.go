package guard

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type failingGetSettings struct {
	*memorySettings
	fail atomic.Bool
}

func (s *failingGetSettings) SettingGet(ctx context.Context, key string) (string, bool, error) {
	if s.fail.Load() {
		return "", false, errors.New("injected read failure")
	}
	return s.memorySettings.SettingGet(ctx, key)
}

// A failed Reload after a revoking sync apply must not leave the stale allowing cache in place.
func TestGrantsReloadFailureInvalidatesCache(t *testing.T) {
	ctx := context.Background()
	settings := &failingGetSettings{memorySettings: &memorySettings{values: map[string]string{}}}
	grants, err := NewGrants(ctx, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grants.Grant(ctx, "device-a", []GrantKind{GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	ruling := Confirm(KindProcess, "x")
	if got := grants.Evaluate(ctx, Config{Mode: ReadWrite}, ruling, nil, "device-a", "send_keys", "run-1"); got.Action == ActionAsk {
		t.Fatalf("grant not effective before revocation: %+v", got)
	}

	// Sync revokes the grant in storage, then the reload fails.
	settings.memorySettings.mu.Lock()
	delete(settings.memorySettings.values, DeviceGrantsSettingKeyForUser(ctx))
	settings.memorySettings.mu.Unlock()
	settings.fail.Store(true)
	if err := grants.Reload(ctx); err == nil {
		t.Fatal("Reload succeeded despite injected failure")
	}
	if got := grants.Evaluate(ctx, Config{Mode: ReadWrite}, ruling, nil, "device-a", "send_keys", "run-2"); got.Action == ActionAllow {
		t.Fatalf("stale grant still allowed after failed reload: %+v", got)
	}

	settings.fail.Store(false)
	if err := grants.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if got := grants.Evaluate(ctx, Config{Mode: ReadWrite}, ruling, nil, "device-a", "send_keys", "run-3"); got.Action != ActionAsk {
		t.Fatalf("revoked grant allowed after successful reload: %+v", got)
	}
}
