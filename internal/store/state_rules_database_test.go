package store_test

import (
	"context"
	"errors"
	"fmt"
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

func createStateRuleAsset(t *testing.T, db *store.Store, ctx context.Context, name string) store.AssetRow {
	t.Helper()
	asset, err := db.AssetCreate(ctx, store.AssetInput{Kind: "ssh", Name: name, OptionsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	return asset
}

func stateRuleByID(t *testing.T, rows []store.StateRuleRow, id string) store.StateRuleRow {
	t.Helper()
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("rule %s not found in %+v", id, rows)
	return store.StateRuleRow{}
}

func stateRuleIDs(rows []store.StateRuleRow) []string {
	result := make([]string, len(rows))
	for i, row := range rows {
		result[i] = row.ID
	}
	return result
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
	assetA := createStateRuleAsset(t, db, ctxA, "a-host")
	assetB := createStateRuleAsset(t, db, ctxB, "b-host")
	host := stateRuleInput(`esc to interrupt`)
	host.Scope, host.AssetID, host.State, host.Name = store.StateRuleScopeHost, assetA.ID, "running", "agent thinking"
	hostRule, err := db.StateRuleSave(ctxA, host)
	if err != nil {
		t.Fatal(err)
	}
	if hostRule.Position <= created.Position {
		t.Fatalf("positions %d then %d", created.Position, hostRule.Position)
	}
	// A host-scoped rule must name one of the owner's own assets.
	foreign := host
	foreign.AssetID = assetB.ID
	_, err = db.StateRuleSave(ctxA, foreign)
	requireStateRuleCode(t, err, ipc.CodeBadParam)
	foreign.AssetID = ids.New()
	_, err = db.StateRuleSave(ctxA, foreign)
	requireStateRuleCode(t, err, ipc.CodeBadParam)
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

	// The list keeps the order of the saves; an edit keeps the position.
	rows, err := db.StateRuleList(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stateRuleIDs(rows), []string{created.ID, hostRule.ID, globalRule.ID}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("list order = %v, want %v", got, want)
	}
	before, err := db.StateRuleFingerprint(ctxA)
	if err != nil {
		t.Fatal(err)
	}

	if err := db.StateRuleDelete(ctxA, hostRule.ID); err != nil {
		t.Fatal(err)
	}
	requireStateRuleCode(t, db.StateRuleDelete(ctxA, hostRule.ID), ipc.CodeNotFound)
	if rows, err := db.StateRuleList(ctxA); err != nil || len(rows) != 2 {
		t.Fatalf("after delete: %d rules, %v", len(rows), err)
	}
	after, err := db.StateRuleFingerprint(ctxA)
	if err != nil || after == before || after.Count != 2 {
		t.Fatalf("fingerprint %+v -> %+v, %v", before, after, err)
	}
	if empty, err := db.StateRuleFingerprint(ipc.WithUserID(context.Background(), "state-rule-nobody")); err != nil || empty != (store.StateRuleFingerprint{}) {
		t.Fatalf("empty fingerprint = %+v, %v", empty, err)
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

func stateRuleInputsFromRows(rows []store.StateRuleRow) []store.StateRuleInput {
	inputs := make([]store.StateRuleInput, len(rows))
	for i, row := range rows {
		inputs[i] = store.StateRuleInput{
			ID: row.ID, Name: row.Name, Scope: row.Scope, AssetID: row.AssetID, Region: row.Region,
			Lines: row.RegionLines, Pattern: row.Pattern, State: row.TargetState, Keys: row.KeySequence,
			Enabled: row.Enabled, UpdatedAt: row.UpdatedAt,
		}
	}
	return inputs
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
	secondRule, err := db.StateRuleSave(ctxA, second)
	if err != nil {
		t.Fatal(err)
	}
	exported, err := db.StateRuleList(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	inputs := stateRuleInputsFromRows(exported)

	// Re-importing into the same owner changes nothing.
	result, err := db.StateRuleImport(ctxA, inputs)
	if err != nil || result != (store.StateRuleImportResult{Unchanged: 2}) {
		t.Fatalf("re-import = %+v, %v", result, err)
	}

	// Another owner gets copies under derived IDs, in the order of the
	// file; importing the same export again finds the copies.
	result, err = db.StateRuleImport(ctxB, inputs)
	if err != nil || result != (store.StateRuleImportResult{Created: 2}) {
		t.Fatalf("import into another owner = %+v, %v", result, err)
	}
	result, err = db.StateRuleImport(ctxB, inputs)
	if err != nil || result != (store.StateRuleImportResult{Unchanged: 2}) {
		t.Fatalf("repeated import into another owner = %+v, %v", result, err)
	}
	copies, err := db.StateRuleList(ctxB)
	if err != nil || len(copies) != 2 {
		t.Fatalf("copies = %d, %v", len(copies), err)
	}
	if copies[0].Pattern != "Waiting" || copies[1].Pattern != "✳" || copies[1].KeySequence != "\r" || copies[1].Region != staterule.RegionTitle {
		t.Fatalf("copies out of file order or missing fields: %+v", copies)
	}
	for _, row := range copies {
		if row.ID == first.ID || row.ID == secondRule.ID || !ids.Valid(row.ID) {
			t.Fatalf("copy has the ID of another owner's rule or an invalid one: %+v", row)
		}
	}
	if original := stateRuleByID(t, mustStateRuleList(t, db, ctxA), first.ID); original.Pattern != "Waiting" {
		t.Fatalf("original changed: %+v", original)
	}

	// The file order decides the positions of new rules.
	reversed := []store.StateRuleInput{stateRuleInput("third"), stateRuleInput("fourth")}
	result, err = db.StateRuleImport(ctxA, reversed)
	if err != nil || result.Created != 2 {
		t.Fatalf("import of new rules = %+v, %v", result, err)
	}
	rows := mustStateRuleList(t, db, ctxA)
	patterns := make([]string, len(rows))
	for i, row := range rows {
		patterns[i] = row.Pattern
	}
	if got := strings.Join(patterns, ","); got != "Waiting,✳,third,fourth" {
		t.Fatalf("order after import = %s", got)
	}

	// A newer edit replaces the rule; an invalid one rejects the whole batch.
	newer := inputs[0]
	newer.Pattern, newer.UpdatedAt = "Waiting for input", inputs[0].UpdatedAt+1
	result, err = db.StateRuleImport(ctxA, []store.StateRuleInput{newer})
	if err != nil || result != (store.StateRuleImportResult{Updated: 1}) {
		t.Fatalf("newer import = %+v, %v", result, err)
	}
	invalid := stateRuleInput("(bad")
	_, err = db.StateRuleImport(ctxA, []store.StateRuleInput{stateRuleInput("ok"), invalid})
	requireStateRuleCode(t, err, ipc.CodeBadParam)
	if !strings.Contains(err.Error(), "第 2 条") {
		t.Fatalf("import error does not name the rule: %v", err)
	}
	// A host rule for an asset the owner does not have rejects the batch.
	host := stateRuleInput("host")
	host.Scope, host.AssetID = store.StateRuleScopeHost, ids.New()
	_, err = db.StateRuleImport(ctxA, []store.StateRuleInput{host})
	requireStateRuleCode(t, err, ipc.CodeBadParam)
	rows = mustStateRuleList(t, db, ctxA)
	if len(rows) != 4 {
		t.Fatalf("after the rejected imports: %d rules", len(rows))
	}
	if got := stateRuleByID(t, rows, first.ID); got.Pattern != "Waiting for input" || got.Position != first.Position {
		t.Fatalf("newer import did not land in place: %+v", got)
	}
}

func mustStateRuleList(t *testing.T, db *store.Store, ctx context.Context) []store.StateRuleRow {
	t.Helper()
	rows, err := db.StateRuleList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestStateRuleConcurrentCreatesGetDistinctPositionsDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := ipc.WithUserID(context.Background(), "state-rule-concurrent")
	const writers = 6
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func() {
			_, err := db.StateRuleSave(ctx, stateRuleInput(fmt.Sprintf("rule %d", i)))
			errs <- err
		}()
	}
	for i := 0; i < writers; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	rows := mustStateRuleList(t, db, ctx)
	seen := make(map[int64]bool, len(rows))
	for _, row := range rows {
		if seen[row.Position] {
			t.Fatalf("position %d used twice: %+v", row.Position, rows)
		}
		seen[row.Position] = true
	}
	if len(rows) != writers {
		t.Fatalf("stored %d rules", len(rows))
	}
}

func TestStateRuleReorderDatabase(t *testing.T) {
	db := dbtest.NewFixture(t).OpenStore(t)
	ctx := ipc.WithUserID(context.Background(), "state-rule-reorder")
	other := ipc.WithUserID(context.Background(), "state-rule-reorder-other")
	var created []store.StateRuleRow
	for _, pattern := range []string{"first", "second", "third"} {
		row, err := db.StateRuleSave(ctx, stateRuleInput(pattern))
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, row)
	}
	if _, err := db.StateRuleSave(other, stateRuleInput("foreign")); err != nil {
		t.Fatal(err)
	}
	before, err := db.StateRuleFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}

	order := []string{created[2].ID, created[0].ID, created[1].ID}
	rows, err := db.StateRuleReorder(ctx, order)
	if err != nil {
		t.Fatal(err)
	}
	if got := stateRuleIDs(rows); strings.Join(got, ",") != strings.Join(order, ",") {
		t.Fatalf("reordered = %v, want %v", got, order)
	}
	if got := stateRuleIDs(mustStateRuleList(t, db, ctx)); strings.Join(got, ",") != strings.Join(order, ",") {
		t.Fatalf("listed after reorder = %v, want %v", got, order)
	}
	rules, _, err := db.StateRulesFor(ctx, "")
	if err != nil || len(rules) != 3 || rules[0].ID != created[2].ID {
		t.Fatalf("rules for a helper after reorder = %+v, %v", rules, err)
	}
	after, err := db.StateRuleFingerprint(ctx)
	if err != nil || after == before {
		t.Fatalf("fingerprint %+v -> %+v, %v", before, after, err)
	}
	if foreign := mustStateRuleList(t, db, other); len(foreign) != 1 || foreign[0].Pattern != "foreign" {
		t.Fatalf("other owner's rules = %+v", foreign)
	}

	// The order must name exactly the owner's rules.
	for name, rejected := range map[string][]string{
		"missing":   {created[0].ID, created[1].ID},
		"duplicate": {created[0].ID, created[0].ID, created[1].ID},
		"unknown":   {created[0].ID, created[1].ID, ids.New()},
	} {
		_, err := db.StateRuleReorder(ctx, rejected)
		requireStateRuleCode(t, err, ipc.CodeBadParam)
		if got := stateRuleIDs(mustStateRuleList(t, db, ctx)); strings.Join(got, ",") != strings.Join(order, ",") {
			t.Fatalf("%s: a rejected reorder changed the order to %v", name, got)
		}
	}

	// A rule created after a reorder still goes last.
	last, err := db.StateRuleSave(ctx, stateRuleInput("fourth"))
	if err != nil {
		t.Fatal(err)
	}
	if got := stateRuleIDs(mustStateRuleList(t, db, ctx)); got[len(got)-1] != last.ID {
		t.Fatalf("new rule not last: %v", got)
	}
}

func TestValidateStateRuleInputChecksOneRule(t *testing.T) {
	valid, err := store.ValidateStateRuleInput(stateRuleInput(`Continue\? \[y/N\]`))
	if err != nil {
		t.Fatal(err)
	}
	if !ids.Valid(valid.ID) || valid.Lines != staterule.DefaultLines || valid.Name != `Continue\? \[y/N\]` {
		t.Fatalf("normalized = %+v", valid)
	}
	for name, input := range map[string]store.StateRuleInput{
		"pattern":     stateRuleInput(`(unclosed`),
		"empty match": stateRuleInput(`x*`),
		"scope":       {Scope: "everywhere", Region: staterule.RegionBottom, Pattern: "a", State: "idle"},
		"state":       {Scope: store.StateRuleScopeGlobal, Region: staterule.RegionBottom, Pattern: "a", State: "exited"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.ValidateStateRuleInput(input)
			requireStateRuleCode(t, err, ipc.CodeBadParam)
		})
	}
}
