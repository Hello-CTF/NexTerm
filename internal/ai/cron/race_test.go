package cron

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentRegistrationEnforcesSessionBound(t *testing.T) {
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	scheduler := testScheduler(t, store, ExecutorFunc(func(context.Context, Trigger) error { return nil }), clock, func(options *Options) {
		options.MaxJobsPerSession = 4
	})
	const registrations = 24
	var accepted atomic.Int32
	var wait sync.WaitGroup
	wait.Add(registrations)
	for index := 0; index < registrations; index++ {
		go func(index int) {
			defer wait.Done()
			registration := testRegistration("session")
			registration.ID = fmt.Sprintf("job-%02d", index)
			_, err := scheduler.Register(context.Background(), registration)
			switch {
			case err == nil:
				accepted.Add(1)
			case errors.Is(err, ErrJobLimit):
			default:
				t.Errorf("Register() error = %v", err)
			}
		}(index)
	}
	wait.Wait()
	if got := accepted.Load(); got != 4 || store.count() != 4 {
		t.Fatalf("accepted = %d, stored = %d; want 4", got, store.count())
	}
	jobs, err := scheduler.List(context.Background(), "session")
	if err != nil || len(jobs) != 4 {
		t.Fatalf("List() = %d jobs, %v", len(jobs), err)
	}
}
