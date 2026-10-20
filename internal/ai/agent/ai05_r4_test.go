package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/schema"
)

// legacyRunStore forwards the RunStore surface but none of the newer
// conditional-finish capabilities, mimicking a legacy store.
type legacyRunStore struct {
	db *store.Store
}

func (s legacyRunStore) RunInsert(ctx context.Context, row store.RunRow) error {
	return s.db.RunInsert(ctx, row)
}

func (s legacyRunStore) RunGet(ctx context.Context, id string) (store.RunRow, error) {
	return s.db.RunGet(ctx, id)
}

func (s legacyRunStore) RunDelete(ctx context.Context, id string) error {
	return s.db.RunDelete(ctx, id)
}

func (s legacyRunStore) RunList(ctx context.Context, conversationID string, limit int) ([]store.RunRow, error) {
	return s.db.RunList(ctx, conversationID, limit)
}

func (s legacyRunStore) RunsActive(ctx context.Context) ([]store.RunRow, error) {
	return s.db.RunsActive(ctx)
}

func (s legacyRunStore) RunAppendEvent(ctx context.Context, runID, eventType string, payload func(seq uint64) ([]byte, error)) (uint64, error) {
	return s.db.RunAppendEvent(ctx, runID, eventType, payload)
}

func (s legacyRunStore) RunEventsAfter(ctx context.Context, runID string, afterSeq uint64) ([]store.RunEventRow, error) {
	return s.db.RunEventsAfter(ctx, runID, afterSeq)
}

func (s legacyRunStore) RunEventsAfterLimit(ctx context.Context, runID string, afterSeq uint64, limit int) ([]store.RunEventRow, error) {
	return s.db.RunEventsAfterLimit(ctx, runID, afterSeq, limit)
}

func (s legacyRunStore) RunErrorCounts(ctx context.Context, runIDs []string) (map[string]store.RunErrorCounts, error) {
	return s.db.RunErrorCounts(ctx, runIDs)
}

func (s legacyRunStore) RunUpdateStatus(ctx context.Context, runID, status string) error {
	return s.db.RunUpdateStatus(ctx, runID, status)
}

func (s legacyRunStore) RunFinish(ctx context.Context, runID, status, answer, errMsg string, turns int, tokensIn, tokensOut int64) error {
	return s.db.RunFinish(ctx, runID, status, answer, errMsg, turns, tokensIn, tokensOut)
}

func (s legacyRunStore) RunFinishUsage(ctx context.Context, runID, status, answer, errMsg string, turns int, tokensIn, tokensOut, cacheCreationTokens, latencyMS int64) error {
	return s.db.RunFinishUsage(ctx, runID, status, answer, errMsg, turns, tokensIn, tokensOut, cacheCreationTokens, latencyMS)
}

func (s legacyRunStore) HitlRunSave(ctx context.Context, id string, data []byte) error {
	return s.db.HitlRunSave(ctx, id, data)
}

func (s legacyRunStore) HitlRunList(ctx context.Context) ([]store.HitlRunRow, error) {
	return s.db.HitlRunList(ctx)
}

func (s legacyRunStore) HitlRunDelete(ctx context.Context, id string) error {
	return s.db.HitlRunDelete(ctx, id)
}

func TestRunFinishWithEventIfStatusAtomic(t *testing.T) {
	db, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := ipc.WithUserID(context.Background(), "owner-a")
	conversation, err := db.ConvCreate(ctx, "atomic", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RunInsert(ctx, store.RunRow{ID: "run-1", ConversationID: conversation.ID, Status: store.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}

	// Applied: the event and the finish land together, exactly once.
	applied, err := db.RunFinishWithEventIfStatus(ctx, "run-1", store.RunStatusRunning, store.RunStatusFailed, "", "runtime gone", 0, 0, 0, 0, 0, "error", func(seq uint64) ([]byte, error) {
		return []byte(`{"type":"error","message":"runtime gone"}`), nil
	})
	if err != nil || !applied {
		t.Fatalf("applied=%t err=%v", applied, err)
	}
	row, _ := db.RunGet(ctx, "run-1")
	if row.Status != store.RunStatusFailed || row.FinishedAt == nil {
		t.Fatalf("row = %+v", row)
	}
	events, _ := db.RunEventsAfter(ctx, "run-1", 0)
	if len(events) != 1 || events[0].Type != "error" {
		t.Fatalf("events = %+v", events)
	}

	// A second call with a stale expected status is a no-op: no event, no
	// state change, exactly-once preserved.
	applied, err = db.RunFinishWithEventIfStatus(ctx, "run-1", store.RunStatusRunning, store.RunStatusInterrupted, "", "stale", 0, 0, 0, 0, 0, "error", func(seq uint64) ([]byte, error) {
		return []byte(`{"type":"error","message":"stale"}`), nil
	})
	if err != nil || applied {
		t.Fatalf("stale applied=%t err=%v", applied, err)
	}
	events, _ = db.RunEventsAfter(ctx, "run-1", 0)
	if len(events) != 1 {
		t.Fatalf("no-op appended an event: %+v", events)
	}
	row, _ = db.RunGet(ctx, "run-1")
	if row.Status != store.RunStatusFailed || row.Error != "runtime gone" {
		t.Fatalf("no-op changed the row: %+v", row)
	}
}

// gatedAtomicStore blocks the atomic recovery write until released, so a
// concurrent writer can land between the scan and the write.
type gatedAtomicStore struct {
	*store.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *gatedAtomicStore) RunFinishWithEventIfStatus(ctx context.Context, runID, expectedStatus, status, answer, errMsg string, turns int, tokensIn, tokensOut, cacheCreationTokens, latencyMS int64, eventType string, eventPayload func(seq uint64) ([]byte, error)) (bool, error) {
	s.once.Do(func() { close(s.entered); <-s.release })
	return s.Store.RunFinishWithEventIfStatus(ctx, runID, expectedStatus, status, answer, errMsg, turns, tokensIn, tokensOut, cacheCreationTokens, latencyMS, eventType, eventPayload)
}

func TestRunnerRecoverRunsGatedInterleaves(t *testing.T) {
	for _, test := range []struct {
		name         string
		scanned      string
		concurrent   func(t *testing.T, storage *store.Store, ctx context.Context)
		wantStatus   string
		wantFinished bool
	}{
		{"completed", store.RunStatusRunning, func(t *testing.T, storage *store.Store, ctx context.Context) {
			if err := storage.RunFinishUsage(ctx, "run-x", store.RunStatusCompleted, "fresh answer", "", 1, 1, 1, 0, 0); err != nil {
				t.Fatal(err)
			}
		}, store.RunStatusCompleted, true},
		{"canceled", store.RunStatusRunning, func(t *testing.T, storage *store.Store, ctx context.Context) {
			if err := storage.RunFinishUsage(ctx, "run-x", store.RunStatusCanceled, "", "", 0, 0, 0, 0, 0); err != nil {
				t.Fatal(err)
			}
		}, store.RunStatusCanceled, true},
		{"resumed", store.RunStatusInterrupted, func(t *testing.T, storage *store.Store, ctx context.Context) {
			if err := storage.RunUpdateStatus(ctx, "run-x", store.RunStatusRunning); err != nil {
				t.Fatal(err)
			}
		}, store.RunStatusRunning, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage, err := store.OpenInMemory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = storage.Close() })
			ctx := context.Background()
			conversation, err := storage.ConvCreate(ctx, "interleave", nil)
			if err != nil {
				t.Fatal(err)
			}
			insertRecoveryRun(t, storage, ctx, "run-x", conversation.ID, test.scanned, false)
			gated := &gatedAtomicStore{Store: storage, entered: make(chan struct{}), release: make(chan struct{})}
			runner := NewRunner(Config{Store: storage, Runs: gated})
			t.Cleanup(func() { _ = runner.Close() })

			done := make(chan error, 1)
			go func() { done <- runner.RecoverRuns(ctx) }()
			// The gate is entered: recovery is inside the atomic write.
			<-gated.entered
			test.concurrent(t, storage, ctx)
			close(gated.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}

			row, _ := storage.RunGet(ctx, "run-x")
			if row.Status != test.wantStatus {
				t.Fatalf("row status = %s want %s", row.Status, test.wantStatus)
			}
			if test.wantFinished && row.FinishedAt == nil {
				t.Fatalf("row has no finish timestamp: %+v", row)
			}
			if !test.wantFinished && row.FinishedAt != nil {
				t.Fatalf("resumed run was finished: %+v", row)
			}
			events, _ := storage.RunEventsAfter(ctx, "run-x", 0)
			for _, event := range events {
				if event.Type == "error" && strings.Contains(event.PayloadJSON, "重启") {
					t.Fatalf("recovery appended a terminal event on a no-op: %+v", event)
				}
			}
		})
	}
}

func TestRunnerRecoverRunsExactlyOnce(t *testing.T) {
	runner, storage, ctx := recoveryRunner(t)
	conversation, err := storage.ConvCreate(ctx, "exactly-once", nil)
	if err != nil {
		t.Fatal(err)
	}
	insertRecoveryRun(t, storage, ctx, "run-once", conversation.ID, store.RunStatusRunning, false)

	if err := runner.RecoverRuns(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ := storage.RunGet(ctx, "run-once")
	if row.Status != store.RunStatusInterrupted || row.FinishedAt == nil {
		t.Fatalf("row = %+v", row)
	}
	events, _ := storage.RunEventsAfter(ctx, "run-once", 0)
	if len(events) != 1 || events[0].Type != "error" {
		t.Fatalf("recovery events = %+v", events)
	}
	firstUpdated := row.UpdatedAt

	if err := runner.RecoverRuns(ctx); err != nil {
		t.Fatal(err)
	}
	events, _ = storage.RunEventsAfter(ctx, "run-once", 0)
	if len(events) != 1 {
		t.Fatalf("repeated recovery appended events: %+v", events)
	}
	row, _ = storage.RunGet(ctx, "run-once")
	if row.UpdatedAt != firstUpdated {
		t.Fatalf("repeated recovery rewrote the row: %+v", row)
	}
}

func TestRunnerRecoverRunsLegacyStoreUnsupported(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	ctx := context.Background()
	conversation, err := storage.ConvCreate(ctx, "legacy", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.RunInsert(ctx, store.RunRow{ID: "run-legacy", ConversationID: conversation.ID, Status: store.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{Store: storage, Runs: legacyRunStore{db: storage}})
	t.Cleanup(func() { _ = runner.Close() })

	err = runner.RecoverRuns(ctx)
	if err == nil {
		t.Fatal("legacy store recovery succeeded without the conditional capability")
	}
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || appErr.Code != ipc.CodeUnsupported {
		t.Fatalf("err = %v", err)
	}
	row, _ := storage.RunGet(ctx, "run-legacy")
	if row.Status != store.RunStatusRunning || row.FinishedAt != nil {
		t.Fatalf("legacy recovery wrote the row: %+v", row)
	}
	events, _ := storage.RunEventsAfter(ctx, "run-legacy", 0)
	if len(events) != 0 {
		t.Fatalf("legacy recovery appended events: %+v", events)
	}
}

func TestRunnerRecoverRunsScannedRunningPendingCancel(t *testing.T) {
	storage := restartStore(t)
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	_ = waitEvent(t, stream, "confirmRequired")

	// Force the scanned-running branch: the row is running while a
	// restorable pending HITL exists, as after a crash between the HITL
	// persist and the status update.
	if err := storage.RunUpdateStatus(context.Background(), response.JobID, store.RunStatusRunning); err != nil {
		t.Fatal(err)
	}

	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), deps, nil)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	row, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil || row.Status != store.RunStatusInterrupted || row.FinishedAt != nil {
		t.Fatalf("row after recovery = %+v err=%v", row, err)
	}
	if err := restarted.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	row = waitRunStatus(t, storage, response.JobID, store.RunStatusCanceled)
	if row.FinishedAt == nil {
		t.Fatalf("canceled run has no finish timestamp: %+v", row)
	}
	events, _ := storage.RunEventsAfter(context.Background(), response.JobID, 0)
	found := false
	for _, event := range events {
		if event.Type == "canceled" {
			found = true
		}
	}
	if !found {
		t.Fatalf("canceled run has no canceled event: %+v", events)
	}
}

func TestRunnerTypedNilStoreCapabilities(t *testing.T) {
	runner := NewRunner(Config{Store: (*store.Store)(nil), Runs: (*store.Store)(nil)})
	t.Cleanup(func() { _ = runner.Close() })
	if _, err := runner.OutcomeResult(context.Background(), "op-typed-nil"); err == nil {
		t.Fatal("typed-nil outcome result succeeded")
	} else {
		var appErr *ipc.Error
		if !errors.As(err, &appErr) || appErr.Code != ipc.CodeUnsupported {
			t.Fatalf("outcome err = %v", err)
		}
	}
	if _, _, err := runner.SanitizeRunCapture(context.Background(), "job-typed-nil"); err == nil {
		t.Fatal("typed-nil sanitize succeeded")
	} else {
		var appErr *ipc.Error
		if !errors.As(err, &appErr) || appErr.Code != ipc.CodeUnsupported {
			t.Fatalf("sanitize err = %v", err)
		}
	}
	if err := runner.RecoverRuns(context.Background()); err == nil {
		t.Fatal("typed-nil recovery succeeded")
	} else {
		var appErr *ipc.Error
		if !errors.As(err, &appErr) || appErr.Code != ipc.CodeUnsupported {
			t.Fatalf("recovery err = %v", err)
		}
	}
}

func TestEvidenceZeroEventLifecycleMarker(t *testing.T) {
	recorder, verifier, db, ctx := evidenceTestPair(t)
	capture := recorder.NewRecording("cap-empty", "conv-1", "job-1", 1700000000000)
	anchor, err := recorder.Seal(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	events, err := verifier.Replay(ctx, capture)
	if err != nil {
		t.Fatalf("empty capture replay err=%v", err)
	}
	if len(events) != 1 || events[0].Kind != CaptureEventTiming || events[0].Name != "lifecycle" {
		t.Fatalf("empty capture has no lifecycle marker: %+v", events)
	}
	sanitized, sanitizedAnchor, err := verifier.Sanitize(ctx, capture)
	if err != nil {
		t.Fatalf("empty capture sanitize err=%v", err)
	}
	if _, err := verifier.Replay(ctx, sanitized); err != nil {
		t.Fatalf("sanitized empty capture replay err=%v", err)
	}
	if err := capture.verifyAnchor(keyFrom(t, db, ctx), anchor); err != nil {
		t.Fatalf("empty capture anchor does not verify: %v", err)
	}
	if err := sanitized.verifyAnchor(keyFrom(t, db, ctx), sanitizedAnchor); err != nil {
		t.Fatalf("sanitized empty anchor does not verify: %v", err)
	}
}
