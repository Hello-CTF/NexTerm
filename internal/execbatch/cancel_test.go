package execbatch

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestCancelBeforeExecutePreventsExecAndIsOwnerScoped(t *testing.T) {
	t.Run("same owner", func(t *testing.T) {
		var calls atomic.Int32
		transport := newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
			calls.Add(1)
			code := 0
			return base.ExecResult{ExitCode: &code}, nil
		})
		auditor := &fakeAuditor{}
		service := New(Config{
			Resolver: &fakeResolver{targets: map[string]Target{"s1": targetFor("s1", transport)}},
			Auditor:  auditor,
		})
		if _, err := service.Cancel(t.Context(), "owner-a", "early"); err != nil {
			t.Fatal(err)
		}
		batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "early", SessionIDs: []string{"s1"}, Command: "hostname"})
		if err != nil {
			t.Fatal(err)
		}
		if len(batch.Results) != 1 || batch.Results[0].Status != StatusCanceled {
			t.Fatalf("early-canceled batch = %+v", batch)
		}
		if calls.Load() != 0 || len(auditor.records) != 0 {
			t.Fatalf("early cancel executed or audited a command: calls=%d audits=%d", calls.Load(), len(auditor.records))
		}
	})

	t.Run("other owner pending cancel does not match", func(t *testing.T) {
		transport := successTransport()
		service := New(Config{Resolver: &fakeResolver{targets: map[string]Target{"s1": targetFor("s1", transport)}}})
		if _, err := service.Cancel(t.Context(), "owner-b", "shared-id"); err != nil {
			t.Fatal(err)
		}
		batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "shared-id", SessionIDs: []string{"s1"}, Command: "hostname"})
		if err != nil {
			t.Fatal(err)
		}
		if batch.Results[0].Status != StatusSuccess {
			t.Fatalf("owner B pending cancel affected owner A: %+v", batch)
		}
	})
}

func TestPendingCancelTTLAndCapacity(t *testing.T) {
	now := time.Unix(1000, 0)
	service := New(Config{
		Now:      func() time.Time { return now },
		Resolver: &fakeResolver{targets: map[string]Target{"s1": targetFor("s1", successTransport())}},
	})
	if _, err := service.Cancel(t.Context(), "owner-a", "expires"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(pendingCancelTTL + time.Nanosecond)
	batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "expires", SessionIDs: []string{"s1"}, Command: "hostname"})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Results[0].Status != StatusSuccess {
		t.Fatalf("expired pending cancel still applied: %+v", batch)
	}

	for index := range maxPendingCancelsPerOwner {
		if _, err := service.Cancel(t.Context(), "owner-a", fmt.Sprintf("owner-limit-%d", index)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Cancel(t.Context(), "owner-a", "owner-overflow"); !errors.Is(err, ErrPendingCancelLimit) {
		t.Fatalf("owner pending capacity error = %v", err)
	}
	if _, err := service.Cancel(t.Context(), "owner-b", "other-owner-still-accepted"); err != nil {
		t.Fatalf("owner A exhausted owner B quota: %v", err)
	}

	now = now.Add(pendingCancelTTL + time.Nanosecond)
	for owner := range maxPendingCancels / maxPendingCancelsPerOwner {
		for index := range maxPendingCancelsPerOwner {
			id := fmt.Sprintf("global-%d-%d", owner, index)
			if _, err := service.Cancel(t.Context(), fmt.Sprintf("owner-%d", owner), id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := service.Cancel(t.Context(), "owner-global-overflow", "global-overflow"); !errors.Is(err, ErrPendingCancelLimit) {
		t.Fatalf("global pending capacity error = %v", err)
	}
	now = now.Add(pendingCancelTTL + time.Nanosecond)
	if _, err := service.Cancel(t.Context(), "owner-global-overflow", "after-purge"); err != nil {
		t.Fatalf("expired entries were not purged: %v", err)
	}
}

func TestCancelWaitsForRegisteredExecAndBlocksQueuedStart(t *testing.T) {
	started := make(chan context.Context, 1)
	allowReturn := make(chan struct{})
	var calls atomic.Int32
	resolver := &fakeResolver{targets: make(map[string]Target)}
	for _, id := range []string{"s1", "s2"} {
		resolver.targets[id] = targetFor(id, newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
			if calls.Add(1) == 1 {
				started <- ctx
				<-ctx.Done()
				<-allowReturn
			}
			code := 0
			return base.ExecResult{ExitCode: &code}, nil
		}))
	}
	service := New(Config{Resolver: resolver})
	executeDone := make(chan Batch, 1)
	go func() {
		batch, _ := service.Execute(t.Context(), "owner-a", Request{
			BatchID: "linearized", SessionIDs: []string{"s1", "s2"}, Command: "wait", MaxConcurrent: 1,
		})
		executeDone <- batch
	}()
	ctx := <-started
	cancelDone := make(chan error, 1)
	go func() {
		_, err := service.Cancel(t.Context(), "owner-a", "linearized")
		cancelDone <- err
	}()
	<-ctx.Done()
	service.mu.Lock()
	canceling := service.batches[batchKey{ownerID: "owner-a", batchID: "linearized"}].canceling
	service.mu.Unlock()
	if !canceling {
		t.Fatal("context was canceled before the batch entered the canceling state")
	}
	select {
	case err := <-cancelDone:
		t.Fatalf("cancel returned before the registered Exec exited: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(allowReturn)
	if err := <-cancelDone; err != nil {
		t.Fatal(err)
	}
	batch := <-executeDone
	if calls.Load() != 1 {
		t.Fatalf("queued target started after cancel: %d Exec calls", calls.Load())
	}
	for _, result := range batch.Results {
		if result.Status != StatusCanceled {
			t.Fatalf("linearized result = %+v", result)
		}
	}
}

func TestOperationContextStatusWinsOverRemoteExitAndMatchesAudit(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		auditor := &fakeAuditor{}
		started := make(chan struct{}, 1)
		transport := newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
			started <- struct{}{}
			<-ctx.Done()
			code := 0
			return base.ExecResult{ExitCode: &code}, nil
		})
		service := New(Config{Resolver: &fakeResolver{targets: map[string]Target{"s1": targetFor("s1", transport)}}, Auditor: auditor})
		done := make(chan Batch, 1)
		go func() {
			batch, _ := service.Execute(t.Context(), "owner-a", Request{BatchID: "cancel-exit-race", SessionIDs: []string{"s1"}, Command: "exit 0"})
			done <- batch
		}()
		<-started
		if _, err := service.Cancel(t.Context(), "owner-a", "cancel-exit-race"); err != nil {
			t.Fatal(err)
		}
		batch := <-done
		assertExecBatchStatusAndAudit(t, batch, auditor, StatusCanceled)
	})

	t.Run("timeout", func(t *testing.T) {
		auditor := &fakeAuditor{}
		transport := newFakeTransport(func(ctx context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
			<-ctx.Done()
			code := 23
			return base.ExecResult{ExitCode: &code}, nil
		})
		service := New(Config{Resolver: &fakeResolver{targets: map[string]Target{"s1": targetFor("s1", transport)}}, Auditor: auditor})
		batch, err := service.Execute(t.Context(), "owner-a", Request{
			BatchID: "timeout-exit-race", SessionIDs: []string{"s1"}, Command: "exit 23", TimeoutMS: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertExecBatchStatusAndAudit(t, batch, auditor, StatusTimeout)
	})

	t.Run("ordinary non-zero exit", func(t *testing.T) {
		auditor := &fakeAuditor{}
		transport := newFakeTransport(func(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
			code := 23
			return base.ExecResult{ExitCode: &code}, nil
		})
		service := New(Config{Resolver: &fakeResolver{targets: map[string]Target{"s1": targetFor("s1", transport)}}, Auditor: auditor})
		batch, err := service.Execute(t.Context(), "owner-a", Request{BatchID: "ordinary-exit", SessionIDs: []string{"s1"}, Command: "exit 23"})
		if err != nil {
			t.Fatal(err)
		}
		assertExecBatchStatusAndAudit(t, batch, auditor, StatusExitError)
	})
}

func assertExecBatchStatusAndAudit(t *testing.T, batch Batch, auditor *fakeAuditor, want Status) {
	t.Helper()
	if len(batch.Results) != 1 || batch.Results[0].Status != want {
		t.Fatalf("batch status = %+v, want %s", batch.Results, want)
	}
	if len(auditor.records) != 1 {
		t.Fatalf("audit records = %d, want 1", len(auditor.records))
	}
	payload, ok := auditor.records[0].Payload.(auditPayload)
	if !ok || payload.Status != want {
		t.Fatalf("audit payload = %#v, want status %s", auditor.records[0].Payload, want)
	}
}
