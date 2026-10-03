package steer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

var (
	ErrNotStarted = errors.New("steering runner has not started")
	ErrStarted    = errors.New("steering runner has already started")
	ErrCanceled   = errors.New("steering runner is canceled")
	ErrClosed     = errors.New("steering runner is closed")
	ErrQueueFull  = errors.New("steering queue is full")
)

type AgentFactory func(context.Context) (adk.Agent, error)

type EventHandler func(context.Context, *adk.AgentEvent) error

type Config struct {
	NewAgent        AgentFactory
	OnEvent         EventHandler
	MaxPending      int
	EnableStreaming bool
}

type StartRequest struct {
	Message *schema.Message
	History []*schema.Message
}

type Result struct {
	History  []*schema.Message
	Leftover []*schema.Message
}

type runnerState uint8

const (
	runnerNew runnerState = iota
	runnerRunning
	runnerCanceled
	runnerClosed
)

type Runner struct {
	config Config

	mu      sync.Mutex
	state   runnerState
	loop    *adk.TurnLoop[*schema.Message, *schema.Message]
	pending int
	result  Result
	waitErr error

	historyMu sync.Mutex
	history   []*schema.Message

	done     chan struct{}
	doneOnce sync.Once
}

func NewRunner(config Config) (*Runner, error) {
	if config.NewAgent == nil {
		return nil, errors.New("steering agent factory is required")
	}
	if config.MaxPending < 0 {
		return nil, errors.New("steering queue limit cannot be negative")
	}
	if config.MaxPending == 0 {
		config.MaxPending = 64
	}
	return &Runner{config: config, state: runnerNew, done: make(chan struct{})}, nil
}

func (r *Runner) Start(ctx context.Context, request StartRequest) error {
	if err := validateUserMessage(request.Message); err != nil {
		return err
	}
	if err := ValidateHistory(request.History); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	switch r.state {
	case runnerCanceled:
		return ErrCanceled
	case runnerClosed:
		return ErrClosed
	case runnerRunning:
		return ErrStarted
	}

	r.historyMu.Lock()
	r.history = CloneHistory(request.History)
	r.historyMu.Unlock()
	loop := adk.NewTurnLoop(adk.TurnLoopConfig[*schema.Message, *schema.Message]{
		GenInput:      r.genInput,
		PrepareAgent:  r.prepareAgent,
		OnAgentEvents: r.onAgentEvents,
	})
	r.loop = loop
	r.state = runnerRunning
	r.pending = 1
	accepted, _ := loop.Push(cloneMessage(request.Message))
	if !accepted {
		r.state = runnerClosed
		r.pending = 0
		return ErrClosed
	}
	loop.Run(ctx)
	go r.await(loop)
	return nil
}

func (r *Runner) Steer(message *schema.Message) error {
	if err := validateUserMessage(message); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch r.state {
	case runnerNew:
		return ErrNotStarted
	case runnerCanceled:
		return ErrCanceled
	case runnerClosed:
		return ErrClosed
	}
	if r.pending >= r.config.MaxPending {
		return ErrQueueFull
	}
	r.pending++
	accepted, _ := r.loop.Push(cloneMessage(message), adk.WithPreempt[*schema.Message, *schema.Message](adk.AfterToolCalls))
	if !accepted {
		r.pending--
		return ErrClosed
	}
	return nil
}

func (r *Runner) Cancel() {
	r.mu.Lock()
	switch r.state {
	case runnerCanceled, runnerClosed:
		r.mu.Unlock()
		return
	case runnerNew:
		r.state = runnerCanceled
		r.pending = 0
		r.waitErr = context.Canceled
		r.mu.Unlock()
		r.doneOnce.Do(func() { close(r.done) })
		return
	}
	r.state = runnerCanceled
	r.pending = 0
	r.loop.Stop(adk.WithImmediate(), adk.WithSkipCheckpoint(), adk.WithStopCause("canceled"))
	r.mu.Unlock()
}

func (r *Runner) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pending
}

func (r *Runner) History() []*schema.Message {
	r.historyMu.Lock()
	defer r.historyMu.Unlock()
	return stableHistory(r.history)
}

func (r *Runner) Wait(ctx context.Context) (Result, error) {
	select {
	case <-r.done:
		r.mu.Lock()
		defer r.mu.Unlock()
		r.result.History = r.History()
		r.result.Leftover = CloneHistory(r.result.Leftover)
		return r.result, r.waitErr
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

func (r *Runner) genInput(_ context.Context, _ *adk.TurnLoop[*schema.Message, *schema.Message], items []*schema.Message) (*adk.GenInputResult[*schema.Message, *schema.Message], error) {
	if len(items) == 0 {
		return nil, errors.New("steering input is empty")
	}
	r.mu.Lock()
	if r.state != runnerRunning {
		r.mu.Unlock()
		return nil, ErrCanceled
	}
	if r.pending > 0 {
		r.pending--
	}
	r.mu.Unlock()

	r.historyMu.Lock()
	defer r.historyMu.Unlock()
	if err := ValidateHistory(r.history); err != nil {
		return nil, err
	}
	r.history = append(r.history, cloneMessage(items[0]))
	input := CloneHistory(r.history)
	return &adk.GenInputResult[*schema.Message, *schema.Message]{
		Input:     &adk.AgentInput{Messages: input, EnableStreaming: r.config.EnableStreaming},
		Consumed:  items[:1],
		Remaining: items[1:],
	}, nil
}

func (r *Runner) prepareAgent(ctx context.Context, _ *adk.TurnLoop[*schema.Message, *schema.Message], _ []*schema.Message) (adk.Agent, error) {
	agent, err := r.config.NewAgent(ctx)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, errors.New("steering agent factory returned nil")
	}
	return agent, nil
}

func (r *Runner) onAgentEvents(ctx context.Context, turn *adk.TurnContext[*schema.Message, *schema.Message], events *adk.AsyncIterator[*adk.AgentEvent]) error {
	var firstErr error
	for {
		event, ok := events.Next()
		if !ok {
			return firstErr
		}
		if event == nil {
			continue
		}
		preempted := channelClosed(turn.Preempted)
		var cancelErr *adk.CancelError
		canceledByPreempt := errors.As(event.Err, &cancelErr) && preempted
		materialized, message, err := materializeEvent(event)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if !canceledByPreempt && r.acceptingEvents() {
			if message != nil {
				r.recordMessage(materialized.Output.MessageOutput.Role, message)
			}
			if r.config.OnEvent != nil {
				if err := r.config.OnEvent(ctx, materialized); err != nil && firstErr == nil {
					firstErr = err
				}
			}
		}
		if event.Err != nil && !canceledByPreempt && firstErr == nil {
			firstErr = event.Err
		}
	}
}

func (r *Runner) acceptingEvents() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state == runnerRunning
}

func (r *Runner) recordMessage(role schema.RoleType, message *schema.Message) {
	if role != schema.Assistant && role != schema.Tool {
		return
	}
	r.historyMu.Lock()
	r.history = append(r.history, cloneMessage(message))
	r.historyMu.Unlock()
}

func (r *Runner) await(loop *adk.TurnLoop[*schema.Message, *schema.Message]) {
	exit := loop.Wait()
	r.mu.Lock()
	explicitlyCanceled := r.state == runnerCanceled
	discardInput := explicitlyCanceled || errors.Is(exit.ExitReason, context.Canceled) || errors.Is(exit.ExitReason, context.DeadlineExceeded)
	if r.state == runnerRunning {
		r.state = runnerClosed
	}
	r.pending = 0
	r.waitErr = exit.ExitReason
	if exit.CheckpointErr != nil && r.waitErr == nil {
		r.waitErr = exit.CheckpointErr
	}
	if explicitlyCanceled {
		r.waitErr = context.Canceled
	}
	if discardInput {
		r.result.Leftover = nil
	} else {
		r.result.Leftover = CloneHistory(exit.UnhandledItems)
		if exit.TakeLateItems != nil {
			r.result.Leftover = append(r.result.Leftover, CloneHistory(exit.TakeLateItems())...)
		}
	}
	r.result.History = nil
	r.mu.Unlock()
	r.doneOnce.Do(func() { close(r.done) })
}

func validateUserMessage(message *schema.Message) error {
	if message == nil {
		return errors.New("steering message is required")
	}
	if message.Role != schema.User {
		return fmt.Errorf("steering message role must be %q", schema.User)
	}
	return nil
}

func materializeEvent(event *adk.AgentEvent) (*adk.AgentEvent, *schema.Message, error) {
	cloned := *event
	if event.Output == nil || event.Output.MessageOutput == nil {
		return &cloned, nil, nil
	}
	output := *event.Output
	variant := *event.Output.MessageOutput
	message, err := variant.GetMessage()
	if err != nil {
		return &cloned, nil, err
	}
	variant.IsStreaming = false
	variant.Message = cloneMessage(message)
	variant.MessageStream = nil
	output.MessageOutput = &variant
	cloned.Output = &output
	return &cloned, message, nil
}

func channelClosed(channel <-chan struct{}) bool {
	if channel == nil {
		return false
	}
	select {
	case <-channel:
		return true
	default:
		return false
	}
}
