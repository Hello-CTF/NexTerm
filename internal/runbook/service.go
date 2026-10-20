package runbook

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

var (
	ErrInvalid                      = errors.New("invalid runbook run")
	ErrNotFound                     = errors.New("runbook run not found")
	ErrConflict                     = errors.New("runbook run already active")
	ErrForbidden                    = errors.New("runbook run forbidden")
	ErrStepConfirmationRequired     = errors.New("runbook step confirmation required")
	ErrProductionConfirmationNeeded = errors.New("runbook production confirmation required")
)

type Executor interface {
	Execute(context.Context, string, execbatch.Request) (execbatch.Batch, error)
	Cancel(context.Context, string, string) (execbatch.CancelResult, error)
}

type Service struct {
	executor Executor
	mu       sync.Mutex
	runs     map[string]*run
}

type run struct {
	ownerID       string
	executor      Executor
	timeoutMS     int64
	maxConcurrent int

	mu              sync.Mutex
	state           RunState
	requests        []execbatch.Request
	nextRequest     int
	advancing       bool
	stepConfirmed   bool
	cancelRequested bool
}

func New(executor Executor) *Service {
	return &Service{executor: executor, runs: make(map[string]*run)}
}

func (s *Service) Start(_ context.Context, ownerID string, input StartRequest) (RunState, error) {
	if s.executor == nil {
		return RunState{}, fmt.Errorf("%w: exec batch executor unavailable", ErrInvalid)
	}
	if err := validateStart(input); err != nil {
		return RunState{}, err
	}
	policy := input.ConfirmationPolicy
	if policy == "" {
		policy = ConfirmEveryStep
	}
	stopOnFailure := true
	if input.StopOnFailure != nil {
		stopOnFailure = *input.StopOnFailure
	}
	steps := make([]StepState, len(input.Steps))
	for index, step := range input.Steps {
		steps[index] = StepState{
			Number: int32(index + 1), Step: step, Status: StepPending,
			Results: []execbatch.Result{}, BatchIDs: []string{},
		}
	}
	steps[0].Status = StepAwaitingRender
	r := &run{
		ownerID: ownerID, executor: s.executor, timeoutMS: input.TimeoutMS, maxConcurrent: input.MaxConcurrent,
		state: RunState{
			RunID: input.RunID, RunbookID: input.RunbookID, RunbookName: input.RunbookName,
			Status: RunAwaitingRender, ConfirmationPolicy: policy, StopOnFailure: stopOnFailure,
			SessionIDs: append([]string(nil), input.SessionIDs...), CurrentStep: 1,
			StepCount: int32(len(steps)), Steps: steps,
		},
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.runs[input.RunID]; exists {
		return RunState{}, fmt.Errorf("%w: %s", ErrConflict, input.RunID)
	}
	s.runs[input.RunID] = r
	return r.snapshot(), nil
}

func (s *Service) Get(_ context.Context, ownerID, runID string) (RunState, error) {
	r, err := s.ownerRun(ownerID, runID)
	if err != nil {
		return RunState{}, err
	}
	return r.snapshot(), nil
}

func (s *Service) Prepare(_ context.Context, ownerID string, input PrepareRequest) (RunState, error) {
	r, err := s.ownerRun(ownerID, input.RunID)
	if err != nil {
		return RunState{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.advancing {
		return RunState{}, fmt.Errorf("%w: step %d is advancing", ErrConflict, input.StepNumber)
	}
	if r.state.Status != RunAwaitingRender || r.state.CurrentStep != input.StepNumber {
		return RunState{}, fmt.Errorf("%w: step %d is not awaiting rendering", ErrInvalid, input.StepNumber)
	}
	commands, err := normalizeRenderedCommands(r.state.SessionIDs, input.Commands)
	if err != nil {
		return RunState{}, err
	}
	r.requests = buildRequests(r.state, commands)
	for index := range r.requests {
		r.requests[index].TimeoutMS = r.timeoutMS
		r.requests[index].MaxConcurrent = r.maxConcurrent
		r.requests[index].Runbook.GroupNumber = int32(index + 1)
		r.requests[index].Runbook.GroupCount = int32(len(r.requests))
	}
	r.nextRequest = 0
	r.stepConfirmed = false
	r.cancelRequested = false
	r.state.Status = RunAwaitingConfirmation
	step := r.currentStepLocked()
	step.Status = StepAwaitingConfirmation
	step.RenderedCommands = append([]RenderedCommand(nil), commands...)
	step.Error = ""
	r.clearProductionLocked()
	return r.snapshotLocked(), nil
}

func (s *Service) Advance(ctx context.Context, ownerID string, input AdvanceRequest) (RunState, error) {
	r, err := s.ownerRun(ownerID, input.RunID)
	if err != nil {
		return RunState{}, err
	}
	r.mu.Lock()
	if r.advancing {
		r.mu.Unlock()
		return RunState{}, fmt.Errorf("%w: step %d is advancing", ErrConflict, input.StepNumber)
	}
	if r.state.CurrentStep != input.StepNumber ||
		(r.state.Status != RunAwaitingConfirmation && r.state.Status != RunAwaitingProductionConfirmation) {
		r.mu.Unlock()
		return RunState{}, fmt.Errorf("%w: step %d is not awaiting confirmation", ErrInvalid, input.StepNumber)
	}
	productionRetry := r.state.Status == RunAwaitingProductionConfirmation
	if productionRetry && input.ProductionConfirmation == nil {
		r.mu.Unlock()
		return RunState{}, ErrProductionConfirmationNeeded
	}
	if !productionRetry && input.ProductionConfirmation != nil {
		r.mu.Unlock()
		return RunState{}, fmt.Errorf("%w: no production challenge is pending", ErrInvalid)
	}
	if !r.stepConfirmed {
		if r.state.ConfirmationPolicy == ConfirmEveryStep && !input.Confirmed {
			r.mu.Unlock()
			return RunState{}, ErrStepConfirmationRequired
		}
		r.stepConfirmed = true
	}
	r.advancing = true
	r.state.Status = RunRunning
	r.currentStepLocked().Status = StepRunning
	r.clearProductionLocked()
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.advancing = false
		r.mu.Unlock()
	}()
	return r.executeCurrent(ctx, ownerID, input.ProductionConfirmation), nil
}

func (s *Service) Cancel(ctx context.Context, ownerID, runID string) (RunState, error) {
	r, err := s.ownerRun(ownerID, runID)
	if err != nil {
		return RunState{}, err
	}
	r.mu.Lock()
	if terminalRun(r.state.Status) {
		state := r.snapshotLocked()
		r.mu.Unlock()
		return state, nil
	}
	r.cancelRequested = true
	batchID := r.state.ActiveBatchID
	if !r.advancing || r.state.Status == RunAwaitingProductionConfirmation {
		r.finishCanceledLocked()
		state := r.snapshotLocked()
		r.mu.Unlock()
		return state, nil
	}
	r.mu.Unlock()
	if batchID != "" {
		if _, err := r.executor.Cancel(ctx, ownerID, batchID); err != nil {
			return r.snapshot(), err
		}
	}
	return r.snapshot(), nil
}

func (s *Service) ownerRun(ownerID, runID string) (*run, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, fmt.Errorf("%w: runId is required", ErrInvalid)
	}
	s.mu.Lock()
	r := s.runs[runID]
	s.mu.Unlock()
	if r == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, runID)
	}
	r.mu.Lock()
	matches := r.ownerID == ownerID
	r.mu.Unlock()
	if !matches {
		return nil, fmt.Errorf("%w: %s", ErrForbidden, runID)
	}
	return r, nil
}

func (r *run) executeCurrent(ctx context.Context, ownerID string, confirmation *execbatch.ProductionConfirmation) RunState {
	for {
		r.mu.Lock()
		if r.cancelRequested {
			r.finishCanceledLocked()
			state := r.snapshotLocked()
			r.mu.Unlock()
			return state
		}
		if r.nextRequest >= len(r.requests) {
			state := r.finishStepLocked()
			r.mu.Unlock()
			return state
		}
		request := r.requests[r.nextRequest]
		request.ProductionConfirmation = confirmation
		confirmation = nil
		r.state.ActiveBatchID = request.BatchID
		r.mu.Unlock()

		batch, err := r.executor.Execute(ctx, ownerID, request)

		r.mu.Lock()
		r.state.ActiveBatchID = ""
		var required *execbatch.ConfirmationRequiredError
		if errors.As(err, &required) {
			if r.cancelRequested {
				r.finishCanceledLocked()
			} else {
				detail := required.IPCDetail()
				r.state.Status = RunAwaitingProductionConfirmation
				r.currentStepLocked().Status = StepAwaitingProductionConfirmation
				r.state.ProductionChallenge = &detail
				r.state.ProductionCommand = request.Command
			}
			state := r.snapshotLocked()
			r.mu.Unlock()
			return state
		}
		var preflight *execbatch.PreflightError
		targetFailure := errors.As(err, &preflight)
		if err != nil && !targetFailure {
			if r.cancelRequested {
				r.finishCanceledLocked()
			} else {
				r.finishFailedLocked(err)
			}
			state := r.snapshotLocked()
			r.mu.Unlock()
			return state
		}
		if targetFailure {
			if r.cancelRequested {
				r.finishCanceledLocked()
				state := r.snapshotLocked()
				r.mu.Unlock()
				return state
			}
			if batch.BatchID == "" {
				batch.BatchID = request.BatchID
			}
			if batch.Command == "" {
				batch.Command = request.Command
			}
			if len(batch.Results) == 0 {
				batch.Results = preflightResults(request, preflight)
			}
		}
		step := r.currentStepLocked()
		step.BatchIDs = append(step.BatchIDs, batch.BatchID)
		step.Results = append(step.Results, batch.Results...)
		if targetFailure {
			step.Error = err.Error()
		}
		r.nextRequest++
		canceled := resultsCanceled(batch.Results)
		if canceled || r.cancelRequested {
			r.finishCanceledLocked()
			state := r.snapshotLocked()
			r.mu.Unlock()
			return state
		}
		r.mu.Unlock()
	}
}

func (r *run) finishStepLocked() RunState {
	step := r.currentStepLocked()
	step.Results = orderedResults(r.state.SessionIDs, step.Results)
	failed := resultsFailed(step.Results)
	if failed {
		step.Status = StepFailed
	} else {
		step.Status = StepSuccess
	}
	if failed && r.state.StopOnFailure {
		r.state.Status = RunFailed
		r.skipRemainingLocked()
		return r.snapshotLocked()
	}
	if r.state.CurrentStep == r.state.StepCount {
		r.state.Status = RunSuccess
		for _, candidate := range r.state.Steps {
			if candidate.Status == StepFailed {
				r.state.Status = RunFailed
				break
			}
		}
		return r.snapshotLocked()
	}
	r.state.CurrentStep++
	r.state.Status = RunAwaitingRender
	r.requests = nil
	r.nextRequest = 0
	r.stepConfirmed = false
	r.currentStepLocked().Status = StepAwaitingRender
	return r.snapshotLocked()
}

func (r *run) finishFailedLocked(err error) {
	step := r.currentStepLocked()
	step.Status = StepFailed
	step.Error = err.Error()
	r.state.Status = RunFailed
	r.state.Error = err.Error()
	r.skipRemainingLocked()
}

func (r *run) finishCanceledLocked() {
	step := r.currentStepLocked()
	step.Status = StepCanceled
	step.Results = appendMissingCanceled(step.Results, step.RenderedCommands)
	step.Results = orderedResults(r.state.SessionIDs, step.Results)
	r.state.Status = RunCanceled
	r.state.ActiveBatchID = ""
	r.clearProductionLocked()
	r.skipRemainingLocked()
}

func (r *run) skipRemainingLocked() {
	for index := range r.state.Steps {
		if r.state.Steps[index].Number > r.state.CurrentStep && r.state.Steps[index].Status == StepPending {
			r.state.Steps[index].Status = StepSkipped
		}
	}
}

func (r *run) currentStepLocked() *StepState {
	return &r.state.Steps[r.state.CurrentStep-1]
}

func (r *run) clearProductionLocked() {
	r.state.ProductionChallenge = nil
	r.state.ProductionCommand = ""
}

func (r *run) snapshot() RunState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

func (r *run) snapshotLocked() RunState {
	state := r.state
	state.SessionIDs = append([]string(nil), r.state.SessionIDs...)
	state.Steps = make([]StepState, len(r.state.Steps))
	for index, step := range r.state.Steps {
		state.Steps[index] = step
		state.Steps[index].RenderedCommands = append([]RenderedCommand(nil), step.RenderedCommands...)
		state.Steps[index].Results = append([]execbatch.Result(nil), step.Results...)
		state.Steps[index].BatchIDs = append([]string(nil), step.BatchIDs...)
	}
	if r.state.ProductionChallenge != nil {
		challenge := *r.state.ProductionChallenge
		challenge.ProductionTargets = append([]execbatch.ConfirmationTarget(nil), challenge.ProductionTargets...)
		state.ProductionChallenge = &challenge
	}
	return state
}

func validateStart(input StartRequest) error {
	if err := validateID("runId", input.RunID, MaxIDBytes); err != nil {
		return err
	}
	if err := validateID("runbookId", input.RunbookID, 128); err != nil {
		return err
	}
	if len(input.RunbookName) == 0 || len(input.RunbookName) > 512 || strings.TrimSpace(input.RunbookName) != input.RunbookName {
		return fmt.Errorf("%w: runbookName must be 1-512 non-space-padded characters", ErrInvalid)
	}
	if len(input.Steps) == 0 || len(input.Steps) > MaxSteps {
		return fmt.Errorf("%w: steps must contain 1-%d entries", ErrInvalid, MaxSteps)
	}
	seenSteps := make(map[string]struct{}, len(input.Steps))
	for _, step := range input.Steps {
		if err := validateID("step.id", step.ID, 128); err != nil {
			return err
		}
		if _, exists := seenSteps[step.ID]; exists {
			return fmt.Errorf("%w: duplicate step id %s", ErrInvalid, step.ID)
		}
		seenSteps[step.ID] = struct{}{}
		if len(step.Name) > 512 || len(step.Description) > 4096 || len(step.SnippetID) > 128 {
			return fmt.Errorf("%w: step metadata is too large", ErrInvalid)
		}
		if strings.TrimSpace(step.Command) == "" || len(step.Command) > execbatch.MaxCommandBytes {
			return fmt.Errorf("%w: step command must be non-empty and at most %d bytes", ErrInvalid, execbatch.MaxCommandBytes)
		}
	}
	if len(input.SessionIDs) == 0 || len(input.SessionIDs) > execbatch.MaxTargets {
		return fmt.Errorf("%w: sessionIds must contain 1-%d targets", ErrInvalid, execbatch.MaxTargets)
	}
	seenTargets := make(map[string]struct{}, len(input.SessionIDs))
	for _, sessionID := range input.SessionIDs {
		if strings.TrimSpace(sessionID) == "" {
			return fmt.Errorf("%w: sessionId is required", ErrInvalid)
		}
		if _, exists := seenTargets[sessionID]; exists {
			return fmt.Errorf("%w: duplicate sessionId %s", ErrInvalid, sessionID)
		}
		seenTargets[sessionID] = struct{}{}
	}
	switch input.ConfirmationPolicy {
	case "", ConfirmEveryStep, ConfirmProductionOnly:
	default:
		return fmt.Errorf("%w: unknown confirmation policy", ErrInvalid)
	}
	if input.TimeoutMS < 0 || input.TimeoutMS > execbatch.MaxTimeout.Milliseconds() {
		return fmt.Errorf("%w: invalid target timeout", ErrInvalid)
	}
	if input.MaxConcurrent < 0 || input.MaxConcurrent > execbatch.MaxConcurrency {
		return fmt.Errorf("%w: invalid concurrency", ErrInvalid)
	}
	return nil
}

func validateID(name, value string, maximum int) error {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s must be 1-%d non-space-padded characters", ErrInvalid, name, maximum)
	}
	return nil
}

func normalizeRenderedCommands(sessionIDs []string, commands []RenderedCommand) ([]RenderedCommand, error) {
	if len(commands) != len(sessionIDs) {
		return nil, fmt.Errorf("%w: rendered command count must match target count", ErrInvalid)
	}
	byTarget := make(map[string]string, len(commands))
	for _, rendered := range commands {
		if _, exists := byTarget[rendered.SessionID]; exists {
			return nil, fmt.Errorf("%w: duplicate rendered target %s", ErrInvalid, rendered.SessionID)
		}
		if strings.TrimSpace(rendered.Command) == "" || len(rendered.Command) > execbatch.MaxCommandBytes ||
			strings.ContainsAny(rendered.Command, "\r\n\x00") {
			return nil, fmt.Errorf("%w: rendered command for %s must be one non-empty line", ErrInvalid, rendered.SessionID)
		}
		byTarget[rendered.SessionID] = rendered.Command
	}
	normalized := make([]RenderedCommand, len(sessionIDs))
	for index, sessionID := range sessionIDs {
		command, exists := byTarget[sessionID]
		if !exists {
			return nil, fmt.Errorf("%w: missing rendered target %s", ErrInvalid, sessionID)
		}
		normalized[index] = RenderedCommand{SessionID: sessionID, Command: command}
	}
	return normalized, nil
}

func buildRequests(state RunState, commands []RenderedCommand) []execbatch.Request {
	groups := make(map[string]int, len(commands))
	requests := make([]execbatch.Request, 0, len(commands))
	for _, rendered := range commands {
		index, exists := groups[rendered.Command]
		if !exists {
			index = len(requests)
			groups[rendered.Command] = index
			requests = append(requests, execbatch.Request{
				BatchID: fmt.Sprintf("%s:s%02d:g%02d", state.RunID, state.CurrentStep, index+1),
				Command: rendered.Command, TimeoutMS: 0, MaxConcurrent: 0,
				Runbook: &execbatch.RunbookMetadata{
					RunbookID: state.RunbookID, RunID: state.RunID,
					StepNumber: state.CurrentStep, StepCount: state.StepCount,
				},
			})
		}
		requests[index].SessionIDs = append(requests[index].SessionIDs, rendered.SessionID)
	}
	return requests
}

func preflightResults(request execbatch.Request, preflight *execbatch.PreflightError) []execbatch.Result {
	failures := make(map[string]error, len(preflight.Failures))
	for _, failure := range preflight.Failures {
		failures[failure.SessionID] = failure.Err
	}
	results := make([]execbatch.Result, len(request.SessionIDs))
	for index, sessionID := range request.SessionIDs {
		message := preflight.Error()
		if err := failures[sessionID]; err != nil {
			message = err.Error()
		}
		results[index] = execbatch.Result{
			SessionID: sessionID, SessionName: sessionID,
			Status: execbatch.StatusTransportError, Error: message,
		}
	}
	return results
}

func orderedResults(sessionIDs []string, results []execbatch.Result) []execbatch.Result {
	byTarget := make(map[string]execbatch.Result, len(results))
	for _, result := range results {
		byTarget[result.SessionID] = result
	}
	ordered := make([]execbatch.Result, 0, len(results))
	seen := make(map[string]struct{}, len(results))
	for _, sessionID := range sessionIDs {
		if result, exists := byTarget[sessionID]; exists {
			ordered = append(ordered, result)
			seen[sessionID] = struct{}{}
		}
	}
	for _, result := range results {
		if _, exists := seen[result.SessionID]; !exists {
			ordered = append(ordered, result)
			seen[result.SessionID] = struct{}{}
		}
	}
	return ordered
}

func appendMissingCanceled(results []execbatch.Result, commands []RenderedCommand) []execbatch.Result {
	seen := make(map[string]struct{}, len(results))
	for _, result := range results {
		seen[result.SessionID] = struct{}{}
	}
	for _, rendered := range commands {
		if _, exists := seen[rendered.SessionID]; exists {
			continue
		}
		results = append(results, execbatch.Result{
			SessionID: rendered.SessionID, SessionName: rendered.SessionID,
			Status: execbatch.StatusCanceled, Error: context.Canceled.Error(),
		})
	}
	return results
}

func resultsCanceled(results []execbatch.Result) bool {
	for _, result := range results {
		if result.Status == execbatch.StatusCanceled {
			return true
		}
	}
	return false
}

func resultsFailed(results []execbatch.Result) bool {
	for _, result := range results {
		if result.Status != execbatch.StatusSuccess {
			return true
		}
	}
	return false
}

func terminalRun(status RunStatus) bool {
	return status == RunSuccess || status == RunFailed || status == RunCanceled
}
