package cron

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCorruptScheduleDisablesOnlyThatJob(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	executed := make(chan string, 4)
	reported := make(chan error, 4)
	scheduler := testScheduler(t, store, executorFunc(func(_ context.Context, trigger Trigger) error {
		executed <- trigger.JobID
		return nil
	}), clock, func(o *Options) {
		o.OnError = func(err error) { reported <- err }
	})
	registerTestJob(t, scheduler, "bad", "session")
	registerTestJob(t, scheduler, "good", "session")
	store.mu.Lock()
	bad := store.jobs["bad"]
	bad.Timezone = "Not/AZone"
	store.jobs["bad"] = bad
	store.mu.Unlock()
	clock.Add(2 * time.Minute)

	if _, err := scheduler.runOnce(ctx, clock.Now()); err != nil {
		t.Fatalf("runOnce failed on one corrupt job: %v", err)
	}
	select {
	case id := <-executed:
		if id != "good" {
			t.Fatalf("executed %q", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("healthy job was not claimed")
	}
	store.mu.Lock()
	got := store.jobs["bad"]
	store.mu.Unlock()
	if got.Enabled || got.LastError == "" {
		t.Fatalf("corrupt job not disabled visibly: %+v", got)
	}
	select {
	case err := <-reported:
		if err == nil || !strings.Contains(err.Error(), "bad") {
			t.Fatalf("reported error does not name the job: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("disabling the job was not reported through OnError")
	}
}
