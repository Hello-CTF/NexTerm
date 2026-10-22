package guard

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func ownerTestContext(contexts []context.Context) context.Context {
	if len(contexts) != 0 {
		return contexts[0]
	}
	return context.Background()
}

func mustGrantList(t *testing.T, grants *Grants, contexts ...context.Context) []DeviceGrant {
	t.Helper()
	listed, err := grants.List(ownerTestContext(contexts))
	if err != nil {
		t.Fatal(err)
	}
	return listed
}

func mustGrant(t *testing.T, grants *Grants, deviceID string, contexts ...context.Context) (DeviceGrant, bool) {
	t.Helper()
	grant, ok, err := grants.Get(ownerTestContext(contexts), deviceID)
	if err != nil {
		t.Fatal(err)
	}
	return grant, ok
}

func mustGrantRules(t *testing.T, grants *Grants, contexts ...context.Context) []GrantRule {
	t.Helper()
	rules, err := grants.Rules(ownerTestContext(contexts))
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func mustPermission(t *testing.T, manager *Manager, contexts ...context.Context) Config {
	t.Helper()
	config, err := manager.Get(ownerTestContext(contexts))
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func mustDeviceMode(t *testing.T, manager *Manager, deviceID string, contexts ...context.Context) (Mode, bool) {
	t.Helper()
	mode, ok, err := manager.DeviceMode(ownerTestContext(contexts), deviceID)
	if err != nil {
		t.Fatal(err)
	}
	return mode, ok
}

func mustWithDeviceMode(t *testing.T, manager *Manager, config Config, deviceID string, contexts ...context.Context) Config {
	t.Helper()
	config, err := manager.WithDeviceMode(ownerTestContext(contexts), config, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestConcurrentOwnerIsolationAndReload(t *testing.T) {
	settings := &memorySettings{values: make(map[string]string)}
	manager, err := NewManager(context.Background(), settings, WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewGrants(context.Background(), settings, nil, WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		ctx        context.Context
		mode       Mode
		deviceID   string
		deviceMode Mode
		kind       GrantKind
		path       string
	}{
		{ipc.WithUserID(context.Background(), "owner-a"), Silent, "device-a", Unattended, GrantKindTerminalWrite, "/a/**"},
		{ipc.WithUserID(context.Background(), "owner-b"), ReadOnly, "device-b", ReadWrite, GrantKindSessionExec, "/b/**"},
	}
	for _, test := range tests {
		if err := manager.Set(test.ctx, Config{Mode: test.mode}); err != nil {
			t.Fatal(err)
		}
		if err := manager.SetDeviceMode(test.ctx, test.deviceID, test.deviceMode); err != nil {
			t.Fatal(err)
		}
		if _, err := grants.Grant(test.ctx, test.deviceID, []GrantKind{test.kind}); err != nil {
			t.Fatal(err)
		}
		if _, err := grants.AddRule(test.ctx, test.deviceID, "write_file", test.path, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}

	var wait sync.WaitGroup
	for _, test := range tests {
		test := test
		for worker := 0; worker < 3; worker++ {
			wait.Add(1)
			go func() {
				defer wait.Done()
				for round := 0; round < 10; round++ {
					if err := manager.Set(test.ctx, Config{Mode: test.mode}); err != nil {
						t.Errorf("%s set permission: %v", ipc.OwnerID(test.ctx), err)
						return
					}
					if err := manager.SetDeviceMode(test.ctx, test.deviceID, test.deviceMode); err != nil {
						t.Errorf("%s set device mode: %v", ipc.OwnerID(test.ctx), err)
						return
					}
					if _, err := grants.Grant(test.ctx, test.deviceID, []GrantKind{test.kind}); err != nil {
						t.Errorf("%s grant: %v", ipc.OwnerID(test.ctx), err)
						return
					}
					if _, err := grants.AddRule(test.ctx, test.deviceID, "write_file", test.path, time.Time{}); err != nil {
						t.Errorf("%s add rule: %v", ipc.OwnerID(test.ctx), err)
						return
					}
					config, err := manager.Snapshot(test.ctx)
					if err != nil || config.Mode != test.mode {
						t.Errorf("%s permission = %+v, %v", ipc.OwnerID(test.ctx), config, err)
						return
					}
					listed, err := grants.List(test.ctx)
					if err != nil || len(listed) != 1 || listed[0].DeviceID != test.deviceID || !listed[0].Covers(test.kind) {
						t.Errorf("%s grants = %+v, %v", ipc.OwnerID(test.ctx), listed, err)
						return
					}
					rules, err := grants.Rules(test.ctx)
					if err != nil || len(rules) != 1 || rules[0].DeviceID != test.deviceID || rules[0].Path != test.path {
						t.Errorf("%s rules = %+v, %v", ipc.OwnerID(test.ctx), rules, err)
						return
					}
				}
			}()
		}
	}
	wait.Wait()
	if _, err := manager.Snapshot(context.Background()); err == nil {
		t.Fatal("ownerless permission request used shared state")
	}
	if _, err := grants.List(context.Background()); err == nil {
		t.Fatal("ownerless grant request used shared state")
	}

	externalManager, err := NewManager(context.Background(), settings, WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	externalGrants, err := NewGrants(context.Background(), settings, nil, WithShared(false))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		if err := externalManager.Set(test.ctx, Config{Mode: Unattended}); err != nil {
			t.Fatal(err)
		}
		if err := externalGrants.Revoke(test.ctx, test.deviceID); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Reload(tests[0].ctx); err != nil {
		t.Fatal(err)
	}
	if got := mustPermission(t, manager, tests[0].ctx).Mode; got != Unattended {
		t.Fatalf("owner A reload = %v", got)
	}
	if got := mustPermission(t, manager, tests[1].ctx).Mode; got != ReadOnly {
		t.Fatalf("owner A reload also refreshed owner B: %v", got)
	}
	if err := grants.Reload(tests[0].ctx); err != nil {
		t.Fatal(err)
	}
	if listed := mustGrantList(t, grants, tests[0].ctx); len(listed) != 0 {
		t.Fatalf("owner A grants after reload = %+v", listed)
	}
	if listed := mustGrantList(t, grants, tests[1].ctx); len(listed) != 1 || listed[0].DeviceID != tests[1].deviceID {
		t.Fatalf("owner A grant reload also refreshed owner B: %+v", listed)
	}
}
