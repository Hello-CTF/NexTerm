package cron

import (
	"context"
	"errors"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type ownerCapturingRunner struct {
	ownerID string
}

func (r *ownerCapturingRunner) Start(ctx context.Context, _ agent.ChatArgs, factory agent.StreamFactory) (agent.StartResponse, error) {
	r.ownerID = ipc.OwnerID(ctx)
	stream, err := factory(ctx, "", "owner-run")
	if err != nil {
		return agent.StartResponse{}, err
	}
	if err := stream.Send(ctx, agent.Event{Type: "done"}); err != nil {
		return agent.StartResponse{}, err
	}
	return agent.StartResponse{JobID: "owner-run"}, stream.Close()
}

func (r *ownerCapturingRunner) Cancel(string) error { return nil }

func TestAgentExecutorRestoresOwnerForScopeAndRunner(t *testing.T) {
	runner := &ownerCapturingRunner{}
	trigger := testTrigger()
	trigger.OwnerID = "user-a"
	trigger.OwnerKind = OwnerKindAccount
	scopeCalled := false
	executor := &AgentExecutor{Runner: runner, ScopeFor: func(ctx context.Context, got Trigger) (tools.Scope, error) {
		scopeCalled = true
		if ipc.OwnerID(ctx) != "user-a" || got.OwnerID != "user-a" || got.OwnerKind != OwnerKindAccount {
			t.Errorf("scope identity = %q, trigger = %+v", ipc.OwnerID(ctx), got)
		}
		return tools.Scope{SessionID: "terminal-session"}, nil
	}}
	if err := executor.Execute(context.Background(), trigger); err != nil {
		t.Fatal(err)
	}
	if !scopeCalled || runner.ownerID != "user-a" {
		t.Fatalf("scope called = %v, runner owner = %q", scopeCalled, runner.ownerID)
	}
}

func TestAgentExecutorScopeLookupIsCancelable(t *testing.T) {
	runner := &fakeAgentRunner{}
	started := make(chan struct{})
	executor := &AgentExecutor{Runner: runner, ScopeFor: func(ctx context.Context, _ Trigger) (tools.Scope, error) {
		close(started)
		<-ctx.Done()
		return tools.Scope{}, ctx.Err()
	}}
	trigger := testTrigger()
	trigger.OwnerID = "user-a"
	trigger.OwnerKind = OwnerKindAccount
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- executor.Execute(ctx, trigger) }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("scope cancellation error = %v", err)
	}
	if len(runner.args) != 0 {
		t.Fatal("runner started after scope cancellation")
	}
}

func TestAgentExecutorRejectsOwnerlessTrigger(t *testing.T) {
	runner := &fakeAgentRunner{}
	executor := &AgentExecutor{Runner: runner}
	trigger := testTrigger()
	trigger.OwnerKind = ""
	if err := executor.Execute(context.Background(), trigger); !errors.Is(err, ErrOwnerless) {
		t.Fatalf("ownerless execution error = %v", err)
	}
	if len(runner.args) != 0 {
		t.Fatal("ownerless run borrowed the open agent path")
	}
}
