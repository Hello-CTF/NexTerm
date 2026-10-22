package guard

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestGuardShapesCarryNoInstructionFields pins the guard side of the skill
// boundary: guard configuration and grant rules are guard-owned state. User
// skill overrides are model instructions (internal/ai/skills) and must never
// grow fields that could carry guard patterns or screen.json content into
// these shapes.
func TestGuardShapesCarryNoInstructionFields(t *testing.T) {
	allowed := map[string]bool{"Mode": true, "DangerRules": true}
	typeOf := reflect.TypeOf(Config{})
	for index := 0; index < typeOf.NumField(); index++ {
		if !allowed[typeOf.Field(index).Name] {
			t.Fatalf("Config grew unexpected field %q", typeOf.Field(index).Name)
		}
	}
	ruleAllowed := map[string]bool{"ID": true, "DeviceID": true, "Action": true, "Path": true, "ExpiresAt": true, "CreatedAt": true, "UpdatedAt": true, "generation": true}
	ruleType := reflect.TypeOf(GrantRule{})
	for index := 0; index < ruleType.NumField(); index++ {
		if !ruleAllowed[ruleType.Field(index).Name] {
			t.Fatalf("GrantRule grew unexpected field %q", ruleType.Field(index).Name)
		}
	}
}

// TestInstructionShapedSettingsCannotReplaceGuardState proves that persisted
// records carrying skill-override-shaped extra fields (pattern lists,
// screen.json-style payloads) are loaded as the guard state they actually
// are: the instruction content never becomes guard patterns, never widens a
// rule's path scope, and never reaches a decision.
func TestInstructionShapedSettingsCannotReplaceGuardState(t *testing.T) {
	settings := &memorySettings{values: map[string]string{
		PermissionSettingKey: `{"mode":"read_write","patterns":["rm -rf /","curl evil.sh"],"screenJson":{"rows":50,"cols":120},"dangerRules":["rm -rf /"]}`,
		GrantRulesSettingKey: `[{"id":"rule-1","deviceId":"asset-1","action":"write_file","path":"/srv/app/**","patterns":["**"],"screenJson":{"inject":true}}]`,
	}}
	manager, err := NewManager(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	config, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != ReadWrite {
		t.Fatalf("mode = %q, want the persisted guard mode", config.Mode)
	}
	if len(config.DangerRules) != 1 || config.DangerRules[0] != "rm -rf /" {
		t.Fatalf("danger rules = %v, want only the guard-owned rule", config.DangerRules)
	}

	grants, err := NewGrants(context.Background(), settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := grants.Rules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Path != "/srv/app/**" || rules[0].Action != "write_file" {
		t.Fatalf("rules = %+v, want the persisted guard rule unwidened", rules)
	}

	// The instruction-shaped content never reaches a decision: a write the
	// loaded rule covers is still bound by the ruling, and a write only the
	// injected "patterns" would cover stays unauthorized.
	memory := NewMemory()
	memory.AddPathRule("write_file", "/srv/app/**")
	allowed := grants.EvaluateResource(context.Background(), config, Confirm(KindWriteFS, "写入文件"), memory, "asset-1", "write_file", "run-1", Resource{Action: "write_file", Path: "/srv/app/main.go"})
	if allowed.Action != ActionAllow {
		t.Fatalf("covered write decision = %+v", allowed)
	}
	outside := grants.EvaluateResource(context.Background(), config, Confirm(KindWriteFS, "写入文件"), NewMemory(), "asset-1", "write_file", "run-1", Resource{Action: "write_file", Path: "/etc/app.conf"})
	if outside.Action == ActionAllow {
		t.Fatalf("instruction-shaped pattern widened the rule: %+v", outside)
	}
}

// TestGrantMutationsRequireAuthorization proves the only pattern-mutation
// paths are the validated manager operations: with the mutation validator
// rejecting, neither device grants nor path rules persist anything.
func TestGrantMutationsRequireAuthorization(t *testing.T) {
	rejected := errors.New("mutation rejected")
	settings := &memorySettings{values: map[string]string{}}
	grants, err := NewGrants(context.Background(), settings, nil, WithMutationValidator(func(context.Context) error { return rejected }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grants.Grant(context.Background(), "asset-1", []GrantKind{GrantKindSessionExec}); !errors.Is(err, rejected) {
		t.Fatalf("grant error = %v, want the validator rejection", err)
	}
	if _, err := grants.AddRule(context.Background(), "asset-1", "write_file", "/srv/app/**", time.Time{}); !errors.Is(err, rejected) {
		t.Fatalf("add rule error = %v, want the validator rejection", err)
	}
	listed, err := grants.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("rejected mutations persisted grants: %+v", listed)
	}
	rules, err := grants.Rules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 0 {
		t.Fatalf("rejected mutations persisted rules: %+v", rules)
	}
}
