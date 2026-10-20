package cron

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type slowWaitAgentRunner struct {
	started     chan struct{}
	waitStarted chan struct{}
	release     chan struct{}
	once        sync.Once
	waitOnce    sync.Once
}

func (r *slowWaitAgentRunner) Start(context.Context, agent.ChatArgs, agent.StreamFactory) (agent.StartResponse, error) {
	r.once.Do(func() { close(r.started) })
	return agent.StartResponse{JobID: "slow-run"}, nil
}

func (r *slowWaitAgentRunner) Cancel(string) error { return nil }

func (r *slowWaitAgentRunner) Wait(context.Context, string) error {
	r.waitOnce.Do(func() { close(r.waitStarted) })
	<-r.release
	return nil
}

func TestAgentExecutorCancellationWaitsForUnderlyingRun(t *testing.T) {
	runner := &slowWaitAgentRunner{started: make(chan struct{}), waitStarted: make(chan struct{}), release: make(chan struct{})}
	executor := &AgentExecutor{Runner: runner}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- executor.Execute(ctx, testTrigger()) }()
	<-runner.started
	cancel()
	<-runner.waitStarted
	select {
	case err := <-done:
		t.Fatalf("Execute returned before Wait completed: %v", err)
	default:
	}
	close(runner.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute error = %v", err)
	}
}

type transientCASStore struct {
	*memoryStore
	mu        sync.Mutex
	casCalls  int
	failed    chan struct{}
	renewed   chan struct{}
	failOnce  sync.Once
	renewOnce sync.Once
}

func (s *transientCASStore) CompareAndSwap(ctx context.Context, job Job, expectedRevision uint64) (Job, error) {
	s.mu.Lock()
	s.casCalls++
	call := s.casCalls
	s.mu.Unlock()
	if call == 3 {
		s.failOnce.Do(func() { close(s.failed) })
		return Job{}, errors.New("transient renew failure")
	}
	updated, err := s.memoryStore.CompareAndSwap(ctx, job, expectedRevision)
	if call > 3 && err == nil {
		s.renewOnce.Do(func() { close(s.renewed) })
	}
	return updated, err
}

func TestTransientRenewFailureKeepsCleanupLease(t *testing.T) {
	memory := &transientCASStore{memoryStore: newMemoryStore(), failed: make(chan struct{}), renewed: make(chan struct{})}
	started := make(chan struct{})
	canceling := make(chan struct{})
	cleanup := make(chan struct{})
	var calls atomic.Int32
	executor := executorFunc(func(ctx context.Context, _ Trigger) error {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		close(canceling)
		<-cleanup
		return ctx.Err()
	})
	scheduler, err := NewScheduler(memory, executor, Options{
		Now: time.Now, OwnerID: "first", LeaseDuration: 60 * time.Millisecond,
		ExecutionTimeout: 90 * time.Millisecond, PollInterval: 2 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := scheduler.Register(context.Background(), Registration{SessionID: "session", Prompt: "slow", Schedule: "* * * * *"})
	if err != nil {
		t.Fatal(err)
	}
	job.NextRunAt = time.Now().Add(-time.Second)
	if _, err := memory.CompareAndSwap(context.Background(), job, job.Revision); err != nil {
		t.Fatal(err)
	}
	second, err := NewScheduler(memory, executorFunc(func(context.Context, Trigger) error {
		calls.Add(1)
		return nil
	}), Options{Now: time.Now, OwnerID: "second", PollInterval: 2 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- scheduler.Run(runContext) }()
	<-started
	claimed := memory.job(t, job.ID)
	<-memory.failed
	<-canceling
	<-memory.renewed

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current := memory.job(t, job.ID)
		if time.Now().After(claimed.Run.Deadline) && current.Running() && current.Lease.ExpiresAt.After(time.Now().Add(20*time.Millisecond)) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if claimed, err := second.runOnce(context.Background(), time.Now()); err != nil || claimed != 0 {
		t.Fatalf("competing claim after transient renew = %d, %v", claimed, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("execution duplicated during transient cleanup: %d", calls.Load())
	}
	close(cleanup)
	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not finish after cleanup")
	}
	if calls.Load() != 1 {
		t.Fatalf("execution duplicated after cleanup: %d", calls.Load())
	}
}

func TestSQLStoreDistinguishesAccountOpenAndInternalAccess(t *testing.T) {
	storage := newTestSQLiteStore(t)
	storage.shared = false
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	if _, err := storage.Create(ctxA, fullTestJob("account-job", "session", time.Now()), 4); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.List(context.Background()); err == nil {
		t.Fatal("ownerless account-mode List was accepted")
	}
	if _, err := storage.Create(context.Background(), fullTestJob("unknown-job", "session", time.Now()), 4); err == nil {
		t.Fatal("ownerless account-mode Create was accepted")
	}
	jobs, err := storage.List(owner.WithOpenAccess(context.Background()))
	if err != nil || len(jobs) != 0 {
		t.Fatalf("open List exposed account jobs: %+v, %v", jobs, err)
	}
	jobs, err = storage.List(WithInternalAccess(context.Background()))
	if err != nil || len(jobs) != 1 || jobs[0].OwnerID != "owner-a" {
		t.Fatalf("internal List = %+v, %v", jobs, err)
	}
	if _, err := storage.CompareAndSwap(context.Background(), fullTestJob("account-job", "session", time.Now()), 1); err == nil {
		t.Fatal("ownerless account-mode CAS was accepted")
	}
	if err := storage.Delete(context.Background(), "session", "account-job", 1); err == nil {
		t.Fatal("ownerless account-mode Delete was accepted")
	}
}
