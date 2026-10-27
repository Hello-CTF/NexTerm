package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/terminal/staterule"
)

func requireStateRuleCode(t *testing.T, err error, code ipc.Code) {
	t.Helper()
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func stateRuleInput(pattern string) store.StateRuleInput {
	return store.StateRuleInput{Scope: store.StateRuleScopeGlobal, Region: staterule.RegionBottom, Pattern: pattern, State: "needs_me", Enabled: true}
}

func TestStateRuleCRUDAndOwnershipDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "state-rule-owner-a")
	ctxB := ipc.WithUserID(context.Background(), "state-rule-owner-b")

	created, err := db.StateRuleSave(ctxA, stateRuleInput(`Waiting for (you|review)`))
	if err != nil {
		t.Fatal(err)
	}
	if !ids.Valid(created.ID) || created.RegionLines != staterule.DefaultLines || created.Name != "Waiting for (you|review)" || created.UpdatedAt == 0 {
		t.Fatalf("created = %+v", created)
	}
	host := stateRuleInput(`esc to interrupt`)
	host.Scope, host.AssetID, host.State, host.Name = store.StateRuleScopeHost, ids.New(), "running", "agent thinking"
	hostRule, err := db.StateRuleSave(ctxA, host)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := db.StateRuleList(ctxB); err != nil || len(rows) != 0 {
		t.Fatalf("other owner sees %d rules, %v", len(rows), err)
	}

	// Invalid rules are rejected visibly and nothing is stored.
	for name, input := range map[string]store.StateRuleInput{
		"pattern":   stateRuleInput(`(unclosed`),
		"backtrack": stateRuleInput(`(a+)\1`),
		"scope":     {Scope: "everywhere", Region: staterule.RegionBottom, Pattern: "a", State: "idle"},
		"host":      {Scope: store.StateRuleScopeHost, Region: staterule.RegionBottom, Pattern: "a", State: "idle"},
		"state":     {Scope: store.StateRuleScopeGlobal, Region: staterule.RegionBottom, Pattern: "a", State: "exited"},
		"huge":      stateRuleInput(strings.Repeat("x", staterule.MaxPatternBytes+1)),
		"long name": {Scope: store.StateRuleScopeGlobal, Name: strings.Repeat("名", store.MaxStateRuleNameRunes+1), Region: staterule.RegionBottom, Pattern: "a", State: "idle"},
		"idle keys": {Scope: store.StateRuleScopeGlobal, Region: staterule.RegionBottom, Pattern: "a", State: "idle", Keys: "y"},
	} {
		if _, err := db.StateRuleSave(ctxA, input); err == nil {
			t.Fatalf("%s: invalid rule saved", name)
		} else {
			requireStateRuleCode(t, err, ipc.CodeBadParam)
		}
	}

	update := stateRuleInput(`Waiting for review`)
	update.ID, update.Enabled = created.ID, false
	updated, err := db.StateRuleSave(ctxA, update)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Pattern != "Waiting for review" || updated.Enabled || updated.UpdatedAt <= created.UpdatedAt || updated.CreatedAt != created.CreatedAt {
		t.Fatalf("updated = %+v (was %+v)", updated, created)
	}
	if _, err := db.StateRuleSave(ctxB, update); err == nil {
		t.Fatal("another owner updated the rule")
	} else {
		requireStateRuleCode(t, err, ipc.CodeNotFound)
	}
	if err := db.StateRuleDelete(ctxB, hostRule.ID); err == nil {
		t.Fatal("another owner deleted the rule")
	}

	// Only enabled rules for the host apply, host rules first.
	global := stateRuleInput(`\? \[y/N\]`)
	global.State = "idle"
	globalRule, err := db.StateRuleSave(ctxA, global)
	if err != nil {
		t.Fatal(err)
	}
	rules, skipped, err := db.StateRulesFor(ctxA, host.AssetID)
	if err != nil || len(skipped) != 0 {
		t.Fatal(err, skipped)
	}
	if len(rules) != 2 || rules[0].ID != hostRule.ID || rules[1].ID != globalRule.ID {
		t.Fatalf("rules for the host = %+v", rules)
	}
	if rules, _, err = db.StateRulesFor(ctxA, ids.New()); err != nil || len(rules) != 1 || rules[0].ID != globalRule.ID {
		t.Fatalf("rules for another host = %+v, %v", rules, err)
	}
	if _, err := staterule.NewSet(rules); err != nil {
		t.Fatalf("rules for a host do not form a valid set: %v", err)
	}

	if err := db.StateRuleDelete(ctxA, hostRule.ID); err != nil {
		t.Fatal(err)
	}
	requireStateRuleCode(t, db.StateRuleDelete(ctxA, hostRule.ID), ipc.CodeNotFound)
	if rows, err := db.StateRuleList(ctxA); err != nil || len(rows) != 2 {
		t.Fatalf("after delete: %d rules, %v", len(rows), err)
	}
}

func TestStateRuleLimitsDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := ipc.WithUserID(context.Background(), "state-rule-limits")
	// Patterns that each fit but together exceed the set budget.
	heavy := `\w{1,100}z`
	saved := 0
	var limitErr error
	for i := 0; i < staterule.MaxRules; i++ {
		if _, err := db.StateRuleSave(ctx, stateRuleInput(heavy)); err != nil {
			limitErr = err
			break
		}
		saved++
	}
	if limitErr == nil || saved == 0 {
		t.Fatalf("saved %d heavy rules without hitting the budget", saved)
	}
	requireStateRuleCode(t, limitErr, ipc.CodeBadParam)
	rules, _, err := db.StateRulesFor(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staterule.NewSet(rules); err != nil {
		t.Fatalf("stored rules exceed the helper limits: %v", err)
	}

	// The rule count is bounded too.
	other := ipc.WithUserID(context.Background(), "state-rule-count")
	for i := 0; i < staterule.MaxRules; i++ {
		if _, err := db.StateRuleSave(other, stateRuleInput("a")); err != nil {
			t.Fatalf("rule %d: %v", i+1, err)
		}
	}
	_, err = db.StateRuleSave(other, stateRuleInput("a"))
	requireStateRuleCode(t, err, ipc.CodeBadParam)
	if rows, _ := db.StateRuleList(other); len(rows) != staterule.MaxRules {
		t.Fatalf("stored %d rules", len(rows))
	}
}

func TestStateRuleImportRoundTripDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctxA := ipc.WithUserID(context.Background(), "state-rule-import-a")
	ctxB := ipc.WithUserID(context.Background(), "state-rule-import-b")
	first, err := db.StateRuleSave(ctxA, stateRuleInput(`Waiting`))
	if err != nil {
		t.Fatal(err)
	}
	second := stateRuleInput(`✳`)
	second.Region, second.Keys = staterule.RegionTitle, "\r"
	if _, err := db.StateRuleSave(ctxA, second); err != nil {
		t.Fatal(err)
	}
	exported, err := db.StateRuleList(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]store.StateRuleInput, len(exported))
	for i, row := range exported {
		inputs[i] = store.StateRuleInput{
			ID: row.ID, Name: row.Name, Scope: row.Scope, AssetID: row.AssetID, Region: row.Region,
			Lines: row.RegionLines, Pattern: row.Pattern, State: row.TargetState, Keys: row.KeySequence,
			Enabled: row.Enabled, UpdatedAt: row.UpdatedAt,
		}
	}

	// Re-importing into the same owner changes nothing.
	result, err := db.StateRuleImport(ctxA, inputs)
	if err != nil || result != (store.StateRuleImportResult{Unchanged: 2}) {
		t.Fatalf("re-import = %+v, %v", result, err)
	}
	// Another owner gets copies under new IDs; the originals stay.
	result, err = db.StateRuleImport(ctxB, inputs)
	if err != nil || result != (store.StateRuleImportResult{Created: 2}) {
		t.Fatalf("import into another owner = %+v, %v", result, err)
	}
	copies, err := db.StateRuleList(ctxB)
	if err != nil || len(copies) != 2 {
		t.Fatalf("copies = %d, %v", len(copies), err)
	}
	for _, row := range copies {
		if row.ID == first.ID || row.ID == exported[1].ID {
			t.Fatalf("copy reused the ID of another owner's rule: %+v", row)
		}
	}
	titleCopies := 0
	for _, row := range copies {
		if row.Region == staterule.RegionTitle && row.KeySequence == "\r" && row.Pattern == "✳" {
			titleCopies++
		}
	}
	if titleCopies != 1 {
		t.Fatalf("copies lost fields: %+v", copies)
	}

	// A newer edit replaces the rule; an invalid one rejects the whole batch.
	newer := inputs[0]
	newer.Pattern, newer.UpdatedAt = "Waiting for input", inputs[0].UpdatedAt+1
	fresh := stateRuleInput("fresh")
	result, err = db.StateRuleImport(ctxA, []store.StateRuleInput{newer, fresh})
	if err != nil || result != (store.StateRuleImportResult{Created: 1, Updated: 1}) {
		t.Fatalf("newer import = %+v, %v", result, err)
	}
	invalid := stateRuleInput("(bad")
	_, err = db.StateRuleImport(ctxA, []store.StateRuleInput{stateRuleInput("ok"), invalid})
	requireStateRuleCode(t, err, ipc.CodeBadParam)
	if !strings.Contains(err.Error(), "第 2 条") {
		t.Fatalf("import error does not name the rule: %v", err)
	}
	rows, err := db.StateRuleList(ctxA)
	if err != nil || len(rows) != 3 {
		t.Fatalf("after the rejected import: %d rules, %v", len(rows), err)
	}
	for _, row := range rows {
		if row.ID == first.ID && row.Pattern != "Waiting for input" {
			t.Fatalf("newer import did not land: %+v", row)
		}
	}
}
