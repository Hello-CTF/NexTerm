package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func runbookHistoryRow(id string, step int32, startedAt int64, status string) store.ExecBatchRow {
	finishedAt := startedAt + 10
	return store.ExecBatchRow{
		ID: id, CommandHash: strings.Repeat("a", 64), TargetSetHash: strings.Repeat("b", 64),
		Status: status, TargetCount: 1, StartedAt: startedAt, FinishedAt: &finishedAt,
		RunbookID: "runbook-deploy", RunID: "run-shared", StepNumber: step, StepCount: 2,
		GroupNumber: 1, GroupCount: 1,
	}
}

func withRunbookGroup(row store.ExecBatchRow, number, count int32) store.ExecBatchRow {
	row.GroupNumber = number
	row.GroupCount = count
	return row
}

func runbookHistoryResult(sessionID string, status string) store.ExecBatchResultRow {
	startedAt := int64(1000)
	finishedAt := int64(1010)
	result := store.ExecBatchResultRow{
		SessionID: sessionID, Position: 0, SessionName: "name-" + sessionID,
		Status: status, StartedAt: &startedAt, FinishedAt: &finishedAt,
	}
	if status != "success" {
		result.ErrorKind = store.ExecBatchErrorRemoteExit
	}
	return result
}

func TestExecBatchRunHistoryReadbackGroupsStepsAndScopesOwner(t *testing.T) {
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	for _, item := range []struct {
		row    store.ExecBatchRow
		result store.ExecBatchResultRow
	}{
		{runbookHistoryRow("step-two", 2, 3000, store.ExecBatchStatusSuccess), runbookHistoryResult("s3", "success")},
		{withRunbookGroup(runbookHistoryRow("step-one-b", 1, 2000, store.ExecBatchStatusSuccess), 2, 2), runbookHistoryResult("s2", "success")},
		{withRunbookGroup(runbookHistoryRow("step-one-a", 1, 1000, store.ExecBatchStatusFailed), 1, 2), runbookHistoryResult("s1", "exit_error")},
	} {
		if err := database.ExecBatchHistorySave(ctxA, item.row, []store.ExecBatchResultRow{item.result}); err != nil {
			t.Fatal(err)
		}
	}
	otherOwner := runbookHistoryRow("other-owner", 1, 4000, store.ExecBatchStatusSuccess)
	otherOwner.RunbookID = "runbook-other"
	if err := database.ExecBatchHistorySave(ctxB, otherOwner, []store.ExecBatchResultRow{runbookHistoryResult("other", "success")}); err != nil {
		t.Fatal(err)
	}

	history, err := database.ExecBatchRunHistoryGet(ctxA, "run-shared")
	if err != nil {
		t.Fatal(err)
	}
	if history.RunbookID != "runbook-deploy" || history.RunID != "run-shared" || history.Status != store.ExecBatchStatusFailed ||
		history.ReachedStep != 2 || history.StepCount != 2 || len(history.Batches) != 3 {
		t.Fatalf("run history = %+v", history)
	}
	wantIDs := []string{"step-one-a", "step-one-b", "step-two"}
	wantSteps := []int32{1, 1, 2}
	wantSessions := []string{"s1", "s2", "s3"}
	for index, batch := range history.Batches {
		if batch.Batch.ID != wantIDs[index] || batch.Batch.StepNumber != wantSteps[index] || batch.Batch.RunID != "run-shared" {
			t.Fatalf("batch %d = %+v", index, batch.Batch)
		}
		if len(batch.Results) != 1 || batch.Results[0].SessionID != wantSessions[index] {
			t.Fatalf("batch %d results = %+v", index, batch.Results)
		}
	}
	otherHistory, err := database.ExecBatchRunHistoryGet(ctxB, "run-shared")
	if err != nil {
		t.Fatal(err)
	}
	if otherHistory.RunbookID != "runbook-other" || len(otherHistory.Batches) != 1 || otherHistory.Batches[0].Batch.ID != "other-owner" {
		t.Fatalf("same-run-id history mixed owners: %+v", otherHistory)
	}
	if _, err := database.ExecBatchRunHistoryGet(ipc.WithUserID(context.Background(), "owner-c"), "run-shared"); err == nil {
		t.Fatal("cross-owner run history was visible")
	} else {
		var appErr *ipc.Error
		if !errors.As(err, &appErr) || appErr.Code != ipc.CodeNotFound {
			t.Fatalf("cross-owner error = %v", err)
		}
	}
}

func TestExecBatchHistoryRejectsPartialRunbookMetadata(t *testing.T) {
	database, err := store.OpenInMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	row := runbookHistoryRow("invalid", 1, 1000, store.ExecBatchStatusSuccess)
	row.RunID = ""
	if err := database.ExecBatchHistorySave(t.Context(), row, []store.ExecBatchResultRow{runbookHistoryResult("s1", "success")}); err == nil {
		t.Fatal("partial runbook metadata was accepted")
	}
}
