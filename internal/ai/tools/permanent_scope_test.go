package tools

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

func TestPersistPermanentGrantRefusesWithoutScope(t *testing.T) {
	grants := newChatGrantManager(t, &chatGrantRecorder{})
	h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, &chatGrantRecorder{})
	state := InteractionState{
		Kind: "confirm", MemoryKinds: []guard.Kind{guard.KindService},
		Info:     Interaction{Risk: "needs_confirm"},
		Resource: guard.Resource{Action: "exec_commands"},
	}
	if err := h.execution.persistPermanentGrant(context.Background(), state); err == nil {
		t.Fatal("permanent grant without a scope was accepted")
	}
	if rules, err := grants.Rules(context.Background()); err != nil || len(rules) != 0 {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
}

func TestPersistPermanentGrantCarriesRulingScope(t *testing.T) {
	grants := newChatGrantManager(t, &chatGrantRecorder{})
	h := newChatGrantHarness(guard.Config{Mode: guard.ReadWrite}, "asset-a", grants, &chatGrantRecorder{})
	ruling := guard.ConfirmProgram("git", "program")
	state := InteractionState{
		Kind: "confirm", MemoryKinds: ruling.ApprovalKinds(), MemoryPrograms: ruling.Programs,
		Info:     Interaction{Risk: "needs_confirm", PermanentScope: guard.PermanentScope(ruling, guard.Resource{Action: "exec_commands"})},
		Resource: guard.Resource{Action: "exec_commands"},
	}
	if state.Info.PermanentScope == "" {
		t.Fatal("rememberable ruling has no permanent scope")
	}
	if err := h.execution.persistPermanentGrant(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	rules, err := grants.Rules(context.Background())
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
	rule := rules[0]
	if rule.Legacy() || rule.Unattended || len(rule.Kinds) != 1 || rule.Kinds[0] != guard.KindProgram || len(rule.Programs) != 1 || rule.Programs[0] != "git" {
		t.Fatalf("rule = %+v", rule)
	}
	if !rule.Covers("exec_commands", "", ruling) || rule.Covers("exec_commands", "", guard.Confirm(guard.KindService, "x")) {
		t.Fatal("rule coverage does not follow the confirmed ruling")
	}
}

func TestPermanentScopeNotOfferedWhenNotRememberable(t *testing.T) {
	for _, ruling := range []guard.Ruling{guard.Dangerous("d"), guard.Indeterminate("u"), guard.Allow()} {
		if scope := guard.PermanentScope(ruling, guard.Resource{Action: "exec_commands"}); scope != "" {
			t.Fatalf("scope %q offered for %+v", scope, ruling)
		}
	}
	if guard.PermanentScope(guard.Confirm(guard.KindWriteFS, "w"), guard.Resource{Action: "write_file", Path: "/var/log/a.log"}) == "" {
		t.Fatal("write_file scope missing")
	}
}
