package cron

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegistrationAndReadStorageFailuresAreExplicit(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	scheduler := testScheduler(t, store, executorFunc(func(context.Context, Trigger) error { return nil }), clock)
	cause := errors.New("injected durable write failure")
	store.fail("create", cause)
	registration := testRegistration("session")
	registration.ID = "job"
	if _, err := scheduler.Register(ctx, registration); !errors.Is(err, ErrStorage) || !errors.Is(err, cause) {
		t.Fatalf("create failure = %v", err)
	}
	var storageErr *StorageError
	if _, err := scheduler.Register(ctx, registration); !errors.As(err, &storageErr) || storageErr.Op != "create" {
		t.Fatalf("create failure lacks operation: %v", err)
	}
	if store.count() != 0 {
		t.Fatal("failed create changed the store")
	}
	store.heal("create")
	registerTestJob(t, scheduler, "job", "session")
	store.fail("list", cause)
	if _, err := scheduler.List(ctx, "session"); !errors.Is(err, ErrStorage) || !errors.Is(err, cause) {
		t.Fatalf("list failure = %v", err)
	}
	if _, err := scheduler.runOnce(ctx, clock.Now()); !errors.Is(err, ErrStorage) || !errors.Is(err, cause) {
		t.Fatalf("scheduler list failure = %v", err)
	}
}

func TestClaimPersistenceFailureDoesNotExecute(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	calls := 0
	scheduler := testScheduler(t, store, executorFunc(func(context.Context, Trigger) error {
		calls++
		return nil
	}), clock)
	registerTestJob(t, scheduler, "job", "session")
	cause := errors.New("injected CAS failure")
	store.fail("cas", cause)
	now := clock.Add(time.Minute)
	if _, err := scheduler.runOnce(ctx, now); !errors.Is(err, ErrStorage) || !errors.Is(err, cause) {
		t.Fatalf("claim failure = %v", err)
	}
	if calls != 0 || store.job(t, "job").Running() {
		t.Fatal("job executed without a durable claim")
	}
}

func TestRunReturnsCompletionPersistenceFailure(t *testing.T) {
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	cause := errors.New("injected completion write failure")
	notified := make(chan error, 1)
	scheduler := testScheduler(t, store, executorFunc(func(context.Context, Trigger) error {
		store.fail("cas", cause)
		return nil
	}), clock, func(options *Options) {
		options.OnError = func(err error) { notified <- err }
	})
	registerTestJob(t, scheduler, "job", "session")
	clock.Add(time.Minute)
	if err := scheduler.Run(context.Background()); !errors.Is(err, ErrStorage) || !errors.Is(err, cause) {
		t.Fatalf("Run error = %v", err)
	}
	select {
	case err := <-notified:
		if !errors.Is(err, cause) {
			t.Fatalf("notification = %v", err)
		}
	default:
		t.Fatal("storage failure was not reported")
	}
	job := store.job(t, "job")
	if !job.Running() || !job.LastRunAt.IsZero() {
		t.Fatalf("failed completion was recorded as successful: %+v", job)
	}
}

func TestRunReturnsLeaseRenewalFailureAndCancelsExecution(t *testing.T) {
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	cause := errors.New("injected lease renewal failure")
	executorReturned := make(chan struct{})
	scheduler := testScheduler(t, store, executorFunc(func(ctx context.Context, _ Trigger) error {
		store.fail("cas", cause)
		<-ctx.Done()
		close(executorReturned)
		return ctx.Err()
	}), clock, func(options *Options) {
		options.LeaseDuration = 30 * time.Millisecond
	})
	registerTestJob(t, scheduler, "job", "session")
	clock.Add(time.Minute)
	if err := scheduler.Run(context.Background()); !errors.Is(err, ErrStorage) || !errors.Is(err, cause) {
		t.Fatalf("Run renewal error = %v", err)
	}
	select {
	case <-executorReturned:
	default:
		t.Fatal("renewal failure did not cancel the executor")
	}
	if !store.job(t, "job").Running() {
		t.Fatal("uncertain execution was silently completed")
	}
}

func TestRunCancellationDoesNotDeadlock(t *testing.T) {
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	scheduler := testScheduler(t, store, executorFunc(func(context.Context, Trigger) error { return nil }), clock)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run cancellation error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}
