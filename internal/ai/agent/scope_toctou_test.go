package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

// gatedRunFinishStore blocks the run conditional-finish entry until
// released, so a status change can land after the scan but before the
// guarded write.
type gatedRunFinishStore struct {
	*store.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *gatedRunFinishStore) RunFinishUsageIfStatus(ctx context.Context, runID, expectedStatus, status, answer, errMsg string, turns int, tokensIn, tokensOut, cacheCreationTokens, latencyMS int64) (bool, error) {
	s.once.Do(func() { close(s.entered); <-s.release })
	return s.Store.RunFinishUsageIfStatus(ctx, runID, expectedStatus, status, answer, errMsg, turns, tokensIn, tokensOut, cacheCreationTokens, latencyMS)
}

func recoveryRealStore(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	db, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, context.Background()
}

func TestRunRecoveryStatusReplacedAfterCheckDoesNotWrite(t *testing.T) {
	db, ctx := recoveryRealStore(t)
	conversation, err := db.ConvCreate(ctx, "recover", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RunInsert(ctx, store.RunRow{ID: "run-x", ConversationID: conversation.ID, Status: store.RunStatusInterrupted}); err != nil {
		t.Fatal(err)
	}
	gated := &gatedRunFinishStore{Store: db, entered: make(chan struct{}), release: make(chan struct{})}
	probe := newFakeRunProbe()
	probe.set("run-x", RunRuntimeGone, "")

	done := make(chan RunRecoverySummary, 1)
	go func() {
		summary, err := NewRunRecovery(gated).Recover(ctx, probe)
		if err != nil {
			t.Error(err)
		}
		done <- summary
	}()
	// The run is resumed to running after the scan but before the guarded
	// write: the conditional finish must not land.
	<-gated.entered
	if err := db.RunUpdateStatus(ctx, "run-x", store.RunStatusRunning); err != nil {
		t.Fatal(err)
	}
	close(gated.release)
	summary := <-done

	if len(summary.Entries) != 1 || summary.Entries[0].Wrote {
		t.Fatalf("stale status wrote the run: %+v", summary.Entries)
	}
	row, err := db.RunGet(ctx, "run-x")
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.RunStatusRunning || row.FinishedAt != nil {
		t.Fatalf("resumed run was finished: %+v", row)
	}
	events, _ := db.RunEventsAfter(ctx, "run-x", 0)
	if len(events) != 0 {
		t.Fatalf("no-op appended an event: %+v", events)
	}
}

// TestOutcomeLedgerOrderingMonotonicityContract pins the ledger's
// ordering and monotonic completion contract: records are keyed by
// owner+operation ID, the first final write wins, and a later completion
// never regresses the recorded outcome. The ledger does not execute or
// bind assets: execution-time asset revalidation is the mutation tools'
// (M26) and wiring's (M15/M60) unfinished boundary, not this contract.
func TestOutcomeLedgerOrderingMonotonicityContract(t *testing.T) {
	ledger, _, ctx := outcomeLedgerForTest(t)
	if _, err := ledger.Begin(ctx, "op-1", "terminal_create"); err != nil {
		t.Fatal(err)
	}
	first, err := ledger.Succeed(ctx, "op-1", map[string]any{"tabId": "tab-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ledger.Fail(ctx, "op-1", "connect", errTestOutcome, OutcomeResidual{})
	if err != nil {
		t.Fatal(err)
	}
	if second.State != OutcomeStateSucceeded || string(second.Result) != string(first.Result) {
		t.Fatalf("completion regressed: %+v", second)
	}
	record, err := ledger.Result(ctx, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if record.State != OutcomeStateSucceeded || string(record.Result) != `{"tabId":"tab-1"}` {
		t.Fatalf("record = %+v", record)
	}
}

var errTestOutcome = errors.New("dial failed")
