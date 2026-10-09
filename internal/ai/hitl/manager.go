package hitl

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
)

type requestStatus uint8

const (
	requestPending requestStatus = iota
	requestConsumed
	requestStale
	requestExpired
	requestCanceled
)

type requestState struct {
	interrupt Interrupt
	status    requestStatus
	timer     *time.Timer
}

type runState struct {
	id           string
	checkpointID string
	ctx          context.Context
	cancel       context.CancelFunc

	mu         sync.Mutex
	status     RunStatus
	attempt    uint64
	sequence   uint64
	requests   map[string]*requestState
	answers    map[string]string
	events     []Event
	terminal   *Event
	finishedAt time.Time
	cancelFn   adk.AgentCancelFunc
	cleanupErr error
}

type Manager struct {
	checkpoints       adk.CheckPointStore
	store             Store
	ttl               time.Duration
	terminalRetention time.Duration
	now               func() time.Time
	newNonce          func() (string, error)

	mu     sync.RWMutex
	runs   map[string]*runState
	nonces map[string]string
	closed bool
}

func NewManager(config Config) (*Manager, error) {
	if config.Checkpoints == nil {
		return nil, fmt.Errorf("%w: checkpoint store is required", ErrInvalidArgument)
	}
	if config.TTL <= 0 {
		config.TTL = DefaultTTL
	}
	if config.TerminalRetention <= 0 {
		config.TerminalRetention = DefaultTerminalRetention
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.NewNonce == nil {
		config.NewNonce = secureNonce
	}
	return &Manager{
		checkpoints:       config.Checkpoints,
		store:             config.Store,
		ttl:               config.TTL,
		terminalRetention: config.TerminalRetention,
		now:               config.Now,
		newNonce:          config.NewNonce,
		runs:              make(map[string]*runState),
		nonces:            make(map[string]string),
	}, nil
}

func secureNonce() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (m *Manager) RegisterRun(ctx context.Context, runID, checkpointID string) (Snapshot, error) {
	if !validIDs(runID, checkpointID) {
		return Snapshot{}, ErrInvalidArgument
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	run := &runState{
		id:           runID,
		checkpointID: checkpointID,
		ctx:          runCtx,
		cancel:       cancel,
		status:       RunStatusRunning,
		attempt:      1,
		requests:     make(map[string]*requestState),
		answers:      make(map[string]string),
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		return Snapshot{}, ErrManagerClosed
	}
	if _, exists := m.runs[runID]; exists {
		m.mu.Unlock()
		cancel()
		return Snapshot{}, ErrRunExists
	}
	m.runs[runID] = run
	m.mu.Unlock()
	m.sweepTerminal(m.now())
	if err := m.persistLocked(run); err != nil {
		m.mu.Lock()
		delete(m.runs, runID)
		m.mu.Unlock()
		cancel()
		return Snapshot{}, err
	}
	return run.snapshot(), nil
}

func (m *Manager) Interrupt(ctx context.Context, target *adk.InterruptCtx, input InterruptInput) (Interrupt, error) {
	if target == nil || !validIDs(input.RunID, input.CheckpointID, input.CallID, input.Tool, target.ID) {
		return Interrupt{}, ErrInvalidArgument
	}
	if input.Kind != KindConfirm && input.Kind != KindQuestion {
		return Interrupt{}, ErrInvalidArgument
	}
	if input.Kind == KindQuestion && (input.Question == nil || strings.TrimSpace(input.Question.Text) == "") {
		return Interrupt{}, ErrInvalidArgument
	}
	if input.Kind == KindConfirm && input.Question != nil {
		return Interrupt{}, ErrInvalidArgument
	}
	parameters, parameterHash, err := CanonicalParameters(input.Parameters)
	if err != nil {
		return Interrupt{}, err
	}
	question := cloneQuestion(input.Question)
	questionJSON, err := json.Marshal(question)
	if err != nil {
		return Interrupt{}, fmt.Errorf("%w: question: %v", ErrInvalidArgument, err)
	}
	run, err := m.run(input.RunID)
	if err != nil {
		return Interrupt{}, err
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.terminal != nil {
		return Interrupt{}, ErrRunFinished
	}
	if run.checkpointID != input.CheckpointID {
		return Interrupt{}, ErrCheckpointChanged
	}
	checkpoint, ok, err := m.checkpoints.Get(ctx, run.checkpointID)
	if err != nil {
		return Interrupt{}, fmt.Errorf("hitl: load checkpoint: %w", err)
	}
	if !ok {
		return Interrupt{}, ErrCheckpointNotFound
	}
	checkpointHash := digest("checkpoint", checkpoint)
	requestID := stableID("itr_", run.id, run.checkpointID, checkpointHash, target.ID, input.CallID, input.Tool, string(input.Kind), parameterHash, string(questionJSON))
	if question != nil {
		question.ID = requestID
	}
	if existing := run.requests[requestID]; existing != nil {
		switch existing.status {
		case requestPending:
			return cloneInterrupt(existing.interrupt), nil
		case requestConsumed:
			return Interrupt{}, ErrRequestConsumed
		default:
			return Interrupt{}, ErrRequestStale
		}
	}
	for _, pending := range run.requests {
		if pending.status == requestPending && (pending.interrupt.CheckpointHash != checkpointHash || pending.interrupt.TargetID == target.ID) {
			pending.status = requestStale
			pending.timer.Stop()
			m.releaseNonces(pending.interrupt.Nonce)
		}
	}
	nonce, err := m.uniqueNonce(run.id)
	if err != nil {
		return Interrupt{}, fmt.Errorf("hitl: generate nonce: %w", err)
	}
	now := m.now()
	interrupt := Interrupt{
		ID:             requestID,
		RunID:          run.id,
		CheckpointID:   run.checkpointID,
		CheckpointHash: checkpointHash,
		TargetID:       target.ID,
		CallID:         input.CallID,
		Tool:           input.Tool,
		Kind:           input.Kind,
		Parameters:     parameters,
		ParameterHash:  parameterHash,
		Question:       question,
		Nonce:          nonce,
		CreatedAt:      now,
		ExpiresAt:      now.Add(m.ttl),
		Attempt:        run.attempt,
	}
	request := &requestState{interrupt: interrupt, status: requestPending}
	run.requests[requestID] = request
	run.status = RunStatusInterrupted
	event := run.appendEventLocked(EventInterrupted, TerminalInterrupted, requestID, "")
	interrupt.Sequence = event.Sequence
	request.interrupt.Sequence = event.Sequence
	request.timer = time.AfterFunc(m.ttl, func() { m.expire(run.id, requestID) })
	if err := m.persistLocked(run); err != nil {
		return Interrupt{}, err
	}
	return cloneInterrupt(interrupt), nil
}

func (m *Manager) Resume(ctx context.Context, resumer Resumer, answer Answer, opts ...adk.AgentRunOption) (Resume, *adk.AsyncIterator[*adk.AgentEvent], error) {
	if resumer == nil || !validIDs(answer.ID, answer.RunID, answer.RequestID, answer.CheckpointID, answer.TargetID, answer.CallID, answer.Nonce) {
		return Resume{}, nil, ErrInvalidArgument
	}
	_, parameterHash, err := CanonicalParameters(answer.Parameters)
	if err != nil {
		return Resume{}, nil, err
	}
	run, err := m.run(answer.RunID)
	if err != nil {
		return Resume{}, nil, err
	}
	run.mu.Lock()
	if run.terminal != nil {
		run.mu.Unlock()
		return Resume{}, nil, ErrRunFinished
	}
	if run.checkpointID != answer.CheckpointID {
		run.mu.Unlock()
		return Resume{}, nil, ErrCheckpointChanged
	}
	request := run.requests[answer.RequestID]
	if request == nil {
		run.mu.Unlock()
		return Resume{}, nil, ErrRequestNotFound
	}
	interrupt := request.interrupt
	if interrupt.TargetID != answer.TargetID || interrupt.CallID != answer.CallID || interrupt.CheckpointID != answer.CheckpointID {
		run.mu.Unlock()
		return Resume{}, nil, ErrRequestStale
	}
	if parameterHash != interrupt.ParameterHash {
		run.mu.Unlock()
		return Resume{}, nil, ErrParametersChanged
	}
	if subtle.ConstantTimeCompare([]byte(request.interrupt.Nonce), []byte(answer.Nonce)) != 1 {
		run.mu.Unlock()
		return Resume{}, nil, ErrInvalidNonce
	}
	value, err := answerValue(interrupt.Kind, answer)
	if err != nil {
		run.mu.Unlock()
		return Resume{}, nil, err
	}
	if previousRequest, exists := run.answers[answer.ID]; exists {
		run.mu.Unlock()
		if previousRequest == answer.RequestID {
			return Resume{}, nil, ErrRequestConsumed
		}
		return Resume{}, nil, ErrAnswerReused
	}
	switch request.status {
	case requestConsumed:
		run.mu.Unlock()
		return Resume{}, nil, ErrRequestConsumed
	case requestExpired:
		run.mu.Unlock()
		return Resume{}, nil, ErrRequestExpired
	case requestPending:
	default:
		run.mu.Unlock()
		return Resume{}, nil, ErrRequestStale
	}
	if !m.now().Before(interrupt.ExpiresAt) {
		run.mu.Unlock()
		m.expire(run.id, request.interrupt.ID)
		return Resume{}, nil, ErrRequestExpired
	}
	if run.status != RunStatusInterrupted {
		run.mu.Unlock()
		return Resume{}, nil, ErrRunNotInterrupted
	}
	checkpoint, ok, err := m.checkpoints.Get(ctx, run.checkpointID)
	if err != nil {
		run.mu.Unlock()
		return Resume{}, nil, fmt.Errorf("hitl: load checkpoint: %w", err)
	}
	if !ok {
		run.mu.Unlock()
		return Resume{}, nil, ErrCheckpointNotFound
	}
	if digest("checkpoint", checkpoint) != interrupt.CheckpointHash {
		request.status = requestStale
		request.timer.Stop()
		nonce := request.interrupt.Nonce
		run.mu.Unlock()
		m.releaseNonces(nonce)
		return Resume{}, nil, ErrCheckpointChanged
	}
	request.status = requestConsumed
	request.timer.Stop()
	m.releaseNonces(request.interrupt.Nonce)
	run.answers[answer.ID] = request.interrupt.ID
	run.status = RunStatusRunning
	run.attempt++
	resume := Resume{
		ID:           stableID("res_", request.interrupt.ID, answer.ID),
		AnswerID:     answer.ID,
		RequestID:    request.interrupt.ID,
		RunID:        run.id,
		CheckpointID: run.checkpointID,
		TargetID:     request.interrupt.TargetID,
		Attempt:      run.attempt,
	}
	event := run.appendEventLocked(EventResumed, "", request.interrupt.ID, "")
	resume.Sequence = event.Sequence
	cancelOption, cancelFn := adk.WithCancel()
	run.cancelFn = cancelFn
	params := &adk.ResumeParams{Targets: map[string]any{request.interrupt.TargetID: value}}
	options := append([]adk.AgentRunOption(nil), opts...)
	options = append(options, cancelOption, adk.WithCheckPointID(run.checkpointID))
	if err := m.persistLocked(run); err != nil {
		run.mu.Unlock()
		return Resume{}, nil, err
	}
	run.mu.Unlock()
	iterator, resumeErr := resumer.ResumeWithParams(run.ctx, run.checkpointID, params, options...)
	if resumeErr == nil && iterator == nil {
		resumeErr = errors.New("hitl: resumer returned a nil event stream")
	}
	if resumeErr != nil {
		_, _ = m.finish(run, reasonForError(resumeErr), errorMessage(resumeErr), true)
		return Resume{}, nil, fmt.Errorf("hitl: resume checkpoint: %w", resumeErr)
	}
	return resume, iterator, nil
}

func answerValue(kind Kind, answer Answer) (string, error) {
	switch kind {
	case KindConfirm:
		if answer.Text != "" {
			return "", ErrInvalidArgument
		}
		switch answer.Decision {
		case DecisionAllow, DecisionAllowSession, DecisionAllowPersist, DecisionDeny:
			return string(answer.Decision), nil
		default:
			return "", ErrInvalidArgument
		}
	case KindQuestion:
		if answer.Decision != "" || strings.TrimSpace(answer.Text) == "" {
			return "", ErrInvalidArgument
		}
		return answer.Text, nil
	default:
		return "", ErrInvalidArgument
	}
}

func (m *Manager) Finish(runID string, reason TerminalReason, cause error) (Event, error) {
	if reason != TerminalCompleted && reason != TerminalCanceled && reason != TerminalFailed {
		return Event{}, ErrInvalidArgument
	}
	run, err := m.run(runID)
	if err != nil {
		return Event{}, err
	}
	message := ""
	if cause != nil {
		message = errorMessage(cause)
	}
	return m.finish(run, reason, message, false)
}

func (m *Manager) FinishError(runID string, cause error) (Event, error) {
	if cause == nil {
		return m.Finish(runID, TerminalCompleted, nil)
	}
	return m.Finish(runID, reasonForError(cause), cause)
}

func (m *Manager) Cancel(runID string) (Event, error) {
	run, err := m.run(runID)
	if err != nil {
		return Event{}, err
	}
	return m.finish(run, TerminalCanceled, "", true)
}

func reasonForError(err error) TerminalReason {
	var cancelErr *adk.CancelError
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &cancelErr) {
		return TerminalCanceled
	}
	return TerminalFailed
}

func errorMessage(err error) string {
	var cancelErr *adk.CancelError
	if errors.As(err, &cancelErr) && cancelErr.Info == nil {
		return "agent canceled"
	}
	return err.Error()
}

func (m *Manager) finish(run *runState, reason TerminalReason, message string, cancelExecution bool) (Event, error) {
	run.mu.Lock()
	if run.terminal != nil {
		terminal := *run.terminal
		cleanupErr := run.cleanupErr
		run.mu.Unlock()
		if terminal.Reason == reason && terminal.Message == message {
			return terminal, cleanupErr
		}
		return terminal, ErrRunFinished
	}
	status := map[TerminalReason]RunStatus{
		TerminalCompleted: RunStatusCompleted,
		TerminalCanceled:  RunStatusCanceled,
		TerminalFailed:    RunStatusFailed,
		TerminalExpired:   RunStatusExpired,
	}[reason]
	run.status = status
	nonces := make([]string, 0, len(run.requests))
	for _, request := range run.requests {
		nonces = append(nonces, request.interrupt.Nonce)
		if request.status != requestPending {
			continue
		}
		if reason == TerminalExpired {
			request.status = requestExpired
		} else {
			request.status = requestCanceled
		}
		request.timer.Stop()
	}
	run.finishedAt = m.now()
	event := run.appendEventLocked(EventTerminal, reason, "", message)
	run.terminal = &event
	cancelFn := run.cancelFn
	run.cancelFn = nil
	persistErr := m.persistLocked(run)
	run.mu.Unlock()
	m.releaseNonces(nonces...)
	m.sweepTerminal(m.now())
	run.cancel()
	if cancelExecution && cancelFn != nil {
		_, _ = cancelFn(adk.WithAgentCancelMode(adk.CancelImmediate))
	}
	cleanupErr := m.deleteCheckpoint(run.checkpointID)
	if cleanupErr == nil {
		cleanupErr = persistErr
	}
	run.mu.Lock()
	run.cleanupErr = cleanupErr
	run.mu.Unlock()
	return event, cleanupErr
}

func (m *Manager) expire(runID, requestID string) {
	run, err := m.run(runID)
	if err != nil {
		return
	}
	run.mu.Lock()
	m.mu.RLock()
	closed := m.closed
	m.mu.RUnlock()
	if closed || run.terminal != nil {
		run.mu.Unlock()
		return
	}
	request := run.requests[requestID]
	if request == nil || request.status != requestPending {
		run.mu.Unlock()
		return
	}
	if remaining := request.interrupt.ExpiresAt.Sub(m.now()); remaining > 0 {
		request.timer = time.AfterFunc(remaining, func() { m.expire(run.id, requestID) })
		run.mu.Unlock()
		return
	}
	request.status = requestExpired
	nonce := request.interrupt.Nonce
	blocked := run.status == RunStatusInterrupted && !run.hasPendingLocked()
	if !blocked {
		_ = m.persistLocked(run)
	}
	run.mu.Unlock()
	m.releaseNonces(nonce)
	if blocked {
		_, _ = m.finish(run, TerminalExpired, "", true)
	}
}

func (run *runState) hasPendingLocked() bool {
	for _, request := range run.requests {
		if request.status == requestPending {
			return true
		}
	}
	return false
}

func (m *Manager) releaseNonces(nonces ...string) {
	if len(nonces) == 0 {
		return
	}
	m.mu.Lock()
	for _, nonce := range nonces {
		delete(m.nonces, nonce)
	}
	m.mu.Unlock()
}

func (m *Manager) sweepTerminal(now time.Time) {
	if m.terminalRetention <= 0 {
		return
	}
	m.mu.RLock()
	runs := make([]*runState, 0, len(m.runs))
	for _, run := range m.runs {
		runs = append(runs, run)
	}
	m.mu.RUnlock()
	type victim struct {
		id  string
		run *runState
	}
	var victims []victim
	for _, run := range runs {
		run.mu.Lock()
		stale := run.terminal != nil && !run.finishedAt.IsZero() && !now.Before(run.finishedAt.Add(m.terminalRetention))
		run.mu.Unlock()
		if stale {
			victims = append(victims, victim{id: run.id, run: run})
		}
	}
	if len(victims) == 0 {
		return
	}
	m.mu.Lock()
	for _, candidate := range victims {
		if m.runs[candidate.id] == candidate.run {
			delete(m.runs, candidate.id)
		}
	}
	m.mu.Unlock()
}

func (m *Manager) deleteCheckpoint(checkpointID string) error {
	deleter, ok := m.checkpoints.(adk.CheckPointDeleter)
	if !ok {
		return nil
	}
	if err := deleter.Delete(context.Background(), checkpointID); err != nil {
		return fmt.Errorf("hitl: delete checkpoint: %w", err)
	}
	return nil
}

func (m *Manager) Snapshot(runID string) (Snapshot, error) {
	run, err := m.run(runID)
	if err != nil {
		return Snapshot{}, err
	}
	return run.snapshot(), nil
}

func (run *runState) snapshot() Snapshot {
	run.mu.Lock()
	defer run.mu.Unlock()
	snapshot := Snapshot{
		RunID:        run.id,
		CheckpointID: run.checkpointID,
		Status:       run.status,
		Attempt:      run.attempt,
		Sequence:     run.sequence,
		Pending:      make([]Interrupt, 0),
	}
	for _, request := range run.requests {
		if request.status == requestPending {
			snapshot.Pending = append(snapshot.Pending, cloneInterrupt(request.interrupt))
		}
	}
	sort.Slice(snapshot.Pending, func(i, j int) bool { return snapshot.Pending[i].Sequence < snapshot.Pending[j].Sequence })
	if run.terminal != nil {
		terminal := *run.terminal
		snapshot.Terminal = &terminal
	}
	return snapshot
}

func (m *Manager) Events(runID string, after uint64) ([]Event, error) {
	run, err := m.run(runID)
	if err != nil {
		return nil, err
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	events := make([]Event, 0, len(run.events))
	for _, event := range run.events {
		if event.Sequence > after {
			events = append(events, event)
		}
	}
	return events, nil
}

func (m *Manager) Done(runID string) (<-chan struct{}, error) {
	run, err := m.run(runID)
	if err != nil {
		return nil, err
	}
	return run.ctx.Done(), nil
}

func (run *runState) appendEventLocked(kind EventKind, reason TerminalReason, requestID, message string) Event {
	run.sequence++
	event := Event{
		RunID:        run.id,
		CheckpointID: run.checkpointID,
		RequestID:    requestID,
		Kind:         kind,
		Reason:       reason,
		Message:      message,
		Attempt:      run.attempt,
		Sequence:     run.sequence,
	}
	run.events = append(run.events, event)
	return event
}

func (m *Manager) uniqueNonce(runID string) (string, error) {
	for range 4 {
		nonce, err := m.newNonce()
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(nonce) == "" {
			return "", ErrInvalidArgument
		}
		m.mu.Lock()
		if _, exists := m.nonces[nonce]; !exists {
			m.nonces[nonce] = runID
			m.mu.Unlock()
			return nonce, nil
		}
		m.mu.Unlock()
	}
	return "", errors.New("nonce generator repeatedly returned a duplicate")
}

func (m *Manager) run(runID string) (*runState, error) {
	m.mu.RLock()
	run := m.runs[runID]
	m.mu.RUnlock()
	if run == nil {
		return nil, ErrRunNotFound
	}
	return run, nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	runs := make([]*runState, 0, len(m.runs))
	for _, run := range m.runs {
		runs = append(runs, run)
	}
	m.mu.Unlock()
	var firstErr error
	for _, run := range runs {
		run.mu.Lock()
		parked := run.terminal == nil && run.status == RunStatusInterrupted
		if parked {
			for _, request := range run.requests {
				if request.timer != nil {
					request.timer.Stop()
				}
			}
		}
		run.mu.Unlock()
		if parked {
			run.cancel()
			continue
		}
		if _, err := m.finish(run, TerminalCanceled, "", true); err != nil && !errors.Is(err, ErrRunFinished) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func validIDs(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func cloneInterrupt(value Interrupt) Interrupt {
	value.Parameters = append(json.RawMessage(nil), value.Parameters...)
	value.Question = cloneQuestion(value.Question)
	return value
}

func cloneQuestion(value *Question) *Question {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Options = append([]string(nil), value.Options...)
	return &clone
}
