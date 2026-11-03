package guard

import (
	"context"
	"testing"
	"time"
)

func TestDeviceGrantDoesNotCoverNonRememberableCommands(t *testing.T) {
	grants, _ := newTestGrants(t, nil)
	ctx := context.Background()
	if _, err := grants.Grant(ctx, "device-a", []GrantKind{GrantKindSessionExec}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Mode: ReadWrite}
	unknown := ClassifyCommand("bash /tmp/a.sh", nil)
	if got := grants.Evaluate(ctx, cfg, unknown, nil, "device-a", "exec_commands", "run-1"); got.Action != ActionAsk {
		t.Fatalf("unknown command under device grant = %+v", got)
	}
	restart := ClassifyCommand("systemctl restart nginx", nil)
	if got := grants.Evaluate(ctx, cfg, restart, nil, "device-a", "exec_commands", "run-1"); got.Action != ActionAllow {
		t.Fatalf("rememberable command under device grant = %+v", got)
	}
}

func TestCommandRuleDecisionReasonIsGrantRule(t *testing.T) {
	grants, _ := newTestGrants(t, nil)
	ctx := context.Background()
	if _, err := grants.AddRule(ctx, "device-a", "exec_commands", "", RuleScope{Kinds: []Kind{KindService}}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	restart := ClassifyCommand("systemctl restart nginx", nil)
	got := grants.Evaluate(ctx, Config{Mode: ReadWrite}, restart, nil, "device-a", "exec_commands", "run-1")
	if got.Action != ActionAllow || got.Reason != "授权规则" {
		t.Fatalf("command rule decision = %+v", got)
	}
	if _, err := grants.AddRule(ctx, "device-a", "write_file", "/var/log/**", RuleScope{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	write := Confirm(KindWriteFS, "write")
	got = grants.EvaluateResource(ctx, Config{Mode: ReadWrite}, write, nil, "device-a", "write_file", "run-1", Resource{Action: "write_file", Path: "/var/log/x"})
	if got.Action != ActionAllow || got.Reason != "路径规则授权" {
		t.Fatalf("path rule decision = %+v", got)
	}
}

func TestAddRuleTreatsUnattendedAsDistinctRule(t *testing.T) {
	grants, _ := newTestGrants(t, nil)
	ctx := context.Background()
	interactive, err := grants.AddRule(ctx, "device-a", "exec_commands", "", RuleScope{Kinds: []Kind{KindService}}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	unattended, err := grants.AddRule(ctx, "device-a", "exec_commands", "", RuleScope{Kinds: []Kind{KindService}, Unattended: true}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if interactive.ID == unattended.ID {
		t.Fatal("unattended variant reused the interactive rule ID")
	}
	again, err := grants.AddRule(ctx, "device-a", "exec_commands", "", RuleScope{Kinds: []Kind{KindService}, Unattended: true}, time.Time{})
	if err != nil || again.ID != unattended.ID {
		t.Fatalf("identical scope not deduplicated: %+v, %v", again, err)
	}
	if got := RuleSyncObjectID(plainPathRule()); got != "rule/p" {
		t.Fatalf("path rule sync id = %s", got)
	}
	if got := RuleSyncObjectID(interactive); got != "scoped-rule/"+interactive.ID {
		t.Fatalf("scoped rule sync id = %s", got)
	}
}

func plainPathRule() GrantRule {
	return GrantRule{ID: "p", DeviceID: "d", Action: "write_file", Path: "/a/**"}
}
