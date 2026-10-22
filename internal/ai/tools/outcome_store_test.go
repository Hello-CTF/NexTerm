package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// The production durable adapter maps the store's owner-scoped agent-outcome
// primitives verbatim: claims, progress residuals, completions, adjudication,
// and the result query behave exactly as the durable rows do, across owners
// and across store reopen (the restart boundary).

func TestWithStoreWiresDurableOutcomes(t *testing.T) {
	deps := WithStore(Dependencies{}, nil, nil)
	if deps.DurableOutcomes != nil {
		t.Fatal("nil storage produced a durable ledger")
	}
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	deps = WithStore(Dependencies{}, database, nil)
	if deps.DurableOutcomes == nil {
		t.Fatal("live storage did not wire the durable ledger")
	}
}

func TestDurableStoreOutcomesLifecycleAndOwnerIsolation(t *testing.T) {
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	durable := DurableStoreOutcomes(database)
	ctxA := ipc.WithUserID(context.Background(), "durable-owner-a")
	ctxB := ipc.WithUserID(context.Background(), "durable-owner-b")

	// Claim is atomic per owner: the second claimant loses, and a different
	// owner neither sees nor blocks the family.
	if _, claimed, err := durable.ClaimOutcome(ctxA, "op-family", "terminal_create"); err != nil || !claimed {
		t.Fatalf("first claim = %t err=%v", claimed, err)
	}
	if _, claimed, err := durable.ClaimOutcome(ctxA, "op-family", "terminal_create"); err != nil || claimed {
		t.Fatalf("duplicate claim = %t err=%v", claimed, err)
	}
	if _, claimed, err := durable.ClaimOutcome(ctxB, "op-family", "terminal_create"); err != nil || !claimed {
		t.Fatalf("cross-owner claim = %t err=%v", claimed, err)
	}
	// The progress residual lands on the in-flight row immediately.
	if err := durable.ProgressOutcome(ctxA, "op-family", "s-residual"); err != nil {
		t.Fatal(err)
	}
	pending, err := durable.OutcomeResult(ctxA, "op-family")
	if err != nil {
		t.Fatalf("pending result = %v", err)
	}
	if pending.State != "initiated" || pending.ResidualSessionID != "s-residual" {
		t.Fatalf("pending record = %+v", pending)
	}
	// Adjudication sees exactly one pending attempt per owner.
	adjudication, err := durable.AdjudicateOutcome(ctxA, "op-family")
	if err != nil || adjudication.Pending == nil || adjudication.Conflict || adjudication.Terminal != nil {
		t.Fatalf("adjudication = %+v err=%v", adjudication, err)
	}
	// Completion writes the history row and releases the claim atomically.
	if err := durable.CompleteOutcome(ctxA, "op-family", OutcomeAttempt{
		OperationID: "op-family:attempt-1", Kind: "terminal_create", State: "succeeded",
		ResidualSessionID: "s-residual", Result: json.RawMessage(`{"tabId":"tab-1"}`),
	}); err != nil {
		t.Fatal(err)
	}
	adjudication, err = durable.AdjudicateOutcome(ctxA, "op-family")
	if err != nil || adjudication.Pending != nil || adjudication.Conflict || adjudication.Terminal == nil {
		t.Fatalf("post-completion adjudication = %+v err=%v", adjudication, err)
	}
	result, err := durable.OutcomeResult(ctxA, "op-family")
	if err != nil || result.State != "succeeded" || !strings.Contains(string(result.Result), "tab-1") {
		t.Fatalf("completed result = %+v err=%v", result, err)
	}
	// The family is claimable again after completion; the other owner's own
	// claim row shows only its own pending attempt — never the first owner's
	// residual or history.
	if _, claimed, err := durable.ClaimOutcome(ctxA, "op-family", "terminal_create"); err != nil || !claimed {
		t.Fatalf("post-completion claim = %t err=%v", claimed, err)
	}
	crossResult, err := durable.OutcomeResult(ctxB, "op-family")
	if err != nil || crossResult.State != "initiated" || crossResult.ResidualSessionID != "" {
		t.Fatalf("cross-owner result leaked the family: %+v err=%v", crossResult, err)
	}
	crossOwner, err := durable.AdjudicateOutcome(ctxB, "op-family")
	if err != nil || crossOwner.Pending == nil || crossOwner.Terminal != nil || crossOwner.Conflict {
		t.Fatalf("cross-owner adjudication = %+v err=%v", crossOwner, err)
	}
	if crossOwner.Pending.ResidualSessionID != "" {
		t.Fatalf("cross-owner pending carried the residual: %+v", crossOwner.Pending)
	}
}

// The result query never resolves a different operation's record through a
// shared leading prefix: only the family itself and its ":…" descendant rows
// answer.
func TestDurableStoreOutcomesResultNeverPrefixConfused(t *testing.T) {
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	durable := DurableStoreOutcomes(database)
	ctx := ipc.WithUserID(context.Background(), "durable-owner")
	for _, id := range []string{"op-ab:attempt-1", "op-abcdef:attempt-1"} {
		if err := database.AgentOutcomeCompleteAttempt(ctx, strings.Split(id, ":attempt")[0], store.AgentOutcomeRow{
			OperationID: id, Kind: "terminal_create", State: store.AgentOutcomeStateSucceeded,
		}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := durable.OutcomeResult(ctx, "op-ab")
	if err != nil || result.OperationID != "op-ab:attempt-1" {
		t.Fatalf("family result = %+v err=%v", result, err)
	}
	// A bare ID that only prefixes a longer family has no record of its own.
	if _, err := durable.OutcomeResult(ctx, "op-abcd"); err == nil {
		t.Fatal("prefix-sibling query resolved the longer family's record")
	}
}

// TestDurableStoreOutcomesDatabase exercises the production adapter's full
// claim/progress/complete/adjudicate cycle against every real configured
// backend (PostgreSQL/MySQL/MariaDB via NEXTERM_TEST_DB_*), not just SQLite.
func TestDurableStoreOutcomesDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	durable := DurableStoreOutcomes(database)
	ctxA := ipc.WithUserID(context.Background(), "durable-db-owner-a")
	ctxB := ipc.WithUserID(context.Background(), "durable-db-owner-b")

	if _, claimed, err := durable.ClaimOutcome(ctxA, "op-db", "terminal_create"); err != nil || !claimed {
		t.Fatalf("claim = %t err=%v", claimed, err)
	}
	if _, claimed, err := durable.ClaimOutcome(ctxA, "op-db", "terminal_create"); err != nil || claimed {
		t.Fatalf("duplicate claim = %t err=%v", claimed, err)
	}
	if err := durable.ProgressOutcome(ctxA, "op-db", "s-db"); err != nil {
		t.Fatal(err)
	}
	adjudication, err := durable.AdjudicateOutcome(ctxA, "op-db")
	if err != nil || adjudication.Pending == nil || adjudication.Pending.ResidualSessionID != "s-db" || adjudication.Conflict {
		t.Fatalf("adjudication = %+v err=%v", adjudication, err)
	}
	if err := durable.CompleteOutcome(ctxA, "op-db", OutcomeAttempt{
		OperationID: "op-db:attempt-1", Kind: "terminal_create", State: "failed",
		FailureStage: "connect", Error: "dial refused", ResidualSessionID: "s-db",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := durable.OutcomeResult(ctxA, "op-db")
	if err != nil || result.State != "failed" || result.FailureStage != "connect" || result.ResidualSessionID != "s-db" {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	if _, err := durable.OutcomeResult(ctxB, "op-db"); err == nil {
		t.Fatal("cross-owner result query succeeded")
	}
	if _, claimed, err := durable.ClaimOutcome(ctxA, "op-db", "terminal_create"); err != nil || !claimed {
		t.Fatalf("post-completion claim = %t err=%v", claimed, err)
	}
}
