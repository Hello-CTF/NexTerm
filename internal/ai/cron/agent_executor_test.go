package cron

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
)

type fakeAgentRunner struct {
	mu         sync.Mutex
	args       []agent.ChatArgs
	unattended []bool
	canceled   []string
	emit       func(stream agent.Stream)
	startErr   error
}

func (f *fakeAgentRunner) Start(ctx context.Context, args agent.ChatArgs, factory agent.StreamFactory) (agent.StartResponse, error) {
	if f.startErr != nil {
		return agent.StartResponse{}, f.startErr
	}
	stream, err := factory(ctx, "", "run-1")
	if err != nil {
		return agent.StartResponse{}, err
	}
	f.mu.Lock()
	f.args = append(f.args, args)
	f.unattended = append(f.unattended, guard.UnattendedFrom(ctx))
	f.mu.Unlock()
	response := agent.StartResponse{JobID: "run-1", ConversationID: args.ConversationID}
	if f.emit != nil {
		go func() {
			f.emit(stream)
			_ = stream.Close()
		}()
	}
	return response, nil
}

func (f *fakeAgentRunner) Cancel(jobID string) error {
	f.mu.Lock()
	f.canceled = append(f.canceled, jobID)
	f.mu.Unlock()
	return nil
}

func (f *fakeAgentRunner) canceledJobs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.canceled...)
}

func testTrigger() Trigger {
	return Trigger{
		RunID: "run-1", JobID: "job-1", SessionID: "session-1", Name: "check",
		Prompt: "inspect the deployment", ScheduledFor: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestAgentExecutorRunsToDone(t *testing.T) {
	runner := &fakeAgentRunner{emit: func(stream agent.Stream) {
		_ = stream.Send(context.Background(), agent.Event{Type: "done", Answer: "all healthy"})
	}}
	executor := &AgentExecutor{Runner: runner, ScopeFor: func(trigger Trigger) tools.Scope {
		return tools.Scope{SessionID: trigger.SessionID, AssetID: "asset-1"}
	}}
	if err := executor.Execute(context.Background(), testTrigger()); err != nil {
		t.Fatalf("execute = %v; want success", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.args) != 1 {
		t.Fatalf("started %d runs; want 1", len(runner.args))
	}
	args := runner.args[0]
	if args.ConversationID != "session-1" || args.Message != "inspect the deployment" {
		t.Fatalf("chat args = %+v", args)
	}
	if args.Scope.AssetID != "asset-1" || args.Scope.SessionID != "session-1" {
		t.Fatalf("scope = %+v", args.Scope)
	}
	if !runner.unattended[0] {
		t.Fatal("run was not marked unattended")
	}
}

func TestAgentExecutorFailsWithHonestError(t *testing.T) {
	runner := &fakeAgentRunner{emit: func(stream agent.Stream) {
		_ = stream.Send(context.Background(), agent.Event{Type: "error", Message: "模型调用失败"})
	}}
	executor := &AgentExecutor{Runner: runner}
	err := executor.Execute(context.Background(), testTrigger())
	if err == nil || !strings.Contains(err.Error(), "模型调用失败") {
		t.Fatalf("execute error = %v; want the run's failure", err)
	}
}

func TestAgentExecutorCancelsOnHITLInterrupt(t *testing.T) {
	for _, event := range []agent.Event{
		{Type: "confirmRequired", Tool: "exec_commands", Reason: "写入或编辑文件"},
		{Type: "questionRequired", Tool: "ask_user"},
	} {
		runner := &fakeAgentRunner{emit: func(stream agent.Stream) {
			_ = stream.Send(context.Background(), event)
		}}
		executor := &AgentExecutor{Runner: runner}
		err := executor.Execute(context.Background(), testTrigger())
		if err == nil || !strings.Contains(err.Error(), event.Type) {
			t.Fatalf("execute error = %v; want the %s interrupt reported", err, event.Type)
		}
		if canceled := runner.canceledJobs(); len(canceled) != 1 || canceled[0] != "run-1" {
			t.Fatalf("canceled = %v; want the parked run canceled", canceled)
		}
	}
}

func TestAgentExecutorCancelOnContextDone(t *testing.T) {
	runner := &fakeAgentRunner{}
	executor := &AgentExecutor{Runner: runner}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- executor.Execute(ctx, testTrigger()) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("execute error = %v; want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("execute did not return after context cancellation")
	}
	if canceled := runner.canceledJobs(); len(canceled) != 1 || canceled[0] != "run-1" {
		t.Fatalf("canceled = %v; want the run canceled so side effects stop", canceled)
	}
}

func TestAgentExecutorClosedWithoutTerminalEvent(t *testing.T) {
	runner := &fakeAgentRunner{emit: func(agent.Stream) {}}
	executor := &AgentExecutor{Runner: runner}
	err := executor.Execute(context.Background(), testTrigger())
	if err == nil || !strings.Contains(err.Error(), "without a terminal event") {
		t.Fatalf("execute error = %v; want an explicit missing-outcome failure", err)
	}
}

func TestAgentExecutorRequiresRunner(t *testing.T) {
	executor := &AgentExecutor{}
	if err := executor.Execute(context.Background(), testTrigger()); err == nil {
		t.Fatal("execute without a runner reported success")
	}
}

func TestAgentExecutorPersistsHonestOutcomesDurably(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteStore(t)
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 30, 0, time.UTC))
	runner := &fakeAgentRunner{emit: func(stream agent.Stream) {
		_ = stream.Send(context.Background(), agent.Event{Type: "error", Message: "权限策略已拒绝: 无人值守执行无法人工确认"})
	}}
	scheduler := testScheduler(t, store, &AgentExecutor{Runner: runner}, clock)
	registerTestJob(t, scheduler, "job", "session")

	now := clock.Add(time.Minute)
	if claimed, err := scheduler.runOnce(ctx, now); err != nil || claimed != 1 {
		t.Fatalf("runOnce = %d, %v", claimed, err)
	}
	scheduler.wg.Wait()
	job := loadSQLiteJob(t, store, "job")
	if job.LastError == "" || !strings.Contains(job.LastError, "无人值守") || job.ConsecutiveFailures != 1 || job.RetryAt.IsZero() {
		t.Fatalf("guard denial was not persisted honestly: %+v", job)
	}
	if job.Running() {
		t.Fatal("finished run still holds the durable claim")
	}
	if got := len(runner.canceledJobs()); got != 0 {
		t.Fatalf("a denied run must not be canceled again: %d", got)
	}
}
