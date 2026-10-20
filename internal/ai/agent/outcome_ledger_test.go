package agent

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func outcomeLedgerForTest(t *testing.T) (*OutcomeLedger, *store.Store, context.Context) {
	t.Helper()
	db, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ledger := NewOutcomeLedger(db)
	ledger.now = func() int64 { return 7777 }
	return ledger, db, ipc.WithUserID(context.Background(), "owner-a")
}

func TestOutcomeLedgerBeginAndResultStates(t *testing.T) {
	ledger, _, ctx := outcomeLedgerForTest(t)

	// Unknown is distinct from in-progress: an unrecorded operation reports
	// ErrOutcomeNotFound, never a faked final state.
	if _, err := ledger.Result(ctx, "op-missing"); !errors.Is(err, ErrOutcomeNotFound) {
		t.Fatalf("unknown result err=%v", err)
	}

	record, err := ledger.Begin(ctx, "op-1", "terminal_create")
	if err != nil {
		t.Fatal(err)
	}
	if record.State != OutcomeStateInitiated || record.OperationID != "op-1" || record.Kind != "terminal_create" {
		t.Fatalf("begin = %+v", record)
	}
	if OutcomeStateFinal(record.State) {
		t.Fatal("initiated reported as final")
	}
	// Beginning twice returns the original record unchanged.
	again, err := ledger.Begin(ctx, "op-1", "other-kind")
	if err != nil {
		t.Fatal(err)
	}
	if again.Kind != "terminal_create" {
		t.Fatalf("second begin overwrote: %+v", again)
	}
	// An unfinished operation reports in-progress, not final.
	pending, err := ledger.Result(ctx, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != OutcomeStateInitiated || pending.FinishedAt != nil {
		t.Fatalf("in-progress result = %+v", pending)
	}
	if _, err := ledger.Begin(ctx, "", "kind"); err == nil {
		t.Fatal("empty operation id accepted")
	}
}

func TestOutcomeLedgerSucceedAndFailRecords(t *testing.T) {
	ledger, _, ctx := outcomeLedgerForTest(t)
	if _, err := ledger.Begin(ctx, "op-ok", "send_keys"); err != nil {
		t.Fatal(err)
	}
	succeeded, err := ledger.Succeed(ctx, "op-ok", map[string]any{"tabId": "tab-1"})
	if err != nil {
		t.Fatal(err)
	}
	if succeeded.State != OutcomeStateSucceeded || succeeded.FinishedAt == nil || *succeeded.FinishedAt != 7777 {
		t.Fatalf("succeed = %+v", succeeded)
	}
	if string(succeeded.Result) != `{"tabId":"tab-1"}` {
		t.Fatalf("result = %s", succeeded.Result)
	}

	if _, err := ledger.Begin(ctx, "op-bad", "terminal_create"); err != nil {
		t.Fatal(err)
	}
	failed, err := ledger.Fail(ctx, "op-bad", "connect", errors.New("dial failed"), OutcomeResidual{SessionID: "session-1", Resource: "tab-partial"})
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != OutcomeStateFailed || failed.FailureStage != "connect" || failed.Error != "dial failed" {
		t.Fatalf("fail = %+v", failed)
	}
	if failed.Residual.SessionID != "session-1" || failed.Residual.Resource != "tab-partial" {
		t.Fatalf("residual lost: %+v", failed.Residual)
	}
	// The pre-retry query returns the original outcome.
	original, err := ledger.Result(ctx, "op-bad")
	if err != nil {
		t.Fatal(err)
	}
	if original.State != OutcomeStateFailed || original.FailureStage != "connect" {
		t.Fatalf("result = %+v", original)
	}
}

func TestOutcomeLedgerCompletionIsMonotonic(t *testing.T) {
	ledger, _, ctx := outcomeLedgerForTest(t)
	if _, err := ledger.Begin(ctx, "op-1", "paste_submit"); err != nil {
		t.Fatal(err)
	}
	first, err := ledger.Succeed(ctx, "op-1", map[string]any{"attempt": 1})
	if err != nil {
		t.Fatal(err)
	}
	// A later failure never regresses the recorded success.
	second, err := ledger.Fail(ctx, "op-1", "paste", errors.New("late"), OutcomeResidual{})
	if err != nil {
		t.Fatal(err)
	}
	if second.State != OutcomeStateSucceeded || second.Error != "" {
		t.Fatalf("regressed: %+v", second)
	}
	stored, err := ledger.Result(ctx, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != first.State || string(stored.Result) != string(first.Result) {
		t.Fatalf("stored = %+v want %+v", stored, first)
	}

	// And a later success never overwrites a recorded failure either.
	if _, err := ledger.Begin(ctx, "op-2", "paste_submit"); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Fail(ctx, "op-2", "connect", errors.New("dial"), OutcomeResidual{}); err != nil {
		t.Fatal(err)
	}
	after, err := ledger.Succeed(ctx, "op-2", map[string]any{"attempt": 2})
	if err != nil {
		t.Fatal(err)
	}
	if after.State != OutcomeStateFailed || after.Error != "dial" {
		t.Fatalf("regressed: %+v", after)
	}
}

func TestOutcomeLedgerConcurrentCompletionSingleWinner(t *testing.T) {
	ledger, _, ctx := outcomeLedgerForTest(t)
	if _, err := ledger.Begin(ctx, "op-1", "terminal_create"); err != nil {
		t.Fatal(err)
	}
	const workers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	records := make(chan OutcomeRecord, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			var record OutcomeRecord
			var err error
			if worker%2 == 0 {
				record, err = ledger.Succeed(ctx, "op-1", map[string]any{"worker": worker})
			} else {
				record, err = ledger.Fail(ctx, "op-1", "stage", errors.New("failed"), OutcomeResidual{})
			}
			if err != nil {
				errs <- err
				return
			}
			records <- record
		}(i)
	}
	close(start)
	wg.Wait()
	close(records)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// Every caller observes the same single winning outcome.
	var winner OutcomeRecord
	for record := range records {
		if !OutcomeStateFinal(record.State) {
			t.Fatalf("non-final completion: %+v", record)
		}
		if winner.OperationID == "" {
			winner = record
			continue
		}
		if record.State != winner.State || string(record.Result) != string(winner.Result) {
			t.Fatalf("callers observed different outcomes: %+v vs %+v", record, winner)
		}
	}
}

func TestOutcomeLedgerOwnerIsolation(t *testing.T) {
	ledger, _, ctxA := outcomeLedgerForTest(t)
	if _, err := ledger.Begin(ctxA, "op-1", "terminal_create"); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Succeed(ctxA, "op-1", map[string]any{"tabId": "tab-1"}); err != nil {
		t.Fatal(err)
	}
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	if _, err := ledger.Result(ctxB, "op-1"); !errors.Is(err, ErrOutcomeNotFound) {
		t.Fatalf("cross-owner result err=%v", err)
	}
	if _, err := ledger.Succeed(ctxB, "op-1", map[string]any{"forged": true}); !errors.Is(err, ErrOutcomeNotFound) {
		t.Fatalf("cross-owner completion err=%v", err)
	}
	// Owner A's record is untouched.
	stored, err := ledger.Result(ctxA, "op-1")
	if err != nil || stored.State != OutcomeStateSucceeded {
		t.Fatalf("owner A record disturbed: %+v err=%v", stored, err)
	}
}

func TestOutcomeLedgerRestartRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outcome-ledger.db")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	ledger := NewOutcomeLedger(db)
	if _, err := ledger.Begin(ctx, "op-1", "terminal_create"); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Succeed(ctx, "op-1", map[string]any{"tabId": "tab-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Begin(ctx, "op-2", "send_keys"); err != nil {
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
	final, err := ledger.Result(ctx, "op-1")
	if err != nil || final.State != OutcomeStateSucceeded || string(final.Result) != `{"tabId":"tab-1"}` {
		t.Fatalf("recovered final = %+v err=%v", final, err)
	}
	pending, err := ledger.Result(ctx, "op-2")
	if err != nil || pending.State != OutcomeStateInitiated {
		t.Fatalf("recovered in-progress = %+v err=%v", pending, err)
	}
}
