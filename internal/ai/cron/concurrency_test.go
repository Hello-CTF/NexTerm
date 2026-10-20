package cron

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSimultaneousDueJobsShareGlobalExecutionBound(t *testing.T) {
	const (
		total = 100
		limit = 4
	)
	memory := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	started := make(chan string, total)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var active, maximum, calls atomic.Int32
	executor := executorFunc(func(_ context.Context, trigger Trigger) error {
		current := active.Add(1)
		for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
		}
		calls.Add(1)
		started <- trigger.JobID
		<-release
		active.Add(-1)
		return nil
	})
	scheduler := testScheduler(t, memory, executor, clock, func(options *Options) {
		options.MaxJobsPerSession = total
		options.MaxConcurrentExecutions = limit
	})
	for index := 0; index < total; index++ {
		registerTestJob(t, scheduler, fmt.Sprintf("job-%03d", index), "session")
	}

	now := clock.Add(time.Minute)
	unique := make(map[string]bool, total)
	claimed, err := scheduler.runOnce(context.Background(), now)
	if err != nil || claimed != limit {
		releaseOnce.Do(func() { close(release) })
		t.Fatalf("first claim = %d, %v; want %d", claimed, err, limit)
	}
	for index := 0; index < limit; index++ {
		select {
		case id := <-started:
			unique[id] = true
		case <-time.After(2 * time.Second):
			releaseOnce.Do(func() { close(release) })
			t.Fatal("execution slots did not fill")
		}
	}
	if len(started) != 0 || maximum.Load() != limit {
		releaseOnce.Do(func() { close(release) })
		t.Fatalf("execution exceeded bound: queued=%d maximum=%d", len(started), maximum.Load())
	}
	releaseOnce.Do(func() { close(release) })
	waitSchedulerWithTimeout(t, scheduler)

	totalClaims := claimed
	for attempts := 0; calls.Load() < total && attempts <= total/limit+1; attempts++ {
		claimed, err := scheduler.runOnce(context.Background(), now)
		if err != nil {
			t.Fatal(err)
		}
		totalClaims += claimed
		waitSchedulerWithTimeout(t, scheduler)
	}
	if calls.Load() != total || totalClaims != total || maximum.Load() > limit {
		t.Fatalf("calls=%d claims=%d maximum=%d", calls.Load(), totalClaims, maximum.Load())
	}
	for len(started) > 0 {
		unique[<-started] = true
	}
	if len(unique) != total {
		t.Fatalf("unique executions = %d; want %d", len(unique), total)
	}
}

func TestCancellationRenewsLeaseUntilExecutorStops(t *testing.T) {
	memory := newMemoryStore()
	started := make(chan struct{})
	canceling := make(chan struct{})
	cleanup := make(chan struct{})
	var cleanupOnce sync.Once
	defer cleanupOnce.Do(func() { close(cleanup) })
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
		Now: time.Now, OwnerID: "first", LeaseDuration: 150 * time.Millisecond,
		ExecutionTimeout: 100 * time.Millisecond, PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	registration := testRegistration("session")
	registration.ID = "cancel-job"
	job, err := scheduler.Register(context.Background(), registration)
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
	}), Options{Now: time.Now, OwnerID: "second"})
	if err != nil {
		t.Fatal(err)
	}

	runContext, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- scheduler.Run(runContext) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("execution did not start")
	}
	claimed := memory.job(t, job.ID)
	cancel()
	select {
	case <-canceling:
	case <-time.After(2 * time.Second):
		t.Fatal("executor did not observe cancellation")
	}

	deadline := time.Now().Add(2 * time.Second)
	renewedPastDeadline := false
	for time.Now().Before(deadline) && !renewedPastDeadline {
		current := memory.job(t, job.ID)
		renewedPastDeadline = time.Now().After(claimed.Run.Deadline) && current.Running() && current.Lease.ExpiresAt.After(time.Now().Add(30*time.Millisecond))
		if !renewedPastDeadline {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !renewedPastDeadline {
		t.Fatal("lease was not renewed while canceled executor cleaned up")
	}
	if claimed, err := second.runOnce(context.Background(), time.Now()); err != nil || claimed != 0 {
		t.Fatalf("competing claim during cleanup = %d, %v", claimed, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("execution duplicated during cleanup: %d", calls.Load())
	}
	select {
	case err := <-runDone:
		t.Fatalf("Run returned before executor cleanup: %v", err)
	default:
	}

	cleanupOnce.Do(func() { close(cleanup) })
	select {
	case err := <-runDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run leaked after executor cleanup")
	}
	finished := memory.job(t, job.ID)
	if finished.Running() || !strings.Contains(finished.LastError, context.Canceled.Error()) || calls.Load() != 1 {
		t.Fatalf("cancellation result = %+v, calls = %d", finished, calls.Load())
	}
}
