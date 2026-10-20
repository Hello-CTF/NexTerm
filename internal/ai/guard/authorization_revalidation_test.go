package guard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type blockingGrantSettings struct {
	values      map[string]string
	readStarted chan struct{}
	release     chan struct{}
	once        sync.Once
	block       bool
}

func newBlockingGrantSettings() *blockingGrantSettings {
	return &blockingGrantSettings{values: make(map[string]string), readStarted: make(chan struct{}), release: make(chan struct{})}
}

func (s *blockingGrantSettings) SettingGet(_ context.Context, key string) (string, bool, error) {
	if s.block {
		s.once.Do(func() {
			close(s.readStarted)
			<-s.release
		})
	}
	value, ok := s.values[key]
	return value, ok, nil
}

func (s *blockingGrantSettings) SettingSet(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

func waitGrantTest(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for grant operation")
	}
}

func TestReloadCannotRestoreRevokedState(t *testing.T) {
	for _, test := range []struct {
		name   string
		revoke func(*Grants) error
	}{
		{name: "grant", revoke: func(grants *Grants) error { return grants.Revoke(context.Background(), "device-a") }},
		{name: "rule", revoke: func(grants *Grants) error {
			rules, err := grants.Rules(context.Background())
			if err != nil {
				return err
			}
			return grants.RevokeRule(context.Background(), rules[0].ID)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := newBlockingGrantSettings()
			grants, err := NewGrants(context.Background(), settings, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := grants.Grant(context.Background(), "device-a", []GrantKind{GrantKindTerminalWrite}); err != nil {
				t.Fatal(err)
			}
			if _, err := grants.AddRule(context.Background(), "device-a", "write_file", "/var/log/**", time.Time{}); err != nil {
				t.Fatal(err)
			}
			settings.block = true
			reloadDone := make(chan error, 1)
			go func() { reloadDone <- grants.Reload(context.Background()) }()
			<-settings.readStarted
			revokeDone := make(chan error, 1)
			go func() { revokeDone <- test.revoke(grants) }()
			close(settings.release)
			waitGrantTest(t, reloadDone)
			waitGrantTest(t, revokeDone)

			ruling := Confirm(KindWriteFS, "write")
			if test.name == "grant" {
				if decision := grants.Evaluate(context.Background(), Config{Mode: ReadWrite}, ruling, nil, "device-a", "send_keys", "run"); decision.Action == ActionAllow {
					t.Fatal("revoked grant was restored by reload")
				}
			} else {
				if decision := grants.EvaluateResource(context.Background(), Config{Mode: ReadWrite}, ruling, nil, "device-a", "write_file", "run", Resource{Action: "write_file", Path: "/var/log/app.log"}); decision.Action == ActionAllow {
					t.Fatal("revoked rule was restored by reload")
				}
			}
		})
	}
}

func TestPersistedAuthorizationRevalidatesTargetBeforeUse(t *testing.T) {
	foreign := false
	validator := func(context.Context, string) error {
		if foreign {
			return errors.New("foreign target")
		}
		return nil
	}
	settings := &memorySettings{values: make(map[string]string)}
	grants, err := NewGrants(context.Background(), settings, nil, WithTargetValidator(validator))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grants.Grant(context.Background(), "device-a", []GrantKind{GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	if _, err := grants.AddRule(context.Background(), "device-a", "write_file", "/var/log/**", time.Time{}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(context.Background(), settings, WithTargetValidator(validator))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetDeviceMode(context.Background(), "device-a", Silent); err != nil {
		t.Fatal(err)
	}
	foreign = true
	ruling := Confirm(KindProcess, "write")
	if decision := grants.Evaluate(context.Background(), Config{Mode: ReadWrite}, ruling, nil, "device-a", "send_keys", "run"); decision.Action != ActionDeny {
		t.Fatalf("foreign persisted grant decision = %+v", decision)
	}
	if decision := grants.EvaluateResource(context.Background(), Config{Mode: ReadWrite}, ruling, nil, "device-a", "write_file", "run", Resource{Action: "write_file", Path: "/var/log/app.log"}); decision.Action != ActionDeny {
		t.Fatalf("foreign persisted rule decision = %+v", decision)
	}
	if _, err := manager.WithDeviceMode(context.Background(), Config{Mode: ReadWrite}, "device-a"); err == nil {
		t.Fatal("foreign persisted device mode was applied")
	}
}
