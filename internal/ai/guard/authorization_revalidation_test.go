package guard

import (
	"context"
	"encoding/json"
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

func TestAutomaticAuthorizationGenerationSurvivesReloadAndRejectsStaleRecords(t *testing.T) {
	ctx := context.Background()
	settings := &memorySettings{values: make(map[string]string)}
	grants, err := NewGrants(ctx, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grants.Grant(ctx, "device-a", []GrantKind{GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	ruling := Confirm(KindProcess, "write")
	oldGrant := grants.Evaluate(ctx, Config{Mode: ReadWrite}, ruling, nil, "device-a", "send_keys", "run")
	if oldGrant.Authorization == nil {
		t.Fatal("grant decision has no authorization binding")
	}
	if err := grants.Revoke(ctx, "device-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := grants.Grant(ctx, "device-a", []GrantKind{GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	if err := grants.RevalidateAuthorization(ctx, oldGrant.Authorization); err == nil {
		t.Fatal("revoke and re-create revived the old grant admission")
	}
	newGrant := grants.Evaluate(ctx, Config{Mode: ReadWrite}, ruling, nil, "device-a", "send_keys", "run")
	if err := grants.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if err := grants.RevalidateAuthorization(ctx, newGrant.Authorization); err != nil {
		t.Fatalf("persisted grant generation did not survive reload: %v", err)
	}

	if _, err := grants.AddRule(ctx, "device-a", "write_file", "/var/log/**", time.Time{}); err != nil {
		t.Fatal(err)
	}
	resource := Resource{Action: "write_file", Path: "/var/log/app.log"}
	oldRule := grants.EvaluateResource(ctx, Config{Mode: ReadWrite}, Confirm(KindWriteFS, "write"), nil, "device-a", "write_file", "run", resource)
	if oldRule.Authorization == nil {
		t.Fatal("rule decision has no authorization binding")
	}
	if _, err := grants.AddRule(ctx, "device-a", "write_file", "/var/log/**", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := grants.RevalidateAuthorization(ctx, oldRule.Authorization); err == nil {
		t.Fatal("rule update revived the old rule admission")
	}
	newRule := grants.EvaluateResource(ctx, Config{Mode: ReadWrite}, Confirm(KindWriteFS, "write"), nil, "device-a", "write_file", "run", resource)
	if err := grants.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if err := grants.RevalidateAuthorization(ctx, newRule.Authorization); err != nil {
		t.Fatalf("persisted rule generation did not survive reload: %v", err)
	}
}

func TestAuthorizationAdmissionRechecksGenerationAfterTargetValidation(t *testing.T) {
	type admissionContextKey struct{}
	entered := make(chan struct{})
	release := make(chan struct{})
	validator := func(ctx context.Context, _ string) error {
		if ctx.Value(admissionContextKey{}) != nil {
			close(entered)
			<-release
		}
		return nil
	}
	grants, err := NewGrants(context.Background(), &memorySettings{values: make(map[string]string)}, nil, WithTargetValidator(validator))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := grants.Grant(ctx, "device-a", []GrantKind{GrantKindTerminalWrite}); err != nil {
		t.Fatal(err)
	}
	decision := grants.Evaluate(ctx, Config{Mode: ReadWrite}, Confirm(KindProcess, "write"), nil, "device-a", "send_keys", "run")
	done := make(chan error, 1)
	go func() {
		done <- grants.RevalidateAuthorization(context.WithValue(ctx, admissionContextKey{}, true), decision.Authorization)
	}()
	<-entered
	if err := grants.Revoke(ctx, "device-a"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("revoke completed during target validation but the old admission succeeded")
	}
}

// TestLegacyGrantRecordsAreRejectedNotMigrated pins the 1.3.0 contract: records
// written before 1.3.0 (no explicit permanence or no generation, including
// rows rewritten by an older sync peer) are never migrated on read or on
// evaluation. They stay byte-for-byte in storage, are listed as Legacy so the
// user sees them, and never authorize a call.
func TestLegacyGrantRecordsAreRejectedNotMigrated(t *testing.T) {
	ctx := context.Background()
	legacyGrants := `{"device-a":{"deviceId":"device-a","kinds":["terminal_write"],"permanent":true,"updatedAt":1},"device-b":{"deviceId":"device-b","kinds":["terminal_write"],"updatedAt":1,"generation":"gen-b"}}`
	legacyRules := `[{"id":"rule-a","deviceId":"device-a","action":"write_file","path":"/var/log/**"}]`
	settings := &memorySettings{values: map[string]string{
		DeviceGrantsSettingKey: legacyGrants,
		GrantRulesSettingKey:   legacyRules,
	}}
	manager, err := NewGrants(ctx, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, deviceID := range []string{"device-a", "device-b"} {
		if decision := manager.Evaluate(ctx, Config{Mode: ReadWrite}, Confirm(KindProcess, "write"), nil, deviceID, "send_keys", "run"); decision.Action == ActionAllow {
			t.Fatalf("legacy grant for %s authorized a call: %+v", deviceID, decision)
		}
		if _, ok := mustGrant(t, manager, deviceID); ok {
			t.Fatalf("legacy grant for %s is reported as effective", deviceID)
		}
	}
	if decision := manager.EvaluateResource(ctx, Config{Mode: ReadWrite}, Confirm(KindWriteFS, "write"), nil, "device-a", "write_file", "run", Resource{Action: "write_file", Path: "/var/log/app.log"}); decision.Action == ActionAllow {
		t.Fatalf("legacy rule authorized a call: %+v", decision)
	}
	if settings.values[DeviceGrantsSettingKey] != legacyGrants || settings.values[GrantRulesSettingKey] != legacyRules {
		t.Fatalf("evaluation rewrote legacy records: grants=%s rules=%s", settings.values[DeviceGrantsSettingKey], settings.values[GrantRulesSettingKey])
	}

	listed := mustGrantList(t, manager)
	if len(listed) != 2 {
		t.Fatalf("listed grants = %+v, want both legacy records visible", listed)
	}
	for _, grant := range listed {
		if !grant.Legacy {
			t.Fatalf("legacy grant listed without the legacy marker: %+v", grant)
		}
	}
	rules, err := manager.Rules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || !rules[0].Legacy || rules[0].ID != "rule-a" {
		t.Fatalf("listed rules = %+v, want the legacy rule visible", rules)
	}

	// An explicit re-grant replaces the legacy record; other legacy records stay verbatim.
	granted, err := manager.Grant(ctx, "device-a", []GrantKind{GrantKindTerminalWrite})
	if err != nil {
		t.Fatal(err)
	}
	if granted.Legacy {
		t.Fatalf("re-granted record is still legacy: %+v", granted)
	}
	if decision := manager.Evaluate(ctx, Config{Mode: ReadWrite}, Confirm(KindProcess, "write"), nil, "device-a", "send_keys", "run"); decision.Action != ActionAllow {
		t.Fatalf("re-granted device was not authorized: %+v", decision)
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal([]byte(settings.values[DeviceGrantsSettingKey]), &stored); err != nil {
		t.Fatal(err)
	}
	if string(stored["device-b"]) != `{"deviceId":"device-b","kinds":["terminal_write"],"updatedAt":1,"generation":"gen-b"}` {
		t.Fatalf("bystander legacy record was not preserved verbatim: %s", stored["device-b"])
	}

	// Revoking a legacy record removes it explicitly.
	if err := manager.Revoke(ctx, "device-b"); err != nil {
		t.Fatal(err)
	}
	if err := manager.RevokeRule(ctx, "rule-a"); err != nil {
		t.Fatal(err)
	}
	fresh, err := NewGrants(ctx, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed = mustGrantList(t, fresh)
	if len(listed) != 1 || listed[0].DeviceID != "device-a" || listed[0].Legacy {
		t.Fatalf("grants after revoke = %+v", listed)
	}
	if rules, err := fresh.Rules(ctx); err != nil || len(rules) != 0 {
		t.Fatalf("rules after revoke = %+v, %v", rules, err)
	}
}
