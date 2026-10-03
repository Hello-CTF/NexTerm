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

func TestRegistrationSkipsSpringForwardGapWithTimeout(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.March, 7, 2, 30, 0, 0, location))
	scheduler := testScheduler(t, store, ExecutorFunc(func(context.Context, Trigger) error { return nil }), clock)
	registration := testRegistration("session")
	registration.ID = "spring-registration"
	registration.Schedule = "30 2 * * *"
	registration.Timezone = location.String()
	type result struct {
		job Job
		err error
	}
	results := make(chan result, 1)
	go func() {
		job, err := scheduler.Register(context.Background(), registration)
		results <- result{job: job, err: err}
	}()
	select {
	case result := <-results:
		if result.err != nil {
			t.Fatal(result.err)
		}
		want := time.Date(2026, time.March, 9, 2, 30, 0, 0, location)
		if !result.job.NextRunAt.Equal(want) {
			t.Fatalf("NextRunAt = %s; want %s", result.job.NextRunAt, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("registration did not terminate at the spring-forward gap")
	}
}

func TestRegistrationBoundsCalendarSearchWithTimeout(t *testing.T) {
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	scheduler := testScheduler(t, store, ExecutorFunc(func(context.Context, Trigger) error { return nil }), clock)
	registration := testRegistration("session")
	registration.ID = "impossible-calendar-date"
	registration.Schedule = "0 0 30 2 *"
	results := make(chan error, 1)
	go func() {
		_, err := scheduler.Register(context.Background(), registration)
		results <- err
	}()
	select {
	case err := <-results:
		if !errors.Is(err, ErrInvalidSchedule) {
			t.Fatalf("Register() error = %v; want ErrInvalidSchedule", err)
		}
		if store.count() != 0 {
			t.Fatal("bounded schedule failure persisted a job")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("calendar search did not terminate at its bound")
	}
}

func TestPollingCheckpointSkipsSpringForwardGapWithTimeout(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.March, 6, 2, 30, 0, 0, location))
	scheduler := testScheduler(t, store, ExecutorFunc(func(context.Context, Trigger) error { return nil }), clock)
	registration := testRegistration("session")
	registration.ID = "spring-polling"
	registration.Schedule = "30 2 * * *"
	registration.Timezone = location.String()
	if _, err := scheduler.Register(context.Background(), registration); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.March, 7, 2, 30, 0, 0, location)
	clock.Set(now)
	claimed, err := runOnceWithTimeout(t, scheduler, now)
	if err != nil || claimed != 1 {
		t.Fatalf("runOnce = %d, %v", claimed, err)
	}
	waitSchedulerWithTimeout(t, scheduler)
	want := time.Date(2026, time.March, 9, 2, 30, 0, 0, location)
	if got := store.job(t, registration.ID).NextRunAt; !got.Equal(want) {
		t.Fatalf("NextRunAt = %s; want %s", got, want)
	}
}

func TestFallBackPollingDoesNotDuplicateExecution(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.October, 31, 1, 30, 0, 0, location))
	calls := make(chan string, 2)
	scheduler := testScheduler(t, store, ExecutorFunc(func(_ context.Context, trigger Trigger) error {
		calls <- trigger.RunID
		return nil
	}), clock)
	registration := testRegistration("session")
	registration.ID = "fall-back"
	registration.Schedule = "30 1 * * *"
	registration.Timezone = location.String()
	if _, err := scheduler.Register(context.Background(), registration); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, time.November, 1, 5, 30, 0, 0, time.UTC)
	clock.Set(first)
	if claimed, err := runOnceWithTimeout(t, scheduler, first); err != nil || claimed != 1 {
		t.Fatalf("first runOnce = %d, %v", claimed, err)
	}
	waitSchedulerWithTimeout(t, scheduler)
	repeated := time.Date(2026, time.November, 1, 6, 30, 0, 0, time.UTC)
	clock.Set(repeated)
	if claimed, err := runOnceWithTimeout(t, scheduler, repeated); err != nil || claimed != 0 {
		t.Fatalf("repeated runOnce = %d, %v", claimed, err)
	}
	if len(calls) != 1 {
		t.Fatalf("ambiguous local occurrence executed %d times; want once", len(calls))
	}
	wantNext := time.Date(2026, time.November, 2, 6, 30, 0, 0, time.UTC)
	if got := store.job(t, registration.ID).NextRunAt; !got.Equal(wantNext) {
		t.Fatalf("NextRunAt = %s; want %s", got, wantNext)
	}
}

func runOnceWithTimeout(t *testing.T, scheduler *Scheduler, now time.Time) (int, error) {
	t.Helper()
	type result struct {
		claimed int
		err     error
	}
	results := make(chan result, 1)
	go func() {
		claimed, err := scheduler.runOnce(context.Background(), now)
		results <- result{claimed: claimed, err: err}
	}()
	select {
	case result := <-results:
		return result.claimed, result.err
	case <-time.After(3 * time.Second):
		t.Fatalf("scheduler polling did not terminate at %s", now)
		return 0, nil
	}
}

func waitSchedulerWithTimeout(t *testing.T, scheduler *Scheduler) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		scheduler.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler execution did not finish")
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
