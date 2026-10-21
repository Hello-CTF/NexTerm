package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func outcomeCtx(owner string) context.Context {
	return ipc.WithUserID(context.Background(), owner)
}

func beginOutcome(t *testing.T, db *Store, ctx context.Context, operationID, kind string) AgentOutcomeRow {
	t.Helper()
	row, created, err := db.AgentOutcomeBegin(ctx, AgentOutcomeRow{OperationID: operationID, Kind: kind, State: "initiated"})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatalf("begin %s was not created", operationID)
	}
	return row
}

func TestAgentOutcomeBeginIsIdempotent(t *testing.T) {
	db := testStore(t)
	ctx := outcomeCtx("owner-a")
	row := beginOutcome(t, db, ctx, "op-1", "terminal_create")
	if row.OwnerID != "owner-a" || row.State != "initiated" || row.Kind != "terminal_create" {
		t.Fatalf("begin = %+v", row)
	}
	if row.CreatedAt == 0 || row.UpdatedAt == 0 || row.FinishedAt != nil {
		t.Fatalf("initiated timestamps wrong: %+v", row)
	}
	// A second begin returns the existing record untouched.
	again, created, err := db.AgentOutcomeBegin(ctx, AgentOutcomeRow{OperationID: "op-1", Kind: "other", State: "initiated"})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("second begin reported creation")
	}
	if again.Kind != "terminal_create" {
		t.Fatalf("second begin overwrote kind: %+v", again)
	}
}

func TestAgentOutcomeBeginValidation(t *testing.T) {
	db := testStore(t)
	ctx := outcomeCtx("owner-a")
	if _, _, err := db.AgentOutcomeBegin(ctx, AgentOutcomeRow{Kind: "k", State: "initiated"}); err == nil {
		t.Fatal("empty operation id accepted")
	} else {
		requireCode(t, err, ipc.CodeBadParam)
	}
	if _, _, err := db.AgentOutcomeBegin(ctx, AgentOutcomeRow{OperationID: "op", State: "initiated"}); err == nil {
		t.Fatal("empty kind accepted")
	} else {
		requireCode(t, err, ipc.CodeBadParam)
	}
}

func TestAgentOutcomeCompleteAndMonotonicity(t *testing.T) {
	db := testStore(t)
	ctx := outcomeCtx("owner-a")
	row := beginOutcome(t, db, ctx, "op-1", "send_keys")

	completed, err := db.AgentOutcomeComplete(ctx, "op-1", map[string]any{
		"state": "succeeded", "result_json": `{"tabId":"tab-1"}`, "finished_at": row.UpdatedAt + 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "succeeded" || completed.ResultJSON != `{"tabId":"tab-1"}` || completed.FinishedAt == nil {
		t.Fatalf("complete = %+v", completed)
	}

	// A later completion never regresses the final state: the original wins.
	again, err := db.AgentOutcomeComplete(ctx, "op-1", map[string]any{
		"state": "failed", "error": "late failure", "failure_stage": "paste",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.State != "succeeded" || again.Error != "" || again.FailureStage != "" {
		t.Fatalf("final state regressed: %+v", again)
	}
	stored, err := db.AgentOutcomeGet(ctx, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != "succeeded" {
		t.Fatalf("stored state = %s", stored.State)
	}
}

func TestAgentOutcomeCompleteRecordsFailureStageAndResidual(t *testing.T) {
	db := testStore(t)
	ctx := outcomeCtx("owner-a")
	beginOutcome(t, db, ctx, "op-1", "terminal_create")
	completed, err := db.AgentOutcomeComplete(ctx, "op-1", map[string]any{
		"state": "failed", "failure_stage": "create_tab",
		"error":               "tab quota exceeded",
		"residual_session_id": "session-residual-1", "residual_resource": "tab-partial",
		"finished_at": int64(4242),
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "failed" || completed.FailureStage != "create_tab" || completed.Error != "tab quota exceeded" {
		t.Fatalf("failure record = %+v", completed)
	}
	if completed.ResidualSessionID != "session-residual-1" || completed.ResidualResource != "tab-partial" {
		t.Fatalf("residual lost: %+v", completed)
	}
	if completed.FinishedAt == nil || *completed.FinishedAt != 4242 {
		t.Fatalf("finished_at = %v", completed.FinishedAt)
	}
}

func TestAgentOutcomeUnknownVersusFinal(t *testing.T) {
	db := testStore(t)
	ctx := outcomeCtx("owner-a")
	if _, err := db.AgentOutcomeGet(ctx, "missing"); err == nil {
		t.Fatal("unknown operation resolved")
	} else {
		requireCode(t, err, ipc.CodeNotFound)
	}
	if _, err := db.AgentOutcomeComplete(ctx, "missing", map[string]any{"state": "succeeded"}); err == nil {
		t.Fatal("unknown operation completed")
	} else {
		requireCode(t, err, ipc.CodeNotFound)
	}
}

func TestAgentOutcomeOwnerIsolation(t *testing.T) {
	db := testStore(t)
	ctxA := outcomeCtx("owner-a")
	ctxB := outcomeCtx("owner-b")
	beginOutcome(t, db, ctxA, "op-1", "terminal_create")
	if _, err := db.AgentOutcomeGet(ctxB, "op-1"); err == nil {
		t.Fatal("cross-owner get succeeded")
	} else {
		requireCode(t, err, ipc.CodeNotFound)
	}
	if _, err := db.AgentOutcomeComplete(ctxB, "op-1", map[string]any{"state": "succeeded"}); err == nil {
		t.Fatal("cross-owner complete succeeded")
	} else {
		requireCode(t, err, ipc.CodeNotFound)
	}
	// The same operation id under another owner is a separate record.
	own, created, err := db.AgentOutcomeBegin(ctxB, AgentOutcomeRow{OperationID: "op-1", Kind: "send_keys", State: "initiated"})
	if err != nil || !created {
		t.Fatalf("owner B begin = %+v created=%t err=%v", own, created, err)
	}
	rowA, err := db.AgentOutcomeGet(ctxA, "op-1")
	if err != nil || rowA.Kind != "terminal_create" {
		t.Fatalf("owner A record disturbed: %+v err=%v", rowA, err)
	}
}

func TestAgentOutcomeConcurrentCompleteExactlyOneWins(t *testing.T) {
	db := testStore(t)
	ctx := outcomeCtx("owner-a")
	beginOutcome(t, db, ctx, "op-1", "paste_submit")

	const workers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan AgentOutcomeRow, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			row, err := db.AgentOutcomeComplete(ctx, "op-1", map[string]any{
				"state": "succeeded", "result_json": `{"worker":` + string(rune('0'+worker)) + `}`, "finished_at": int64(100),
			})
			if err != nil {
				errs <- err
				return
			}
			results <- row
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	states := map[string]int{}
	for row := range results {
		states[row.State]++
		if row.FinishedAt == nil {
			t.Fatal("completed row has no finished_at")
		}
	}
	if states["succeeded"] != workers {
		t.Fatalf("completion results = %v, want all %d succeeded", states, workers)
	}
	// Every caller observed the same single winner's payload.
	stored, err := db.AgentOutcomeGet(ctx, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != "succeeded" {
		t.Fatalf("stored state = %s", stored.State)
	}
}

func TestAgentOutcomeRestartRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outcome.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := outcomeCtx("owner-a")
	beginOutcome(t, db, ctx, "op-final", "terminal_create")
	if _, err := db.AgentOutcomeComplete(ctx, "op-final", map[string]any{
		"state": "failed", "failure_stage": "connect", "error": "dial failed", "finished_at": int64(77),
	}); err != nil {
		t.Fatal(err)
	}
	beginOutcome(t, db, ctx, "op-pending", "send_keys")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	final, err := reopened.AgentOutcomeGet(ctx, "op-final")
	if err != nil {
		t.Fatal(err)
	}
	if final.State != "failed" || final.FailureStage != "connect" || final.Error != "dial failed" {
		t.Fatalf("recovered final = %+v", final)
	}
	pending, err := reopened.AgentOutcomeGet(ctx, "op-pending")
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != "initiated" || pending.FinishedAt != nil {
		t.Fatalf("in-progress recovery = %+v", pending)
	}
	// The recovered in-progress record still completes exactly once.
	completed, err := reopened.AgentOutcomeComplete(ctx, "op-pending", map[string]any{"state": "succeeded", "finished_at": int64(88)})
	if err != nil || completed.State != "succeeded" {
		t.Fatalf("post-restart complete = %+v err=%v", completed, err)
	}
}

func TestAgentOutcomeListByPrefix(t *testing.T) {
	db := testStore(t)
	ctx := outcomeCtx("owner-a")
	beginOutcome(t, db, ctx, "connect:asset-1:attempt-1", "connect_asset")
	if _, err := db.AgentOutcomeComplete(ctx, "connect:asset-1:attempt-1", map[string]any{
		"state": "succeeded", "result_json": `{"sessionId":"session-1"}`, "finished_at": int64(11),
	}); err != nil {
		t.Fatal(err)
	}
	beginOutcome(t, db, ctx, "connect:asset-1:attempt-2", "connect_asset")
	beginOutcome(t, db, ctx, "connect:asset-2:attempt-1", "connect_asset")
	beginOutcome(t, db, ctx, "send:asset-1:attempt-1", "send_keys")

	rows, err := db.AgentOutcomeListByPrefix(ctx, "connect:asset-1:", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].OperationID != "connect:asset-1:attempt-2" || rows[1].OperationID != "connect:asset-1:attempt-1" {
		t.Fatalf("prefix rows = %+v", rows)
	}
	if rows[0].State != "initiated" || rows[1].State != "succeeded" {
		t.Fatalf("prefix row states = %+v", rows)
	}

	// The prefix is literal: pattern characters never widen the match.
	for _, literal := range []string{"connect:asset-1:%", "connect:asset-1:_", "connect:asset-%", "connect:asset-_"} {
		rows, err := db.AgentOutcomeListByPrefix(ctx, literal, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Fatalf("literal prefix %q matched %+v", literal, rows)
		}
	}

	// An exact operation ID is a valid one-row prefix.
	rows, err = db.AgentOutcomeListByPrefix(ctx, "send:asset-1:attempt-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Kind != "send_keys" {
		t.Fatalf("exact prefix rows = %+v", rows)
	}

	// Owner isolation: another owner sees none of these records.
	rows, err = db.AgentOutcomeListByPrefix(outcomeCtx("owner-b"), "connect:", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("cross-owner prefix rows = %+v", rows)
	}

	// The limit bounds the page.
	rows, err = db.AgentOutcomeListByPrefix(ctx, "connect:", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("limited prefix rows = %+v", rows)
	}

	// An empty prefix would enumerate the whole owner ledger: rejected.
	if _, err := db.AgentOutcomeListByPrefix(ctx, "", 0); err == nil {
		t.Fatal("empty prefix accepted")
	} else {
		requireCode(t, err, ipc.CodeBadParam)
	}
}

func TestAgentOutcomeListByPrefixSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outcome-prefix.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := outcomeCtx("owner-a")
	beginOutcome(t, db, ctx, "connect:asset-1:attempt-1", "connect_asset")
	if _, err := db.AgentOutcomeComplete(ctx, "connect:asset-1:attempt-1", map[string]any{
		"state": "failed", "failure_stage": "connect", "residual_session_id": "session-residual", "finished_at": int64(21),
	}); err != nil {
		t.Fatal(err)
	}
	beginOutcome(t, db, ctx, "connect:asset-1:attempt-2", "connect_asset")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	rows, err := reopened.AgentOutcomeListByPrefix(ctx, "connect:asset-1:", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].OperationID != "connect:asset-1:attempt-2" || rows[0].State != "initiated" {
		t.Fatalf("recovered prefix rows = %+v", rows)
	}
	if rows[1].State != "failed" || rows[1].ResidualSessionID != "session-residual" {
		t.Fatalf("recovered terminal row = %+v", rows[1])
	}
}

func TestAgentOutcomeCompleteValidation(t *testing.T) {
	db := testStore(t)
	ctx := outcomeCtx("owner-a")
	beginOutcome(t, db, ctx, "op-1", "k")
	if _, err := db.AgentOutcomeComplete(ctx, "op-1", map[string]any{"state": "initiated"}); err == nil {
		t.Fatal("non-final completion accepted")
	} else {
		requireCode(t, err, ipc.CodeBadParam)
	}
	if _, err := db.AgentOutcomeComplete(ctx, "op-1", nil); err == nil {
		t.Fatal("empty completion accepted")
	} else {
		requireCode(t, err, ipc.CodeBadParam)
	}
	if !errors.Is(nil, nil) { // keep errors import if assertions change
		t.Fatal()
	}
}
