package cron

import (
	"context"
	"testing"
	"time"
)

func TestRecoveryBatchDoesNotStarveAbandoned(t *testing.T) {
	storage := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	scheduler := testScheduler(t, storage, executorFunc(func(context.Context, Trigger) error { return nil }), clock, func(options *Options) { options.RecoveryBatchSize = 1 })
	now := clock.Now()
	for index, id := range []string{"local", "abandoned"} {
		registerTestJob(t, scheduler, id, "session")
		deadline := now.Add(time.Duration(index-2) * time.Minute)
		plantRunState(t, storage, id, RunState{ID: id + "-run", ScheduledFor: deadline.Add(-time.Minute), StartedAt: deadline.Add(-time.Minute), Deadline: deadline}, Lease{Owner: "dead-owner", ExpiresAt: deadline})
	}
	scheduler.mu.Lock()
	scheduler.local["local"] = "local-run"
	scheduler.mu.Unlock()
	for range 2 {
		if _, err := scheduler.runOnce(context.Background(), now); err != nil {
			t.Fatal(err)
		}
	}
	if job := storage.job(t, "abandoned"); job.Running() {
		t.Fatalf("abandoned run remains permanently hidden behind locally tracked earliest candidate: %+v", job.Run)
	}
}
