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

type generationFaultSettings struct {
	*memorySettings
	failKey  string
	failures int
}

func (s *generationFaultSettings) SettingSet(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == s.failKey && s.failures > 0 {
		s.failures--
		return errors.New("injected generation write failure")
	}
	s.values[key] = value
	return nil
}

func (s *generationFaultSettings) SettingCompareAndSwap(_ context.Context, key, expected, value string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == s.failKey && s.failures > 0 {
		s.failures--
		return false, errors.New("injected generation write failure")
	}
	current, ok := s.values[key]
	if !ok || current != expected {
		return false, nil
	}
	s.values[key] = value
	return true, nil
}

func newLegacyGenerationSettings(t *testing.T) *generationFaultSettings {
	t.Helper()
	settings := &generationFaultSettings{memorySettings: &memorySettings{values: make(map[string]string)}}
	grants, err := json.Marshal(map[string]DeviceGrant{
		"device-a": {DeviceID: "device-a", Kinds: []GrantKind{GrantKindTerminalWrite}, Permanent: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := json.Marshal([]GrantRule{{ID: "rule-a", DeviceID: "device-a", Action: "write_file", Path: "/var/log/**"}})
	if err != nil {
		t.Fatal(err)
	}
	settings.values[DeviceGrantsSettingKey] = string(grants)
	settings.values[GrantRulesSettingKey] = string(rules)
	return settings
}

func evaluateGenerationRule(manager *Grants) Decision {
	return manager.EvaluateResource(context.Background(), Config{Mode: ReadWrite}, Confirm(KindWriteFS, "write"), nil, "device-a", "write_file", "run-generation-retry", Resource{Action: "write_file", Path: "/var/log/app.log"})
}

func persistedAuthorizationGenerations(t *testing.T, settings *generationFaultSettings) (string, string) {
	t.Helper()
	var grants map[string]deviceGrantRecord
	if err := json.Unmarshal([]byte(settings.values[DeviceGrantsSettingKey]), &grants); err != nil {
		t.Fatal(err)
	}
	var rules []grantRuleRecord
	if err := json.Unmarshal([]byte(settings.values[GrantRulesSettingKey]), &rules); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("persisted rules = %+v", rules)
	}
	return grants["device-a"].Generation, rules[0].Generation
}

func TestAuthorizationGenerationPartialPersistenceFailureRetries(t *testing.T) {
	for _, test := range []struct {
		name    string
		failKey string
	}{
		{name: "rule_succeeds_grant_fails", failKey: DeviceGrantsSettingKey},
		{name: "grant_succeeds_rule_fails", failKey: GrantRulesSettingKey},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := newLegacyGenerationSettings(t)
			settings.failKey = test.failKey
			settings.failures = 1
			manager, err := NewGrants(context.Background(), settings, nil)
			if err != nil {
				t.Fatal(err)
			}
			if decision := evaluateGenerationRule(manager); decision.Action != ActionDeny {
				t.Fatalf("failed generation write decision = %+v", decision)
			}

			decision := evaluateGenerationRule(manager)
			if decision.Action != ActionAllow || decision.Authorization == nil {
				t.Fatalf("generation write retry = %+v", decision)
			}
			fresh, err := NewGrants(context.Background(), settings, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := fresh.RevalidateAuthorization(context.Background(), decision.Authorization); err != nil {
				t.Fatalf("retry used a generation absent from persistent storage: %v", err)
			}
			grantGeneration, ruleGeneration := persistedAuthorizationGenerations(t, settings)
			if grantGeneration == "" || ruleGeneration != decision.Authorization.Generation {
				t.Fatalf("persisted generations do not match final decision: grant=%q rule=%q decision=%+v", grantGeneration, ruleGeneration, decision.Authorization)
			}

			grantDecision := manager.Evaluate(context.Background(), Config{Mode: ReadWrite}, Confirm(KindProcess, "write"), nil, "device-a", "send_keys", "run-generation-retry")
			if grantDecision.Action != ActionAllow || grantDecision.Authorization == nil {
				t.Fatalf("grant retry = %+v", grantDecision)
			}
			if err := fresh.RevalidateAuthorization(context.Background(), grantDecision.Authorization); err != nil {
				t.Fatalf("grant retry used an unpersisted generation: %v", err)
			}
			if grantGeneration != grantDecision.Authorization.Generation {
				t.Fatalf("persisted grant generation %q does not match decision %+v", grantGeneration, grantDecision.Authorization)
			}
		})
	}
}

func TestAuthorizationGenerationMigrationConflictDoesNotReviveRecords(t *testing.T) {
	evaluateGrant := func(manager *Grants) Decision {
		return manager.Evaluate(context.Background(), Config{Mode: ReadWrite}, Confirm(KindProcess, "write"), nil, "device-a", "send_keys", "run-generation-conflict")
	}
	for _, test := range []struct {
		name        string
		key         string
		replacement string
		survivingID string
		evaluate    func(*Grants) Decision
	}{
		{name: "grant_revoke", key: DeviceGrantsSettingKey, replacement: `{}`, evaluate: evaluateGrant},
		{name: "grant_rewrite", key: DeviceGrantsSettingKey, replacement: `{"device-b":{"deviceId":"device-b","kinds":["terminal_write"],"permanent":true}}`, survivingID: "device-b", evaluate: evaluateGrant},
		{name: "rule_revoke", key: GrantRulesSettingKey, replacement: `[]`, evaluate: evaluateGenerationRule},
		{name: "rule_rewrite", key: GrantRulesSettingKey, replacement: `[{"id":"rule-b","deviceId":"device-a","action":"write_file","path":"/tmp/**"}]`, survivingID: "rule-b", evaluate: evaluateGenerationRule},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := newLegacyGenerationSettings(t)
			manager, err := NewGrants(context.Background(), settings, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := settings.SettingSet(context.Background(), test.key, test.replacement); err != nil {
				t.Fatal(err)
			}
			if decision := test.evaluate(manager); decision.Action == ActionAllow {
				t.Fatalf("stale entry was admitted after storage changed: %+v", decision)
			}
			if err := manager.Reload(context.Background()); err != nil {
				t.Fatal(err)
			}
			raw, found, err := settings.SettingGet(context.Background(), test.key)
			if err != nil || !found {
				t.Fatalf("setting after conflict = %t, %v", found, err)
			}
			switch test.key {
			case DeviceGrantsSettingKey:
				var records map[string]deviceGrantRecord
				if err := json.Unmarshal([]byte(raw), &records); err != nil {
					t.Fatal(err)
				}
				if _, ok := records["device-a"]; ok {
					t.Fatalf("stale grant was revived: %s", raw)
				}
				if test.survivingID == "" && len(records) != 0 {
					t.Fatalf("revoked grants were replaced: %s", raw)
				}
				if test.survivingID != "" {
					if len(records) != 1 || records[test.survivingID].DeviceID != test.survivingID {
						t.Fatalf("rewritten grants were overwritten: %s", raw)
					}
				}
			case GrantRulesSettingKey:
				var records []grantRuleRecord
				if err := json.Unmarshal([]byte(raw), &records); err != nil {
					t.Fatal(err)
				}
				for _, record := range records {
					if record.ID == "rule-a" {
						t.Fatalf("stale rule was revived: %s", raw)
					}
				}
				if test.survivingID == "" && len(records) != 0 {
					t.Fatalf("revoked rules were replaced: %s", raw)
				}
				if test.survivingID != "" {
					if len(records) != 1 || records[0].ID != test.survivingID {
						t.Fatalf("rewritten rules were overwritten: %s", raw)
					}
				}
			}
			fresh, err := NewGrants(context.Background(), settings, nil)
			if err != nil {
				t.Fatal(err)
			}
			if decision := test.evaluate(fresh); decision.Action == ActionAllow {
				t.Fatalf("fresh manager admitted the replaced authorization: %+v", decision)
			}
		})
	}
}
