package guard

import (
	"context"
	"errors"
	"testing"
	"time"
)

func matrixGrants(t *testing.T, authorization string) *Grants {
	t.Helper()
	grants, settings := newTestGrants(t, nil)
	ctx := context.Background()
	switch authorization {
	case "none":
	case "scoped":
		if _, err := grants.AddRule(ctx, "dev-a", "exec_commands", "", RuleScope{Kinds: []Kind{KindService}}, time.Time{}); err != nil {
			t.Fatal(err)
		}
	case "mismatch":
		if _, err := grants.AddRule(ctx, "dev-a", "exec_commands", "", RuleScope{Kinds: []Kind{KindPackage}}, time.Time{}); err != nil {
			t.Fatal(err)
		}
	case "unattended":
		if _, err := grants.AddRule(ctx, "dev-a", "exec_commands", "", RuleScope{Kinds: []Kind{KindService}, Unattended: true}, time.Time{}); err != nil {
			t.Fatal(err)
		}
	case "legacy":
		legacy := GrantRule{ID: "legacy-1", DeviceID: "dev-a", Action: "exec_commands", CreatedAt: 1, UpdatedAt: 1}.Renewed()
		set := GrantRuleSet{Current: []GrantRule{legacy}}
		encoded, err := set.Encode()
		if err != nil {
			t.Fatal(err)
		}
		settings.values[GrantRulesSettingKey] = encoded
		reloaded, err := NewGrants(ctx, settings, nil)
		if err != nil {
			t.Fatal(err)
		}
		return reloaded
	case "device_grant":
		if _, err := grants.Grant(ctx, "dev-a", []GrantKind{GrantKindSessionExec}); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown authorization %q", authorization)
	}
	return grants
}

func TestGrantDecisionMatrix(t *testing.T) {
	ruling := Confirm(KindService, "service change")
	for _, authorization := range []string{"none", "scoped", "mismatch", "legacy", "unattended", "device_grant"} {
		for _, mode := range []Mode{ReadOnly, ReadWrite, Silent, Unattended} {
			for _, production := range []bool{false, true} {
				want := ActionAsk
				switch mode {
				case ReadOnly:
					want = ActionDeny
				case Silent:
					want = ActionAllow
				case Unattended:
					want = ActionDeny
					if authorization == "unattended" && !production {
						want = ActionAllow
					}
				case ReadWrite:
					if !production && (authorization == "scoped" || authorization == "unattended" || authorization == "device_grant") {
						want = ActionAllow
					}
				}
				name := authorization + "/" + string(mode)
				if production {
					name += "/production"
				}
				t.Run(name, func(t *testing.T) {
					grants := matrixGrants(t, authorization)
					resource := Resource{Action: "exec_commands"}
					if production {
						resource.Environment = "production"
					}
					decision := grants.EvaluateResource(context.Background(), Config{Mode: mode}, ruling, nil, "dev-a", "exec_commands", "run-1", resource)
					if decision.Action != want {
						t.Fatalf("action = %q, want %q (decision %+v)", decision.Action, want, decision)
					}
					viaAuthorization := authorization == "scoped" || authorization == "unattended" || authorization == "device_grant"
					if want == ActionAllow && mode != Silent && viaAuthorization && decision.Authorization == nil {
						t.Fatal("allowed without authorization binding")
					}
				})
			}
		}
	}
}

func TestLegacyWholeDeviceRuleNeverAuthorizesUnknownCommand(t *testing.T) {
	grants := matrixGrants(t, "legacy")
	ctx := context.Background()
	rules, err := grants.Rules(ctx)
	if err != nil || len(rules) != 1 || !rules[0].Legacy() {
		t.Fatalf("legacy rule not kept: %+v %v", rules, err)
	}
	ruling := ClassifyCommand("bash /tmp/a.sh", nil)
	resource := Resource{Action: "exec_commands"}
	if d := grants.EvaluateResource(ctx, Config{Mode: ReadWrite}, ruling, nil, "dev-a", "exec_commands", "r", resource); d.Action != ActionAsk {
		t.Fatalf("read_write = %+v", d)
	}
	if d := grants.EvaluateResource(ctx, Config{Mode: ReadOnly}, ruling, nil, "dev-a", "exec_commands", "r", resource); d.Action != ActionDeny {
		t.Fatalf("read_only = %+v", d)
	}
}

func TestScopedRuleDoesNotCoverUnknownKind(t *testing.T) {
	grants := matrixGrants(t, "scoped")
	ruling := Confirm(KindUnknown, "unknown")
	d := grants.EvaluateResource(context.Background(), Config{Mode: ReadWrite}, ruling, nil, "dev-a", "exec_commands", "r", Resource{Action: "exec_commands"})
	if d.Action != ActionAsk {
		t.Fatalf("decision = %+v", d)
	}
}

func TestAddRuleScopeValidation(t *testing.T) {
	grants, _ := newTestGrants(t, nil)
	ctx := context.Background()
	cases := []struct {
		name    string
		action  string
		path    string
		scope   RuleScope
		wantErr error
	}{
		{"empty", "exec_commands", "", RuleScope{}, ErrRuleScopeRequired},
		{"unknown", "exec_commands", "", RuleScope{Kinds: []Kind{KindUnknown}}, ErrRuleScopeRequired},
		{"danger", "send_keys", "", RuleScope{Kinds: []Kind{KindService, KindDanger}}, ErrRuleScopeRequired},
		{"program without programs", "exec_commands", "", RuleScope{Kinds: []Kind{KindProgram}}, ErrRuleScopeRequired},
		{"programs without kind", "exec_commands", "", RuleScope{Kinds: []Kind{KindService}, Programs: []string{"git"}}, ErrRuleScopeRequired},
		{"path with kinds", "write_file", "/var/log/**", RuleScope{Kinds: []Kind{KindService}}, ErrRuleScopeUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := grants.AddRule(ctx, "dev-a", tc.action, tc.path, tc.scope, time.Time{}); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
	if rules, _ := grants.Rules(ctx); len(rules) != 0 {
		t.Fatalf("rejected scopes left rules: %+v", rules)
	}
}

func TestAddRuleDedupesByScope(t *testing.T) {
	grants, settings := newTestGrants(t, nil)
	ctx := context.Background()
	first, err := grants.AddRule(ctx, "dev-a", "exec_commands", "", RuleScope{Kinds: []Kind{KindService, KindProgram, KindService}, Programs: []string{"git", "bash", "git"}}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := grants.AddRule(ctx, "dev-a", "exec_commands", "", RuleScope{Kinds: []Kind{KindProgram, KindService}, Programs: []string{"bash", "git"}, Unattended: true}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || !second.Unattended {
		t.Fatalf("not merged: %+v %+v", first, second)
	}
	if _, err := grants.AddRule(ctx, "dev-a", "exec_commands", "", RuleScope{Kinds: []Kind{KindService}}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	rules, _ := grants.Rules(ctx)
	if len(rules) != 2 {
		t.Fatalf("rules = %+v", rules)
	}

	reloaded, err := NewGrants(ctx, settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	reRules, _ := reloaded.Rules(ctx)
	if len(reRules) != 2 || !reRules[0].Unattended || len(reRules[0].Kinds) != 2 || len(reRules[0].Programs) != 2 {
		t.Fatalf("reloaded = %+v", reRules)
	}
	ruling := ConfirmProgram("git", "program")
	if !reRules[0].Covers("exec_commands", "", ruling) {
		t.Fatal("reloaded rule does not cover program ruling")
	}
	if reRules[0].Covers("exec_commands", "", ConfirmProgram("rm2", "x")) {
		t.Fatal("rule covers unlisted program")
	}
}

func TestSessionMemoryNeverOverridesModeDecision(t *testing.T) {
	ruling := Confirm(KindWriteFS, "write file")
	memories := map[string]func() *Memory{
		"none": func() *Memory { return NewMemory() },
		"kind": func() *Memory {
			m := NewMemory()
			m.Remember(ruling)
			return m
		},
		"path": func() *Memory {
			m := NewMemory()
			m.AddPathRule("write_file", "/var/log/**")
			return m
		},
	}
	for name, build := range memories {
		for _, mode := range []Mode{ReadOnly, ReadWrite, Silent, Unattended} {
			for _, production := range []bool{false, true} {
				want := ActionAsk
				switch mode {
				case ReadOnly, Unattended:
					want = ActionDeny
				case Silent:
					want = ActionAllow
				case ReadWrite:
					if !production && name != "none" {
						want = ActionAllow
					}
				}
				label := name + "/" + string(mode)
				if production {
					label += "/production"
				}
				t.Run(label, func(t *testing.T) {
					resource := Resource{Action: "write_file", Path: "/var/log/app.log"}
					if production {
						resource.Environment = "production"
					}
					got := DecideResource(Config{Mode: mode}, ruling, build(), resource)
					if got.Action != want {
						t.Fatalf("action = %q, want %q (%+v)", got.Action, want, got)
					}
				})
			}
		}
	}
}
