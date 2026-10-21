package execbatch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func streamServiceResolver(targets ...Target) *fakeResolver {
	byID := make(map[string]Target, len(targets))
	for _, target := range targets {
		byID[target.SessionID] = target
	}
	return &fakeResolver{targets: byID}
}

func waitForBatchPhase(t *testing.T, service *Service, ownerID, batchID string, phase ProgressPhase) (BatchProgress, []TargetProgress) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		batch, targets, ok := service.BatchProgress(ownerID, batchID)
		if ok && batch.Phase == phase {
			return batch, targets
		}
		time.Sleep(time.Millisecond)
	}
	batch, targets, ok := service.BatchProgress(ownerID, batchID)
	t.Fatalf("batch %s never reached phase %s (ok=%v): %+v", batchID, phase, ok, batch)
	return batch, targets
}

func TestServiceExecuteStreamsProgressAndCapturedOutput(t *testing.T) {
	targetA := Target{
		SessionID: "session-a", SessionName: "a",
		Transport: &fakeStreamTransport{
			generation: 1,
			open: fakeHostScript{
				stdout: []string{"hello ", "world"}, stderr: []string{"warn"},
			}.open,
			exec: func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
				return base.ExecResult{}, errors.New("one-shot must not be used")
			},
		},
		Generation: 1,
	}
	targetB := Target{
		SessionID: "session-b", SessionName: "b",
		Transport: &fakeStreamTransport{
			generation: 1,
			open: fakeHostScript{
				stdout: []string{"bye"},
			}.open,
			exec: func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
				return base.ExecResult{}, errors.New("one-shot must not be used")
			},
		},
		Generation: 1,
	}
	auditor := &fakeAuditor{}
	service := New(Config{Resolver: streamServiceResolver(targetA, targetB), Auditor: auditor})

	batch, err := service.Execute(context.Background(), "owner-a", Request{
		BatchID: "batch-1", SessionIDs: []string{"session-a", "session-b"}, Command: "deploy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 2 {
		t.Fatalf("results = %+v", batch.Results)
	}
	byID := map[string]Result{}
	for _, result := range batch.Results {
		byID[result.SessionID] = result
	}
	if byID["session-a"].Status != StatusSuccess || byID["session-a"].Stdout != "hello world" || byID["session-a"].Stderr != "warn" {
		t.Fatalf("session-a result = %+v", byID["session-a"])
	}
	if byID["session-b"].Status != StatusSuccess || byID["session-b"].Stdout != "bye" {
		t.Fatalf("session-b result = %+v", byID["session-b"])
	}
	if len(auditor.records) != 2 {
		t.Fatalf("audit records = %d", len(auditor.records))
	}
	if _, _, ok := service.BatchProgress("owner-a", "batch-1"); ok {
		t.Fatal("progress still exposed after the batch finished")
	}
}

func TestServiceExecuteCanceledUnstartedTargetsFinishProgress(t *testing.T) {
	blocked := Target{
		SessionID: "session-a", SessionName: "a",
		Transport: &fakeStreamTransport{
			generation: 1,
			open:       fakeHostScript{block: true}.open,
			exec: func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
				return base.ExecResult{}, errors.New("one-shot must not be used")
			},
		},
		Generation: 1,
	}
	queued := Target{
		SessionID: "session-b", SessionName: "b",
		Transport: &fakeStreamTransport{
			generation: 1,
			open:       fakeHostScript{}.open,
			exec: func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
				return base.ExecResult{}, errors.New("one-shot must not be used")
			},
		},
		Generation: 1,
	}
	service := New(Config{Resolver: streamServiceResolver(blocked, queued)})

	done := make(chan Batch, 1)
	go func() {
		batch, err := service.Execute(context.Background(), "owner-a", Request{
			BatchID: "batch-1", SessionIDs: []string{"session-a", "session-b"}, Command: "deploy", MaxConcurrent: 1,
		})
		if err != nil {
			t.Error(err)
		}
		done <- batch
	}()
	batchProgress, _ := waitForBatchPhase(t, service, "owner-a", "batch-1", ProgressPhaseRunning)
	if batchProgress.Running != 1 || batchProgress.Pending != 1 {
		t.Fatalf("running snapshot = %+v", batchProgress)
	}
	service.mu.Lock()
	active := service.batches[batchKey{ownerID: "owner-a", batchID: "batch-1"}]
	service.mu.Unlock()
	captured := active.progress

	if _, err := service.Cancel(context.Background(), "owner-a", "batch-1"); err != nil {
		t.Fatal(err)
	}
	finished := <-done
	if len(finished.Results) != 2 {
		t.Fatalf("results = %+v", finished.Results)
	}
	finalBatch, finalTargets := captured.Snapshot()
	if finalBatch.Phase != ProgressPhaseFinished || finalBatch.Canceled != 2 {
		t.Fatalf("final progress = %+v", finalBatch)
	}
	for _, target := range finalTargets {
		if target.Status != StatusCanceled {
			t.Fatalf("target %s = %+v", target.SessionID, target)
		}
		if target.SessionID == "session-b" && target.StartedAt != 0 {
			t.Fatalf("never-started target fabricated StartedAt %d", target.StartedAt)
		}
	}
}

func TestServiceBatchProgressPublication(t *testing.T) {
	// Hold Execute inside startHistory (before tracker construction) while
	// progress readers poll: the tracker pointer must be published and read
	// under the service mutex, never observed mid-construction.
	history := &blockingHistory{release: make(chan struct{}), entered: make(chan struct{}, 4)}
	blocked := Target{
		SessionID: "session-a", SessionName: "a",
		Transport: &fakeStreamTransport{
			generation: 1,
			open:       fakeHostScript{stdout: []string{"done"}}.open,
			exec: func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
				return base.ExecResult{}, errors.New("one-shot must not be used")
			},
		},
		Generation: 1,
	}
	service := New(Config{Resolver: streamServiceResolver(blocked), History: history})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = service.Execute(context.Background(), "owner-a", Request{
			BatchID: "batch-1", SessionIDs: []string{"session-a"}, Command: "deploy",
		})
	}()
	<-history.entered

	pollsDone := make(chan struct{})
	go func() {
		defer close(pollsDone)
		for {
			select {
			case <-done:
				return
			default:
				_, _, _ = service.BatchProgress("owner-a", "batch-1")
			}
		}
	}()
	close(history.release)
	<-done
	<-pollsDone
}

type blockingHistory struct {
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (h *blockingHistory) ExecBatchHistorySave(context.Context, store.ExecBatchRow, []store.ExecBatchResultRow) error {
	h.once.Do(func() { h.entered <- struct{}{} })
	<-h.release
	return nil
}

func TestServiceBatchProgressOwnerScoped(t *testing.T) {
	blocked := Target{
		SessionID: "session-a", SessionName: "a",
		Transport: &fakeStreamTransport{
			generation: 1,
			open:       fakeHostScript{block: true}.open,
			exec: func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
				return base.ExecResult{}, errors.New("one-shot must not be used")
			},
		},
		Generation: 1,
	}
	service := New(Config{Resolver: streamServiceResolver(blocked)})
	done := make(chan Batch, 1)
	go func() {
		batch, err := service.Execute(context.Background(), "owner-a", Request{
			BatchID: "batch-1", SessionIDs: []string{"session-a"}, Command: "deploy",
		})
		if err != nil {
			t.Error(err)
		}
		done <- batch
	}()
	waitForBatchPhase(t, service, "owner-a", "batch-1", ProgressPhaseRunning)
	if _, _, ok := service.BatchProgress("owner-b", "batch-1"); ok {
		t.Fatal("owner B read owner A's batch progress")
	}
	if _, err := service.Cancel(context.Background(), "owner-a", "batch-1"); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestServiceRerunExecutesFailedTargetsOnly(t *testing.T) {
	exitCode := 3
	failed := Target{
		SessionID: "session-bad", SessionName: "bad",
		Transport: &fakeStreamTransport{
			generation: 1,
			open: fakeHostScript{
				stdout:  []string{"partial"},
				waitErr: &base.ExitError{Code: exitCode},
			}.open,
			exec: func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
				return base.ExecResult{}, errors.New("one-shot must not be used")
			},
		},
		Generation: 1,
	}
	passed := Target{
		SessionID: "session-ok", SessionName: "ok",
		Transport: &fakeStreamTransport{
			generation: 1,
			open:       fakeHostScript{stdout: []string{"fine"}}.open,
			exec: func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
				return base.ExecResult{}, errors.New("one-shot must not be used")
			},
		},
		Generation: 1,
	}
	resolver := streamServiceResolver(failed, passed)
	service := New(Config{Resolver: resolver})
	original := Request{BatchID: "batch-1", SessionIDs: []string{"session-ok", "session-bad"}, Command: "deploy"}

	first, err := service.Execute(context.Background(), "owner-a", original)
	if err != nil {
		t.Fatal(err)
	}
	callsAfterFirst := resolver.callCount()

	rerun, err := service.Rerun(context.Background(), "owner-a", original, first.Results, "batch-2")
	if err != nil {
		t.Fatal(err)
	}
	if rerun.BatchID != "batch-2" || len(rerun.Results) != 1 {
		t.Fatalf("rerun batch = %+v", rerun)
	}
	if rerun.Results[0].SessionID != "session-bad" || rerun.Results[0].Status != StatusExitError {
		t.Fatalf("rerun result = %+v", rerun.Results[0])
	}
	if resolver.callCount() != callsAfterFirst+1 {
		t.Fatalf("rerun resolved %d targets, want 1", resolver.callCount()-callsAfterFirst)
	}

	if _, err := service.Rerun(context.Background(), "owner-a", original, []Result{{SessionID: "session-ok", Status: StatusSuccess}}, "batch-3"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rerun without failed targets = %v", err)
	}
}

func TestServiceRerunRequiresFreshProductionConfirmation(t *testing.T) {
	production := Target{
		SessionID: "session-prod", SessionName: "prod", Environment: "production",
		Transport: &fakeStreamTransport{
			generation: 1,
			open: fakeHostScript{
				waitErr: &base.ExitError{Code: 1},
			}.open,
			exec: func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
				return base.ExecResult{}, errors.New("one-shot must not be used")
			},
		},
		Generation: 1,
	}
	service := New(Config{Resolver: streamServiceResolver(production), Auditor: &fakeAuditor{}})
	original := Request{BatchID: "batch-1", SessionIDs: []string{"session-prod"}, Command: "deploy"}

	_, err := service.Execute(context.Background(), "owner-a", original)
	var required *ConfirmationRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("first execute error = %v, want confirmation required", err)
	}
	detail := required.IPCDetail()
	confirmed := original
	confirmed.ProductionConfirmation = &ProductionConfirmation{
		Confirmed: true, TargetSetHash: detail.TargetSetHash, CommandHash: detail.CommandHash,
		EnvironmentNames: detail.RequiredEnvironmentNames,
	}
	first, err := service.Execute(context.Background(), "owner-a", confirmed)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Results) != 1 || first.Results[0].Status != StatusExitError {
		t.Fatalf("first batch results = %+v", first.Results)
	}

	_, err = service.Rerun(context.Background(), "owner-a", confirmed, first.Results, "batch-2")
	if !errors.As(err, &required) {
		t.Fatalf("rerun without fresh confirmation = %v", err)
	}
	if required.BatchID != "batch-2" {
		t.Fatalf("rerun challenge batch = %q, want batch-2", required.BatchID)
	}
}
