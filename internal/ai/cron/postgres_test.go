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

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
)

func openPostgresStore(t *testing.T) *SQLStore {
	t.Helper()
	fixture := dbtest.NewFixture(t)
	backend := fixture.OpenStore(t)
	store, err := NewPostgresStore(context.Background(), backend.DB())
	if err != nil {
		t.Fatalf("open postgres cron store: %v", err)
	}
	return store
}

func TestPostgresStoreStartupProbe(t *testing.T) {
	fixture := dbtest.NewFixture(t)
	backend := fixture.OpenStore(t)
	if _, err := NewPostgresStore(context.Background(), backend.DB()); err != nil {
		t.Fatalf("startup probe on migrated schema: %v", err)
	}
}

func TestPostgresStoreStartupProbeFailsWithoutCronTable(t *testing.T) {
	fixture := dbtest.NewFixture(t)
	raw := fixture.OpenRaw(t)
	if _, err := NewPostgresStore(context.Background(), raw); err == nil {
		t.Fatal("startup probe succeeded without cron_job table")
	}
}

func TestPostgresStoreRoundTripPreservesEveryField(t *testing.T) {
	ctx := context.Background()
	store := openPostgresStore(t)
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	job := fullTestJob("job-1", "session-a", now)
	created, err := store.Create(ctx, job, 4)
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 {
		t.Fatalf("created revision = %d; want 1", created.Revision)
	}
	jobs, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || !jobsEqual(jobs[0], job) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", jobs, job)
	}
	stored, err := store.CompareAndSwap(ctx, jobs[0], jobs[0].Revision)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != 2 {
		t.Fatalf("cas revision = %d; want 2", stored.Revision)
	}
	reloaded, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded) != 1 || reloaded[0].Revision != 2 || !jobsEqual(reloaded[0], stored) {
		t.Fatalf("cas not durable: %+v", reloaded)
	}
}

func TestPostgresStoreCreateEnforcesBoundAndUniqueness(t *testing.T) {
	ctx := context.Background()
	store := openPostgresStore(t)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"a-1", "a-2"} {
		if _, err := store.Create(ctx, fullTestJob(id, "session-a", now), 2); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Create(ctx, fullTestJob("a-3", "session-a", now), 2); !errors.Is(err, ErrJobLimit) {
		t.Fatalf("bound error = %v; want ErrJobLimit", err)
	}
	if _, err := store.Create(ctx, fullTestJob("a-1", "session-b", now), 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate error = %v; want ErrConflict", err)
	}
}

func TestPostgresStoreCompareAndSwapGuards(t *testing.T) {
	ctx := context.Background()
	store := openPostgresStore(t)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	created, err := store.Create(ctx, fullTestJob("job", "session-a", now), 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompareAndSwap(ctx, created, 99); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision error = %v; want ErrConflict", err)
	}
	foreign := created
	foreign.SessionID = "session-b"
	if _, err := store.CompareAndSwap(ctx, foreign, created.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign session error = %v; want ErrConflict", err)
	}
	missing := created
	missing.ID = "missing"
	if _, err := store.CompareAndSwap(ctx, missing, created.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing error = %v; want ErrNotFound", err)
	}
}

func TestPostgresStoreDeleteGuardsOtherSessions(t *testing.T) {
	ctx := context.Background()
	store := openPostgresStore(t)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	a, err := store.Create(ctx, fullTestJob("a", "session-a", now), 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, fullTestJob("b", "session-b", now), 4); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "session-b", "a", a.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-session delete error = %v", err)
	}
	if err := store.Delete(ctx, "session-a", "a", 99); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale delete error = %v; want ErrConflict", err)
	}
	if err := store.Delete(ctx, "session-a", "a", a.Revision); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "session-a", "a", a.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeat delete error = %v; want ErrNotFound", err)
	}
}

func TestPostgresStoreConcurrentCompareAndSwapHasSingleWinner(t *testing.T) {
	ctx := context.Background()
	store := openPostgresStore(t)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	created, err := store.Create(ctx, fullTestJob("job", "session-a", now), 4)
	if err != nil {
		t.Fatal(err)
	}
	var won atomic.Int32
	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			candidate := created
			candidate.LastError = fmt.Sprintf("writer-%d", i)
			if _, err := store.CompareAndSwap(ctx, candidate, created.Revision); err == nil {
				won.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("cas %d: %v", i, err)
			}
		}(i)
	}
	wait.Wait()
	if won.Load() != 1 {
		t.Fatalf("winners = %d; want 1", won.Load())
	}
	stored := loadStoredJob(t, store, "job")
	if stored.Revision != 2 {
		t.Fatalf("final revision = %d; want 2", stored.Revision)
	}
}

func TestPostgresSchedulerClaimCompleteAndReschedule(t *testing.T) {
	ctx := context.Background()
	store := openPostgresStore(t)
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	triggers := make(chan Trigger, 1)
	scheduler := testScheduler(t, store, executorFunc(func(_ context.Context, trigger Trigger) error {
		triggers <- trigger
		return nil
	}), clock)
	registration := testRegistration("session")
	registration.ID = "job"
	registration.ModelProfileID = "profile-pg"
	job, err := scheduler.Register(ctx, registration)
	if err != nil {
		t.Fatal(err)
	}

	due := clock.Add(time.Minute)
	claimed, err := scheduler.runOnce(ctx, due)
	if err != nil || claimed != 1 {
		t.Fatalf("claim runOnce = %d, %v", claimed, err)
	}
	scheduler.wg.Wait()
	trigger := <-triggers
	if trigger.RunID == "" || trigger.JobID != "job" || trigger.ModelProfileID != "profile-pg" || !trigger.ScheduledFor.Equal(job.NextRunAt) {
		t.Fatalf("trigger = %+v", trigger)
	}
	stored := loadStoredJob(t, store, "job")
	if stored.Running() || stored.Lease.Owner != "" || !stored.Lease.ExpiresAt.IsZero() || stored.Run.ID != "" {
		t.Fatalf("completed run kept durable run state: %+v", stored)
	}
	if !stored.LastRunAt.Equal(due) || stored.LastError != "" || stored.ConsecutiveFailures != 0 {
		t.Fatalf("completion not checkpointed: %+v", stored)
	}
	if !stored.NextRunAt.Equal(due.Add(time.Minute)) {
		t.Fatalf("next run = %s; want %s", stored.NextRunAt, due.Add(time.Minute))
	}
	if claimed, err := scheduler.runOnce(ctx, due); err != nil || claimed != 0 {
		t.Fatalf("same occurrence ran twice: %d, %v", claimed, err)
	}
}

func TestPostgresSchedulerFailureSchedulesRetry(t *testing.T) {
	ctx := context.Background()
	store := openPostgresStore(t)
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	scheduler := testScheduler(t, store, executorFunc(func(context.Context, Trigger) error {
		return errors.New("injected executor failure")
	}), clock)
	registerTestJob(t, scheduler, "job", "session")

	due := clock.Add(time.Minute)
	claimed, err := scheduler.runOnce(ctx, due)
	if err != nil || claimed != 1 {
		t.Fatalf("claim runOnce = %d, %v", claimed, err)
	}
	scheduler.wg.Wait()
	stored := loadStoredJob(t, store, "job")
	if stored.Running() || stored.Run.ID != "" || stored.Lease.Owner != "" {
		t.Fatalf("failed run kept durable run state: %+v", stored)
	}
	if stored.ConsecutiveFailures != 1 || !strings.Contains(stored.LastError, "injected executor failure") {
		t.Fatalf("failure not checkpointed: %+v", stored)
	}
	if !stored.RetryAt.Equal(due.Add(time.Minute)) {
		t.Fatalf("retry at = %s; want %s", stored.RetryAt, due.Add(time.Minute))
	}
	if claimed, err := scheduler.runOnce(ctx, due.Add(30*time.Second)); err != nil || claimed != 0 {
		t.Fatalf("backoff was ignored: %d, %v", claimed, err)
	}
}

func TestPostgresSchedulerConcurrentClaimHasSingleWinner(t *testing.T) {
	ctx := context.Background()
	store := openPostgresStore(t)
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
	stored := loadStoredJob(t, store, "job")
	if stored.Running() || stored.LastRunAt.IsZero() || stored.LastError != "" || stored.Revision != 3 {
		t.Fatalf("claim race left inconsistent durable state: %+v", stored)
	}
}
