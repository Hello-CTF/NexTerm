package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func requireExecBatchStatus(t *testing.T, ctx context.Context, database *store.Store, id, wantStatus string, wantFinished bool) {
	t.Helper()
	history, err := database.ExecBatchHistoryGet(ctx, id)
	if err != nil {
		t.Fatalf("history %s: %v", id, err)
	}
	if history.Batch.Status != wantStatus || (history.Batch.FinishedAt != nil) != wantFinished {
		t.Fatalf("batch %s = status %q finished %v, want %q finished %v",
			id, history.Batch.Status, history.Batch.FinishedAt != nil, wantStatus, wantFinished)
	}
}

func TestExecBatchStartupReconcileMarksOnlyStaleRunningRowsDatabase(t *testing.T) {
	database := dbtest.NewFixture(t).OpenStore(t)
	ctxA := execBatchDatabaseContext("owner-a")
	ctxB := execBatchDatabaseContext("owner-b")
	now := time.Now().UnixMilli()

	staleRows := map[string]context.Context{
		"stale-a": ctxA,
		"stale-b": ctxB,
	}
	for id, ctx := range staleRows {
		if err := database.ExecBatchHistorySave(ctx, execBatchDatabaseRow(id, 1000, store.ExecBatchStatusRunning), nil); err != nil {
			t.Fatal(err)
		}
	}
	finished := execBatchDatabaseRow("finished-a", now-20_000, store.ExecBatchStatusSuccess)
	if err := database.ExecBatchHistorySave(ctxA, finished, []store.ExecBatchResultRow{
		execBatchDatabaseResult("s-finished", 0, "success"),
	}); err != nil {
		t.Fatal(err)
	}

	reconciled, err := database.ExecBatchStartupReconcile(context.Background())
	if err != nil || reconciled != int64(len(staleRows)) {
		t.Fatalf("reconcile = %d, %v; want %d", reconciled, err, len(staleRows))
	}
	for id, ctx := range staleRows {
		requireExecBatchStatus(t, ctx, database, id, store.ExecBatchStatusFailed, true)
	}
	requireExecBatchStatus(t, ctxA, database, "finished-a", store.ExecBatchStatusSuccess, true)

	reconciledAgain, err := database.ExecBatchStartupReconcile(context.Background())
	if err != nil || reconciledAgain != 0 {
		t.Fatalf("second reconcile = %d, %v; want 0", reconciledAgain, err)
	}

	// Reconciled rows are finished, so age retention can now delete them while
	// recently finished rows survive.
	result, err := database.EnforceRetention(context.Background(), store.RetentionPolicy{ExecBatchMaxAge: time.Hour})
	if err != nil || result.ExecBatchesDeleted != int64(len(staleRows)) {
		t.Fatalf("retention after reconcile = %+v, %v", result, err)
	}
	for id, ctx := range staleRows {
		requireExecBatchHistoryMissing(t, ctx, database, id)
	}
	requireExecBatchStatus(t, ctxA, database, "finished-a", store.ExecBatchStatusSuccess, true)
}
