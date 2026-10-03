package cron

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRestartRecoversAbandonedRunWithoutReexecutingIt(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	var calls atomic.Int32
	executor := ExecutorFunc(func(context.Context, Trigger) error {
		calls.Add(1)
		return nil
	})
	old := testScheduler(t, store, executor, clock, func(options *Options) { options.OwnerID = "old-owner" })
	registerTestJob(t, old, "job", "session")

	claimedAt := clock.Add(time.Minute)
	job := store.job(t, "job")
	job.Lease = Lease{Owner: "old-owner", ExpiresAt: claimedAt.Add(5 * time.Second)}
	job.Run = RunState{
		ID: "abandoned-run", ScheduledFor: claimedAt, StartedAt: claimedAt,
		Deadline: claimedAt.Add(time.Minute),
	}
	job.NextRunAt = claimedAt.Add(time.Minute)
	if _, err := store.CompareAndSwap(ctx, job, job.Revision); err != nil {
		t.Fatal(err)
	}

	restarted := testScheduler(t, store, executor, clock, func(options *Options) { options.OwnerID = "new-owner" })
	if claimed, err := restarted.runOnce(ctx, claimedAt.Add(10*time.Second)); err != nil || claimed != 0 {
		t.Fatalf("run before durable deadline = %d, %v", claimed, err)
	}
	if !store.job(t, "job").Running() || calls.Load() != 0 {
		t.Fatal("expired lease was treated as permission to overlap an active run")
	}

	recoveredAt := claimedAt.Add(time.Minute)
	clock.Set(recoveredAt)
	if claimed, err := restarted.runOnce(ctx, recoveredAt); err != nil || claimed != 0 {
		t.Fatalf("recovery executed the abandoned occurrence: %d, %v", claimed, err)
	}
	job = store.job(t, "job")
	if job.Running() || job.ConsecutiveFailures != 1 || !strings.Contains(job.LastError, "interrupted") || !job.LastScheduledFor.Equal(claimedAt) {
		t.Fatalf("abandoned run was not reconciled: %+v", job)
	}
	if calls.Load() != 0 {
		t.Fatal("abandoned occurrence was reexecuted")
	}

	next := recoveredAt.Add(time.Minute)
	clock.Set(next)
	if claimed, err := restarted.runOnce(ctx, next); err != nil || claimed != 1 {
		t.Fatalf("subsequent occurrence did not recover: %d, %v", claimed, err)
	}
	restarted.wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("subsequent executions = %d; want 1", calls.Load())
	}
}

func TestRestartLoadsMissedDurableJob(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	old := testScheduler(t, store, ExecutorFunc(func(context.Context, Trigger) error { return nil }), clock)
	registerTestJob(t, old, "durable", "session")

	triggers := make(chan Trigger, 1)
	restarted := testScheduler(t, store, ExecutorFunc(func(_ context.Context, trigger Trigger) error {
		triggers <- trigger
		return nil
	}), clock, func(options *Options) { options.OwnerID = "new-owner" })
	now := clock.Add(4 * time.Minute)
	if claimed, err := restarted.runOnce(ctx, now); err != nil || claimed != 1 {
		t.Fatalf("restart claim = %d, %v", claimed, err)
	}
	restarted.wg.Wait()
	trigger := <-triggers
	if trigger.JobID != "durable" || !trigger.Coalesced {
		t.Fatalf("restart did not recover missed occurrences: %+v", trigger)
	}
}
