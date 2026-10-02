package cron

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegisterPersistsAndBoundsEachSession(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 30, 0, time.UTC))
	scheduler := testScheduler(t, store, ExecutorFunc(func(context.Context, Trigger) error { return nil }), clock, func(options *Options) {
		options.MaxJobsPerSession = 2
	})
	registerTestJob(t, scheduler, "a-1", "session-a")
	registerTestJob(t, scheduler, "a-2", "session-a")
	registration := testRegistration("session-a")
	registration.ID = "a-3"
	if _, err := scheduler.Register(ctx, registration); !errors.Is(err, ErrJobLimit) || !errors.Is(err, ErrStorage) {
		t.Fatalf("third registration error = %v", err)
	}
	registerTestJob(t, scheduler, "b-1", "session-b")
	if got := store.count(); got != 3 {
		t.Fatalf("stored jobs = %d; want 3", got)
	}
	job := store.job(t, "a-1")
	if job.Revision != 1 || !job.Enabled || !job.NextRunAt.Equal(clock.Now().Add(30*time.Second)) {
		t.Fatalf("unexpected durable registration: %+v", job)
	}
}

func TestSessionReadsNeverDeleteOtherJobs(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	scheduler := testScheduler(t, store, ExecutorFunc(func(context.Context, Trigger) error { return nil }), clock)
	registerTestJob(t, scheduler, "a", "session-a")
	registerTestJob(t, scheduler, "b", "session-b")

	for _, sessionID := range []string{"session-a", "session-b", "session-a"} {
		jobs, err := scheduler.List(ctx, sessionID)
		if err != nil || len(jobs) != 1 || jobs[0].SessionID != sessionID {
			t.Fatalf("List(%s) = %+v, %v", sessionID, jobs, err)
		}
	}
	if _, err := scheduler.Get(ctx, "session-b", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-session Get error = %v", err)
	}
	if got := store.count(); got != 2 {
		t.Fatalf("session reads deleted jobs: %d", got)
	}
	if err := scheduler.Unregister(ctx, "session-b", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-session delete error = %v", err)
	}
	if err := scheduler.Unregister(ctx, "session-a", "a"); err != nil {
		t.Fatal(err)
	}
	if got := store.count(); got != 1 {
		t.Fatalf("explicit delete removed the wrong jobs: %d", got)
	}
}

func TestMissedOccurrencesCoalesceAndCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	start := time.Date(2026, time.January, 1, 0, 0, 30, 0, time.UTC)
	clock := newFakeClock(start)
	triggers := make(chan Trigger, 2)
	scheduler := testScheduler(t, store, ExecutorFunc(func(_ context.Context, trigger Trigger) error {
		triggers <- trigger
		return nil
	}), clock)
	registerTestJob(t, scheduler, "job", "session")

	now := start.Add(3 * time.Minute)
	clock.Set(now)
	started, err := scheduler.runOnce(ctx, now)
	if err != nil || started != 1 {
		t.Fatalf("runOnce = %d, %v", started, err)
	}
	scheduler.wg.Wait()
	trigger := <-triggers
	if !trigger.ScheduledFor.Equal(start.Add(30*time.Second)) || !trigger.Coalesced {
		t.Fatalf("missed occurrences were not coalesced: %+v", trigger)
	}
	job := store.job(t, "job")
	if !job.NextRunAt.Equal(start.Add(3*time.Minute+30*time.Second)) || job.Running() || job.LastError != "" || !job.LastCoalesced {
		t.Fatalf("unexpected post-run checkpoint: %+v", job)
	}
	started, err = scheduler.runOnce(ctx, now)
	if err != nil || started != 0 {
		t.Fatalf("same occurrence ran twice: %d, %v", started, err)
	}
}

func TestDisableAndReenableSkipsDisabledOccurrences(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	calls := 0
	scheduler := testScheduler(t, store, ExecutorFunc(func(context.Context, Trigger) error {
		calls++
		return nil
	}), clock)
	registerTestJob(t, scheduler, "job", "session")
	if _, err := scheduler.SetEnabled(ctx, "session", "job", false); err != nil {
		t.Fatal(err)
	}
	now := clock.Add(5 * time.Minute)
	if started, err := scheduler.runOnce(ctx, now); err != nil || started != 0 {
		t.Fatalf("disabled job started: %d, %v", started, err)
	}
	if _, err := scheduler.SetEnabled(ctx, "session", "job", true); err != nil {
		t.Fatal(err)
	}
	if started, err := scheduler.runOnce(ctx, now); err != nil || started != 0 || calls != 0 {
		t.Fatalf("disabled occurrences were replayed: %d, %v, calls=%d", started, err, calls)
	}
	now = clock.Add(time.Minute)
	if started, err := scheduler.runOnce(ctx, now); err != nil || started != 1 {
		t.Fatalf("reenabled job did not start: %d, %v", started, err)
	}
	scheduler.wg.Wait()
}
