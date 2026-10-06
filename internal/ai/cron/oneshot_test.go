package cron

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOneShotScheduleParsesAndExpires(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.October, 6, 9, 30, 0, 0, time.UTC)
	schedule, err := ParseSchedule(AtExpression(at), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !OneShot(schedule.expression) || !schedule.oneShot {
		t.Fatal("at expression not recognized as one-shot")
	}
	if next, ok := schedule.Next(at.Add(-time.Hour)); !ok || !next.Equal(at) {
		t.Fatalf("Next(before) = %v, %v", next, ok)
	}
	if _, ok := schedule.Next(at); ok {
		t.Fatal("one-shot schedule offered an occurrence at its own fire time")
	}
	if _, ok := schedule.Next(at.Add(time.Minute)); ok {
		t.Fatal("one-shot schedule offered an occurrence after firing")
	}
	if _, err := ParseSchedule("@at 不是时间", time.UTC); !errors.Is(err, ErrInvalidSchedule) {
		t.Fatalf("invalid at expression error = %v", err)
	}
	if OneShot("* * * * *") || OneShot("@every 5m") {
		t.Fatal("recurring expression misclassified as one-shot")
	}
}

func TestOneShotJobDisablesAfterDelivery(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	start := time.Date(2026, time.October, 5, 12, 0, 0, 0, time.UTC)
	clock := newFakeClock(start)
	triggers := make(chan Trigger, 2)
	scheduler := testScheduler(t, store, executorFunc(func(_ context.Context, trigger Trigger) error {
		triggers <- trigger
		return nil
	}), clock)
	at := start.Add(10 * time.Minute)
	job, err := scheduler.Register(ctx, Registration{SessionID: "session", Name: "reminder", Prompt: "检查备份", Schedule: AtExpression(at)})
	if err != nil {
		t.Fatal(err)
	}
	if !job.NextRunAt.Equal(at) || !job.Enabled {
		t.Fatalf("registered one-shot = %+v", job)
	}

	now := at.Add(time.Second)
	clock.Set(now)
	started, err := scheduler.runOnce(ctx, now)
	if err != nil || started != 1 {
		t.Fatalf("runOnce = %d, %v", started, err)
	}
	scheduler.wg.Wait()
	trigger := <-triggers
	if trigger.Prompt != "检查备份" || !trigger.ScheduledFor.Equal(at) {
		t.Fatalf("trigger = %+v", trigger)
	}
	delivered := store.job(t, job.ID)
	if delivered.Enabled || delivered.Running() || delivered.LastError != "" || !delivered.LastRunAt.Equal(now) {
		t.Fatalf("one-shot not finalized after delivery: %+v", delivered)
	}
	if !delivered.NextRunAt.IsZero() {
		t.Fatalf("delivered one-shot kept a next run: %+v", delivered)
	}

	started, err = scheduler.runOnce(ctx, now.Add(time.Hour))
	if err != nil || started != 0 {
		t.Fatalf("delivered one-shot fired again: %d, %v", started, err)
	}
}

func TestOneShotJobRetriesFailureThenDisables(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	start := time.Date(2026, time.October, 5, 12, 0, 0, 0, time.UTC)
	clock := newFakeClock(start)
	var attempts int
	scheduler := testScheduler(t, store, executorFunc(func(context.Context, Trigger) error {
		attempts++
		if attempts == 1 {
			return errors.New("投递失败")
		}
		return nil
	}), clock)
	at := start.Add(10 * time.Minute)
	job, err := scheduler.Register(ctx, Registration{SessionID: "session", Prompt: "提醒", Schedule: AtExpression(at)})
	if err != nil {
		t.Fatal(err)
	}

	now := at.Add(time.Second)
	clock.Set(now)
	if started, err := scheduler.runOnce(ctx, now); err != nil || started != 1 {
		t.Fatalf("first runOnce = %d, %v", started, err)
	}
	scheduler.wg.Wait()
	failed := store.job(t, job.ID)
	if !failed.Enabled || failed.LastError == "" || failed.RetryAt.IsZero() {
		t.Fatalf("failed one-shot lost retry state: %+v", failed)
	}

	retryAt := failed.RetryAt
	clock.Set(retryAt)
	if started, err := scheduler.runOnce(ctx, retryAt); err != nil || started != 1 {
		t.Fatalf("retry runOnce = %d, %v", started, err)
	}
	scheduler.wg.Wait()
	if attempts != 2 {
		t.Fatalf("attempts = %d", attempts)
	}
	delivered := store.job(t, job.ID)
	if delivered.Enabled || delivered.LastError != "" || delivered.ConsecutiveFailures != 0 {
		t.Fatalf("one-shot not disabled after successful retry: %+v", delivered)
	}
}

func TestOneShotRegistrationRejectsPastTime(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.October, 5, 12, 0, 0, 0, time.UTC))
	scheduler := testScheduler(t, store, executorFunc(func(context.Context, Trigger) error { return nil }), clock)
	_, err := scheduler.Register(context.Background(), Registration{
		SessionID: "session", Prompt: "过期提醒", Schedule: AtExpression(clock.Now().Add(-time.Minute)),
	})
	if err == nil || !errors.Is(err, ErrInvalidSchedule) {
		t.Fatalf("past one-shot registration error = %v", err)
	}
	if !strings.Contains(err.Error(), "no occurrence") {
		t.Fatalf("unexpected error text: %v", err)
	}
}
