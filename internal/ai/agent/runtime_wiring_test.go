package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/schema"
)

func recoveryRunner(t *testing.T) (*Runner, *store.Store, context.Context) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{Store: storage, Runs: storage})
	t.Cleanup(func() {
		_ = runner.Close()
		_ = storage.Close()
	})
	return runner, storage, context.Background()
}

func insertRecoveryRun(t *testing.T, storage *store.Store, ctx context.Context, id, conversationID, status string, finished bool) {
	t.Helper()
	run := store.RunRow{ID: id, ConversationID: conversationID, Status: status, CreatedAt: int64(1000 + len(id)), UpdatedAt: int64(1000 + len(id))}
	if finished {
		now := int64(2000)
		run.FinishedAt = &now
	}
	if err := storage.RunInsert(ctx, run); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerRecoverRunsRestartRecovery(t *testing.T) {
	runner, storage, ctx := recoveryRunner(t)
	conversation, err := storage.ConvCreate(ctx, "recover", nil)
	if err != nil {
		t.Fatal(err)
	}
	insertRecoveryRun(t, storage, ctx, "run-active", conversation.ID, store.RunStatusRunning, false)
	insertRecoveryRun(t, storage, ctx, "run-interrupted", conversation.ID, store.RunStatusInterrupted, false)
	insertRecoveryRun(t, storage, ctx, "run-finished", conversation.ID, store.RunStatusCompleted, true)
	if _, err := storage.RunAppendEvent(ctx, "run-active", "usage", func(seq uint64) ([]byte, error) {
		return []byte(`{"type":"usage","seq":1,"promptTokens":10,"completionTokens":5,"latencyMs":100}`), nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := runner.RecoverRuns(ctx); err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"run-active", "run-interrupted"} {
		row, err := storage.RunGet(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != store.RunStatusInterrupted || row.FinishedAt == nil {
			t.Fatalf("%s = %+v", runID, row)
		}
		events, err := storage.RunEventsAfter(ctx, runID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) == 0 || events[len(events)-1].Type != "error" {
			t.Fatalf("%s has no recovery error event: %+v", runID, events)
		}
	}
	active, _ := storage.RunGet(ctx, "run-active")
	if active.TokensIn != 10 || active.TokensOut != 5 {
		t.Fatalf("recovered usage = %+v", active)
	}
	finished, _ := storage.RunGet(ctx, "run-finished")
	if finished.Status != store.RunStatusCompleted || finished.UpdatedAt != int64(1000+len("run-finished")) {
		t.Fatalf("finished run was touched: %+v", finished)
	}
}

func TestRunnerRecoverRunsLargeSet(t *testing.T) {
	runner, storage, ctx := recoveryRunner(t)
	conversation, err := storage.ConvCreate(ctx, "recover-large", nil)
	if err != nil {
		t.Fatal(err)
	}
	const total = 120
	for i := 0; i < total; i++ {
		status := store.RunStatusRunning
		if i%2 == 1 {
			status = store.RunStatusInterrupted
		}
		insertRecoveryRun(t, storage, ctx, fmt.Sprintf("run-%03d", i), conversation.ID, status, false)
	}
	if err := runner.RecoverRuns(ctx); err != nil {
		t.Fatal(err)
	}
	recovered := 0
	for i := 0; i < total; i++ {
		row, err := storage.RunGet(ctx, fmt.Sprintf("run-%03d", i))
		if err != nil {
			t.Fatal(err)
		}
		if row.Status == store.RunStatusInterrupted && row.FinishedAt != nil {
			recovered++
		}
	}
	if recovered != total {
		t.Fatalf("recovered %d runs, want %d", recovered, total)
	}
}

func TestRunnerRecoverRunsConcurrentFinishGuarded(t *testing.T) {
	runner, storage, ctx := recoveryRunner(t)
	conversation, err := storage.ConvCreate(ctx, "recover-guard", nil)
	if err != nil {
		t.Fatal(err)
	}
	insertRecoveryRun(t, storage, ctx, "run-done", conversation.ID, store.RunStatusRunning, false)
	if err := storage.RunFinishUsage(ctx, "run-done", store.RunStatusCompleted, "fresh answer", "", 1, 1, 1, 0, 0); err != nil {
		t.Fatal(err)
	}

	if err := runner.RecoverRuns(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ := storage.RunGet(ctx, "run-done")
	if row.Status != store.RunStatusCompleted || row.Answer != "fresh answer" {
		t.Fatalf("concurrent finish was overwritten: %+v", row)
	}
	events, _ := storage.RunEventsAfter(ctx, "run-done", 0)
	for _, event := range events {
		if event.Type == "error" {
			t.Fatalf("recovery duplicated the terminal event: %+v", event)
		}
	}
}

func TestRunnerEvidenceCaptureSealAndSanitize(t *testing.T) {
	chat := &fakeModel{steps: []fakeStep{{chunks: []*schema.Message{schema.AssistantMessage("完成", nil)}}}}
	runner, _ := testRunner(t, chat, tools.Dependencies{}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "record evidence")
	waitClosed(t, stream)

	sanitized, anchor, err := runner.SanitizeRunCapture(context.Background(), response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if sanitized == nil || anchor == nil || anchor.MAC == "" {
		t.Fatalf("sanitize = %v anchor=%v", sanitized, anchor)
	}
	if err := sanitized.Verify(); err != nil {
		t.Fatalf("sanitized copy does not verify: %v", err)
	}
	if sanitized.SourceChainHead == "" {
		t.Fatal("sanitized copy has no source provenance")
	}
}

func TestRunnerEvidenceCaptureTamperFailsSanitize(t *testing.T) {
	chat := &fakeModel{steps: []fakeStep{{chunks: []*schema.Message{schema.AssistantMessage("完成", nil)}}}}
	runner, _ := testRunner(t, chat, tools.Dependencies{}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "tamper evidence")
	waitClosed(t, stream)

	capture := runner.lookupCapture(response.JobID)
	if capture == nil || !capture.Sealed {
		t.Fatalf("capture not sealed: %v", capture)
	}
	capture.Events = capture.Events[:0]
	if _, _, err := runner.SanitizeRunCapture(context.Background(), response.JobID); !errors.Is(err, ErrCaptureTampered) {
		t.Fatalf("tampered capture sanitize err=%v", err)
	}
}

func TestRunnerEvidenceCaptureUnsealedFailsSanitize(t *testing.T) {
	runner, _, _ := recoveryRunner(t)
	capture := runner.newJobCapture("job-live", "conv-1")
	if capture == nil {
		t.Fatal("evidence recorder not wired")
	}
	runner.rememberCapture("job-live", capture)
	if _, _, err := runner.SanitizeRunCapture(context.Background(), "job-live"); !errors.Is(err, ErrCaptureNotSealed) {
		t.Fatalf("unsealed capture sanitize err=%v", err)
	}
	if _, _, err := runner.SanitizeRunCapture(context.Background(), "job-missing"); !errors.Is(err, ErrCaptureNotSealed) && err == nil {
		t.Fatal("missing capture sanitized")
	}
}

func TestRunnerOutcomeResult(t *testing.T) {
	runner, storage, ctx := recoveryRunner(t)
	ledger := runner.outcomeLedger()
	if ledger == nil {
		t.Fatal("outcome ledger not wired")
	}
	if _, err := ledger.Begin(ctx, "op-1", "terminal_create"); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Succeed(ctx, "op-1", map[string]any{"tabId": "tab-1"}); err != nil {
		t.Fatal(err)
	}

	record, err := runner.OutcomeResult(ctx, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if record.State != OutcomeStateSucceeded || string(record.Result) != `{"tabId":"tab-1"}` {
		t.Fatalf("outcome result = %+v", record)
	}
	if _, err := runner.OutcomeResult(ctx, "op-missing"); !errors.Is(err, ErrOutcomeNotFound) {
		t.Fatalf("unknown outcome err=%v", err)
	}
	other := ipc.WithUserID(context.Background(), "owner-b")
	if _, err := runner.OutcomeResult(other, "op-1"); !errors.Is(err, ErrOutcomeNotFound) {
		t.Fatalf("cross-owner outcome err=%v", err)
	}
	_ = storage
}

func TestRunnerOutcomeResultUnsupported(t *testing.T) {
	runner := NewRunner(Config{})
	t.Cleanup(func() { _ = runner.Close() })
	if _, err := runner.OutcomeResult(context.Background(), "op-1"); err == nil {
		t.Fatal("outcome result without a store succeeded")
	} else {
		var appErr *ipc.Error
		if !errors.As(err, &appErr) || appErr.Code != ipc.CodeUnsupported {
			t.Fatalf("err = %v", err)
		}
	}
}
