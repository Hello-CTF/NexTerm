package cron

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOverlapIsBlockedLocallyAndByDurableLease(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	clock := newFakeClock(start)
	started := make(chan Trigger, 2)
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	executor := executorFunc(func(_ context.Context, trigger Trigger) error {
		current := active.Add(1)
		for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
		}
		started <- trigger
		<-release
		active.Add(-1)
		return nil
	})
	first := testScheduler(t, store, executor, clock, func(options *Options) { options.OwnerID = "owner-a" })
	second := testScheduler(t, store, executor, clock, func(options *Options) { options.OwnerID = "owner-b" })
	registerTestJob(t, first, "job", "session")

	now := clock.Add(time.Minute)
	if claimed, err := first.runOnce(ctx, now); err != nil || claimed != 1 {
		t.Fatalf("first claim = %d, %v", claimed, err)
	}
	<-started
	now = clock.Add(time.Minute)
	if claimed, err := first.runOnce(ctx, now); err != nil || claimed != 0 {
		t.Fatalf("local overlap claim = %d, %v", claimed, err)
	}
	if claimed, err := second.runOnce(ctx, now); err != nil || claimed != 0 {
		t.Fatalf("remote overlap claim = %d, %v", claimed, err)
	}
	close(release)
	first.wg.Wait()
	second.wg.Wait()

	now = clock.Add(time.Minute)
	if claimed, err := second.runOnce(ctx, now); err != nil || claimed != 1 {
		t.Fatalf("post-completion claim = %d, %v", claimed, err)
	}
	trigger := <-started
	if !trigger.Coalesced {
		t.Fatalf("occurrence during the first run was not coalesced: %+v", trigger)
	}
	second.wg.Wait()
	if got := maximum.Load(); got != 1 {
		t.Fatalf("maximum concurrent executions = %d", got)
	}
}

func TestExecutionTimeoutPersistsFailureAndBackoff(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	scheduler := testScheduler(t, store, executorFunc(func(ctx context.Context, _ Trigger) error {
		<-ctx.Done()
		return ctx.Err()
	}), clock)
	registration := testRegistration("session")
	registration.ID = "job"
	registration.Timeout = 20 * time.Millisecond
	if _, err := scheduler.Register(ctx, registration); err != nil {
		t.Fatal(err)
	}
	now := clock.Add(time.Minute)
	if claimed, err := scheduler.runOnce(ctx, now); err != nil || claimed != 1 {
		t.Fatalf("claim = %d, %v", claimed, err)
	}
	done := make(chan struct{})
	go func() {
		scheduler.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed-out executor did not return")
	}
	job := store.job(t, "job")
	if job.Running() || job.ConsecutiveFailures != 1 || !strings.Contains(job.LastError, ErrExecutionTimeout.Error()) {
		t.Fatalf("timeout was not persisted: %+v", job)
	}
	if !job.RetryAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("retry time = %s; want %s", job.RetryAt, now.Add(time.Minute))
	}
}

func TestFailureBackoffAndCircuitWindow(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	var calls atomic.Int32
	scheduler := testScheduler(t, store, executorFunc(func(context.Context, Trigger) error {
		calls.Add(1)
		return errors.New("injected executor failure")
	}), clock, func(options *Options) {
		options.CircuitThreshold = 2
		options.CircuitCooldown = 10 * time.Minute
	})
	registerTestJob(t, scheduler, "job", "session")

	first := clock.Add(time.Minute)
	if claimed, err := scheduler.runOnce(ctx, first); err != nil || claimed != 1 {
		t.Fatalf("first failure claim = %d, %v", claimed, err)
	}
	scheduler.wg.Wait()
	job := store.job(t, "job")
	if !job.RetryAt.Equal(first.Add(time.Minute)) || !job.CircuitOpenUntil.IsZero() {
		t.Fatalf("first failure backoff = %+v", job)
	}
	if claimed, _ := scheduler.runOnce(ctx, first.Add(30*time.Second)); claimed != 0 {
		t.Fatal("backoff was ignored")
	}

	second := first.Add(time.Minute)
	clock.Set(second)
	if claimed, err := scheduler.runOnce(ctx, second); err != nil || claimed != 1 {
		t.Fatalf("second failure claim = %d, %v", claimed, err)
	}
	scheduler.wg.Wait()
	job = store.job(t, "job")
	if job.ConsecutiveFailures != 2 || !job.CircuitOpenUntil.Equal(second.Add(10*time.Minute)) || !job.RetryAt.Equal(job.CircuitOpenUntil) {
		t.Fatalf("circuit did not open: %+v", job)
	}
	if claimed, _ := scheduler.runOnce(ctx, second.Add(9*time.Minute)); claimed != 0 {
		t.Fatal("open circuit admitted an execution")
	}

	third := second.Add(10 * time.Minute)
	clock.Set(third)
	if claimed, err := scheduler.runOnce(ctx, third); err != nil || claimed != 1 {
		t.Fatalf("half-open claim = %d, %v", claimed, err)
	}
	scheduler.wg.Wait()
	if got := calls.Load(); got != 3 {
		t.Fatalf("execution calls = %d; want 3", got)
	}
}

func TestConcurrentClaimHasSingleWinner(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	var calls atomic.Int32
	executor := executorFunc(func(context.Context, Trigger) error {
		calls.Add(1)
		return nil
	})
	first := testScheduler(t, store, executor, clock, func(options *Options) { options.OwnerID = "owner-a" })
	second := testScheduler(t, store, executor, clock, func(options *Options) { options.OwnerID = "owner-b" })
	registerTestJob(t, first, "job", "session")
	now := clock.Add(time.Minute)

	var wait sync.WaitGroup
	wait.Add(2)
	var firstClaims, secondClaims int
	var firstErr, secondErr error
	go func() {
		defer wait.Done()
		firstClaims, firstErr = first.runOnce(ctx, now)
	}()
	go func() {
		defer wait.Done()
		secondClaims, secondErr = second.runOnce(ctx, now)
	}()
	wait.Wait()
	first.wg.Wait()
	second.wg.Wait()
	if firstErr != nil || secondErr != nil {
		t.Fatalf("racing claims failed: %v, %v", firstErr, secondErr)
	}
	if firstClaims+secondClaims != 1 || calls.Load() != 1 {
		t.Fatalf("claims = %d + %d, executions = %d", firstClaims, secondClaims, calls.Load())
	}
}
