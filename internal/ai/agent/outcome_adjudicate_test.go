package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// TestOutcomeAdjudicateClassifiesPendingAndTerminal covers the pre-retry
// adjudication: an unknown prefix is empty, a pending attempt reports
// Pending, a finished attempt reports Terminal, and more than one unfinished
// attempt under the same prefix is a Conflict that must be reconciled
// explicitly instead of starting another attempt.
func TestOutcomeAdjudicateClassifiesPendingAndTerminal(t *testing.T) {
	ledger, _, ctx := outcomeLedgerForTest(t)

	adj, err := ledger.Adjudicate(ctx, "connect:asset-1:")
	if err != nil {
		t.Fatal(err)
	}
	if adj.Pending != nil || adj.Terminal != nil || adj.Conflict {
		t.Fatalf("unknown prefix adjudication = %+v", adj)
	}

	if _, err := ledger.Begin(ctx, "connect:asset-1:attempt-1", "connect_asset"); err != nil {
		t.Fatal(err)
	}
	adj, err = ledger.Adjudicate(ctx, "connect:asset-1:")
	if err != nil {
		t.Fatal(err)
	}
	if adj.Pending == nil || adj.Pending.OperationID != "connect:asset-1:attempt-1" || adj.Terminal != nil || adj.Conflict {
		t.Fatalf("pending adjudication = %+v", adj)
	}

	if _, err := ledger.Succeed(ctx, "connect:asset-1:attempt-1", map[string]any{"sessionId": "session-1"}); err != nil {
		t.Fatal(err)
	}
	adj, err = ledger.Adjudicate(ctx, "connect:asset-1:")
	if err != nil {
		t.Fatal(err)
	}
	if adj.Pending != nil || adj.Terminal == nil || adj.Terminal.State != OutcomeStateSucceeded || adj.Conflict {
		t.Fatalf("terminal adjudication = %+v", adj)
	}
	if string(adj.Terminal.Result) != `{"sessionId":"session-1"}` {
		t.Fatalf("terminal result = %s", adj.Terminal.Result)
	}

	// A later unfinished attempt alongside the terminal one: both are
	// reported, still without conflict.
	if _, err := ledger.Begin(ctx, "connect:asset-1:attempt-2", "connect_asset"); err != nil {
		t.Fatal(err)
	}
	adj, err = ledger.Adjudicate(ctx, "connect:asset-1:")
	if err != nil {
		t.Fatal(err)
	}
	if adj.Pending == nil || adj.Pending.OperationID != "connect:asset-1:attempt-2" ||
		adj.Terminal == nil || adj.Terminal.OperationID != "connect:asset-1:attempt-1" || adj.Conflict {
		t.Fatalf("mixed adjudication = %+v", adj)
	}

	// Two unfinished attempts under one prefix: conflict.
	if _, err := ledger.Begin(ctx, "connect:asset-1:attempt-3", "connect_asset"); err != nil {
		t.Fatal(err)
	}
	adj, err = ledger.Adjudicate(ctx, "connect:asset-1:")
	if err != nil {
		t.Fatal(err)
	}
	if !adj.Conflict || adj.Pending == nil || adj.Pending.OperationID != "connect:asset-1:attempt-3" {
		t.Fatalf("conflict adjudication = %+v", adj)
	}

	// A different prefix is unaffected by these records.
	adj, err = ledger.Adjudicate(ctx, "connect:asset-2:")
	if err != nil {
		t.Fatal(err)
	}
	if adj.Pending != nil || adj.Terminal != nil || adj.Conflict {
		t.Fatalf("unrelated prefix adjudication = %+v", adj)
	}
}

func TestOutcomeAdjudicateOwnerIsolation(t *testing.T) {
	ledger, _, ctxA := outcomeLedgerForTest(t)
	if _, err := ledger.Begin(ctxA, "connect:asset-1:attempt-1", "connect_asset"); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Succeed(ctxA, "connect:asset-1:attempt-1", map[string]any{"sessionId": "session-1"}); err != nil {
		t.Fatal(err)
	}
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	adj, err := ledger.Adjudicate(ctxB, "connect:")
	if err != nil {
		t.Fatal(err)
	}
	if adj.Pending != nil || adj.Terminal != nil || adj.Conflict {
		t.Fatalf("cross-owner adjudication = %+v", adj)
	}
}

func TestOutcomeAdjudicateRestartRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outcome-adjudicate.db")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	ledger := NewOutcomeLedger(db)
	if _, err := ledger.Begin(ctx, "connect:asset-1:attempt-1", "connect_asset"); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Fail(ctx, "connect:asset-1:attempt-1", "connect", errors.New("dial failed"), OutcomeResidual{SessionID: "session-residual"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Begin(ctx, "connect:asset-1:attempt-2", "connect_asset"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	ledger = NewOutcomeLedger(reopened)
	adj, err := ledger.Adjudicate(ctx, "connect:asset-1:")
	if err != nil {
		t.Fatal(err)
	}
	if adj.Pending == nil || adj.Pending.OperationID != "connect:asset-1:attempt-2" {
		t.Fatalf("recovered pending = %+v", adj)
	}
	if adj.Terminal == nil || adj.Terminal.State != OutcomeStateFailed || adj.Terminal.Residual.SessionID != "session-residual" {
		t.Fatalf("recovered terminal = %+v", adj)
	}
	if adj.Conflict {
		t.Fatalf("recovered adjudication conflicts: %+v", adj)
	}
}

func TestOutcomeAdjudicateValidation(t *testing.T) {
	ledger, _, ctx := outcomeLedgerForTest(t)
	if _, err := ledger.Adjudicate(ctx, ""); err == nil {
		t.Fatal("empty prefix accepted")
	} else {
		var appErr *ipc.Error
		if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam {
			t.Fatalf("empty prefix err=%v", err)
		}
	}
}
