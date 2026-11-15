package cron

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
)

func TestAgentExecutorReportsBusyConversationAsSkip(t *testing.T) {
	executor := &AgentExecutor{Runner: &fakeAgentRunner{startErr: agent.ErrConversationRunBusy}}
	err := executor.Execute(context.Background(), testTrigger())
	if !errors.Is(err, ErrRunSkipped) {
		t.Fatalf("busy conversation = %v, want ErrRunSkipped", err)
	}
}

func TestSkippedRecurringRunIsRecordedWithoutFailure(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	notified := make(chan error, 1)
	scheduler := testScheduler(t, store, &AgentExecutor{Runner: &fakeAgentRunner{startErr: agent.ErrConversationRunBusy}}, clock, func(options *Options) {
		options.OnError = func(err error) {
			select {
			case notified <- err:
			default:
			}
		}
	})
	registerTestJob(t, scheduler, "job", "session")
	now := clock.Add(time.Minute)
	if claimed, err := scheduler.runOnce(ctx, now); err != nil || claimed != 1 {
		t.Fatalf("runOnce = %d, %v", claimed, err)
	}
	scheduler.wg.Wait()
	job := store.job(t, "job")
	if job.Running() || job.ConsecutiveFailures != 0 || !job.RetryAt.IsZero() || !strings.Contains(job.LastError, "已跳过") {
		t.Fatalf("skipped run state = %+v", job)
	}
	select {
	case err := <-notified:
		if !errors.Is(err, ErrRunSkipped) {
			t.Fatalf("notification = %v", err)
		}
	default:
		t.Fatal("skipped run was not reported")
	}
}

func TestSkippedOneShotRetriesLater(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	clock := newFakeClock(start)
	scheduler := testScheduler(t, store, &AgentExecutor{Runner: &fakeAgentRunner{startErr: agent.ErrConversationRunBusy}}, clock)
	at := start.Add(10 * time.Minute)
	job, err := scheduler.Register(ctx, Registration{SessionID: "session", Prompt: "提醒", Schedule: AtExpression(at)})
	if err != nil {
		t.Fatal(err)
	}
	now := at.Add(time.Second)
	clock.Set(now)
	if started, err := scheduler.runOnce(ctx, now); err != nil || started != 1 {
		t.Fatalf("runOnce = %d, %v", started, err)
	}
	scheduler.wg.Wait()
	skipped := store.job(t, job.ID)
	if !skipped.Enabled || skipped.ConsecutiveFailures != 0 || skipped.RetryAt.IsZero() || !strings.Contains(skipped.LastError, "已跳过") {
		t.Fatalf("skipped one-shot state = %+v", skipped)
	}
}
