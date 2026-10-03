//go:build !cgo

package cron

import (
	"context"
	"testing"
	"time"
)

func TestZeroCGOSchedulerEndToEnd(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	executed := make(chan string, 1)
	scheduler := testScheduler(t, store, ExecutorFunc(func(_ context.Context, trigger Trigger) error {
		executed <- trigger.JobID
		return nil
	}), clock)
	registerTestJob(t, scheduler, "zero-cgo-job", "session")
	now := clock.Add(time.Minute)
	if claimed, err := scheduler.runOnce(ctx, now); err != nil || claimed != 1 {
		t.Fatalf("runOnce = %d, %v", claimed, err)
	}
	scheduler.wg.Wait()
	if id := <-executed; id != "zero-cgo-job" {
		t.Fatalf("executed job = %q", id)
	}
	restarted := testScheduler(t, store, ExecutorFunc(func(context.Context, Trigger) error { return nil }), clock)
	job, err := restarted.Get(ctx, "session", "zero-cgo-job")
	if err != nil || job.Running() || job.LastRunAt.IsZero() {
		t.Fatalf("durable job after restart = %+v, %v", job, err)
	}
}
