package cron

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
)

// AgentRunner is the production agent runner surface the executor drives.
// *agent.Runner satisfies it; tests can substitute a fake.
type AgentRunner interface {
	Start(ctx context.Context, args agent.ChatArgs, factory agent.StreamFactory) (agent.StartResponse, error)
	Cancel(jobID string) error
}

// AgentExecutor executes cron triggers as unattended AI runs through the
// production agent runner. The run's context is marked with
// guard.WithUnattended so the composed permission provider hands out
// guard.Config{Mode: guard.Unattended}: NeedsConfirm and Forbidden rulings
// are denied at decision time instead of being executed silently or parked on
// a confirmation no one will answer. As defense in depth, any HITL interrupt
// that still reaches the stream (for example an ask_user question) cancels
// the run and fails the trigger with an explicit error — an unattended
// execution never waits for a human.
//
// The trigger's SessionID is used as the conversation ID: register jobs under
// the conversation that owns them. The execution outcome is exactly the
// scheduler's durable LastError/ConsecutiveFailures state; the adapter never
// touches job records, so other sessions' jobs are unaffected.
type AgentExecutor struct {
	Runner AgentRunner
	// ScopeFor optionally binds the run to a terminal/asset scope. Nil leaves
	// the scope empty.
	ScopeFor func(Trigger) tools.Scope
}

var _ Executor = (*AgentExecutor)(nil)

func (e *AgentExecutor) Execute(ctx context.Context, trigger Trigger) error {
	if e.Runner == nil {
		return errors.New("cron: agent executor has no runner")
	}
	stream := newTerminalStream()
	args := agent.ChatArgs{ConversationID: trigger.SessionID, Message: trigger.Prompt}
	if e.ScopeFor != nil {
		args.Scope = e.ScopeFor(trigger)
	}
	response, err := e.Runner.Start(guard.WithUnattended(ctx), args, agent.StaticStream(stream))
	if err != nil {
		return fmt.Errorf("cron: start unattended agent run: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			// Runner jobs outlive the starter's context by design, so an
			// explicit Cancel is the only way to stop the side effects.
			_ = e.Runner.Cancel(response.JobID)
			return ctx.Err()
		case <-stream.signal:
			if event, closed := stream.outcome(); event != nil {
				return e.terminalError(response.JobID, *event)
			} else if closed {
				return fmt.Errorf("cron: agent run %s ended without a terminal event", response.JobID)
			}
		}
	}
}

func (e *AgentExecutor) terminalError(jobID string, event agent.Event) error {
	switch event.Type {
	case "done":
		return nil
	case "error":
		if event.Message == "" {
			return fmt.Errorf("cron: agent run %s failed", jobID)
		}
		return fmt.Errorf("cron: agent run %s failed: %s", jobID, event.Message)
	default:
		// confirmRequired or questionRequired: unattended runs must not wait
		// for a human. Cancel the parked run and report the interrupt
		// honestly instead of letting it expire silently.
		_ = e.Runner.Cancel(jobID)
		return fmt.Errorf("cron: unattended agent run %s stopped at %s for tool %q: %s", jobID, event.Type, event.Tool, event.Reason)
	}
}

// terminalStream records the first terminal or interrupt event without ever
// blocking the runner's emit path.
type terminalStream struct {
	mu       sync.Mutex
	terminal *agent.Event
	closed   bool
	signal   chan struct{}
}

func newTerminalStream() *terminalStream {
	return &terminalStream{signal: make(chan struct{}, 1)}
}

func (s *terminalStream) Send(_ context.Context, event agent.Event) error {
	switch event.Type {
	case "done", "error", "confirmRequired", "questionRequired":
		s.mu.Lock()
		if s.terminal == nil {
			copy := event
			s.terminal = &copy
		}
		s.mu.Unlock()
		select {
		case s.signal <- struct{}{}:
		default:
		}
	}
	return nil
}

func (s *terminalStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	select {
	case s.signal <- struct{}{}:
	default:
	}
	return nil
}

func (s *terminalStream) outcome() (*agent.Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal, s.closed
}
