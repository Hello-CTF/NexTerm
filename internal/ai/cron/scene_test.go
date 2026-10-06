package cron

import (
	"context"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
)

func TestSchedulerPassesModelProfileToExecutor(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	triggers := make(chan Trigger, 1)
	scheduler := testScheduler(t, store, executorFunc(func(_ context.Context, trigger Trigger) error {
		triggers <- trigger
		return nil
	}), clock)
	registration := testRegistration("session-a")
	registration.ID = "job-profile"
	registration.ModelProfileID = "profile-42"
	if _, err := scheduler.Register(ctx, registration); err != nil {
		t.Fatal(err)
	}
	stored, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].ModelProfileID != "profile-42" {
		t.Fatalf("stored jobs = %+v", stored)
	}
	claimed, err := scheduler.runOnce(ctx, clock.Now().Add(time.Minute))
	if err != nil || claimed != 1 {
		t.Fatalf("runOnce = %d, %v", claimed, err)
	}
	scheduler.wg.Wait()
	trigger := <-triggers
	if trigger.ModelProfileID != "profile-42" {
		t.Fatalf("trigger profile = %q, want profile-42", trigger.ModelProfileID)
	}
}

func TestAgentExecutorForwardsModelProfile(t *testing.T) {
	runner := &fakeAgentRunner{emit: func(stream agent.Stream) {
		_ = stream.Send(context.Background(), agent.Event{Type: "done", Answer: "done"})
	}}
	executor := &AgentExecutor{Runner: runner}
	trigger := testTrigger()
	trigger.ModelProfileID = "profile-42"
	if err := executor.Execute(context.Background(), trigger); err != nil {
		t.Fatalf("execute = %v", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.args) != 1 || runner.args[0].ModelProfileID != "profile-42" {
		t.Fatalf("chat args = %+v", runner.args)
	}
}
