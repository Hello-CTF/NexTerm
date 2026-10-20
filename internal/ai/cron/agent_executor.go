package cron

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

type AgentRunner interface {
	Start(ctx context.Context, args agent.ChatArgs, factory agent.StreamFactory) (agent.StartResponse, error)
	Cancel(jobID string) error
}

type AgentExecutor struct {
	Runner AgentRunner

	ScopeFor             func(context.Context, Trigger) (tools.Scope, error)
	AllowOwnerlessLegacy bool
	// Connect, when non-nil, authorizes and dials a disconnected scope asset
	// before the run starts; a nil connector keeps the fail-without-connecting
	// behavior for disconnected assets.
	Connect *Connector
}

var _ Executor = (*AgentExecutor)(nil)

func (e *AgentExecutor) Execute(ctx context.Context, trigger Trigger) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ownerExecutionError(trigger.OwnerID, trigger.OwnerKind, e.AllowOwnerlessLegacy); err != nil {
		return err
	}
	ctx = contextWithOwner(ctx, trigger.OwnerID, trigger.OwnerKind)
	if e.Runner == nil {
		return errors.New("cron: agent executor has no runner")
	}
	stream := newTerminalStream()
	args := agent.ChatArgs{ConversationID: trigger.SessionID, Message: trigger.Prompt, Source: "cron", ModelProfileID: trigger.ModelProfileID}
	if e.ScopeFor != nil {
		scope, err := e.ScopeFor(ctx, trigger)
		if err != nil {
			scope, err = e.connectDisconnectedScope(ctx, trigger, err)
			if err != nil {
				return fmt.Errorf("无法查询会话的终端范围，本次定时运行已终止，请确认会话仍然存在后重试: %w", err)
			}
		}
		args.Scope = scope
	}
	response, err := e.Runner.Start(guard.WithUnattended(ctx), args, agent.StaticStream(stream))
	if err != nil {
		return fmt.Errorf("cron: start unattended agent run: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			_ = e.Runner.Cancel(response.JobID)
			return errors.Join(ctx.Err(), e.waitRun(ctx, response.JobID, stream))
		case <-stream.signal:
			if event, closed := stream.outcome(); event != nil {
				return e.terminalError(ctx, response.JobID, *event, stream)
			} else if closed {
				// The stream ended without a terminal event, so the underlying
				// run may still be alive; cancel and wait for it instead of
				// letting it outlive the cron run.
				_ = e.Runner.Cancel(response.JobID)
				return errors.Join(fmt.Errorf("cron: agent run %s ended without a terminal event", response.JobID), e.waitRun(ctx, response.JobID, stream))
			}
		}
	}
}

// connectDisconnectedScope applies the opt-in connect flow when scope
// resolution reports a disconnected asset: it authorizes and dials the asset
// once, then retries the scope query a single time. Without an explicit
// opt-in the run keeps the original disconnected failure; every other scope
// failure stays on the original path.
func (e *AgentExecutor) connectDisconnectedScope(ctx context.Context, trigger Trigger, scopeErr error) (tools.Scope, error) {
	var disconnected *AssetDisconnectedError
	if e.Connect == nil || !errors.As(scopeErr, &disconnected) || strings.TrimSpace(disconnected.AssetID) == "" {
		return tools.Scope{}, scopeErr
	}
	if _, err := e.Connect.Connect(ctx, trigger, disconnected.AssetID); err != nil {
		if errors.Is(err, guard.ErrConnectOptInRequired) {
			return tools.Scope{}, scopeErr
		}
		return tools.Scope{}, err
	}
	return e.ScopeFor(ctx, trigger)
}

func (e *AgentExecutor) waitRun(ctx context.Context, jobID string, stream *terminalStream) error {
	if waiter, ok := e.Runner.(interface {
		Wait(context.Context, string) error
	}); ok {
		return waiter.Wait(context.WithoutCancel(ctx), jobID)
	}
	<-stream.done
	return nil
}

func (e *AgentExecutor) terminalError(ctx context.Context, jobID string, event agent.Event, stream *terminalStream) error {
	switch event.Type {
	case "done":
		return nil
	case "error":
		if event.Message == "" {
			return fmt.Errorf("cron: agent run %s failed", jobID)
		}
		return fmt.Errorf("cron: agent run %s failed: %s", jobID, event.Message)
	default:
		_ = e.Runner.Cancel(jobID)
		return errors.Join(fmt.Errorf("cron: unattended agent run %s stopped at %s for tool %q: %s", jobID, event.Type, event.Tool, event.Reason), e.waitRun(ctx, jobID, stream))
	}
}

type terminalStream struct {
	mu        sync.Mutex
	terminal  *agent.Event
	closed    bool
	signal    chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newTerminalStream() *terminalStream {
	return &terminalStream{signal: make(chan struct{}, 1), done: make(chan struct{})}
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
	s.closeOnce.Do(func() { close(s.done) })
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
