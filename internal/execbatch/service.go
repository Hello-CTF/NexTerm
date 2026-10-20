package execbatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type Auditor interface {
	AuditInsert(context.Context, store.AuditInput) error
}

const (
	pendingCancelTTL          = 10 * time.Second
	maxPendingCancels         = 128
	maxPendingCancelsPerOwner = 16
)

type Config struct {
	Resolver    Resolver
	Auditor     Auditor
	History     History
	Now         func() time.Time
	OnError     func(error)
	OutputLimit int64
	// ProductionSettings reads the per-owner production environment
	// configuration; nil classifies with the built-in production names only.
	ProductionSettings guard.Settings
}

type Service struct {
	resolver           Resolver
	auditor            Auditor
	history            History
	now                func() time.Time
	onError            func(error)
	outputLimit        int64
	productionSettings guard.Settings

	beforeCleanupCommit   func(accountCleanupKey) error
	mu                    sync.Mutex
	batches               map[batchKey]*activeBatch
	pendingCancels        map[pendingCancelKey]time.Time
	accountCleanups       map[accountCleanupKey]*accountCleanupEvent
	accountCleanupAliases map[accountCleanupKey]accountCleanupKey
}

type batchKey struct {
	ownerID string
	batchID string
}

type activeBatch struct {
	ownerID    string
	cancel     context.CancelFunc
	canceling  bool
	executions sync.WaitGroup
}

type pendingCancelKey struct {
	ownerID string
	batchID string
}

type accountCleanupKey struct {
	ownerID string
	eventID string
}

type auditPayload struct {
	OwnerID       string `json:"ownerId,omitempty"`
	BatchID       string `json:"batchId"`
	Command       string `json:"command,omitempty"`
	CommandHash   string `json:"commandHash,omitempty"`
	TargetSetHash string `json:"targetSetHash,omitempty"`
	Environment   string `json:"environment,omitempty"`
	Status        Status `json:"status"`
	Error         string `json:"error,omitempty"`
	Truncated     bool   `json:"truncated"`
}

func New(config Config) *Service {
	outputLimit := config.OutputLimit
	if outputLimit <= 0 {
		outputLimit = DefaultOutputLimit
	}
	if outputLimit > MaxOutputLimit {
		outputLimit = MaxOutputLimit
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	onError := config.OnError
	if onError == nil {
		onError = func(err error) { slog.Warn("exec batch audit/history failed", "error", err) }
	}
	return &Service{
		resolver: config.Resolver, auditor: config.Auditor, history: config.History, now: now, onError: onError,
		outputLimit: outputLimit, batches: make(map[batchKey]*activeBatch),
		pendingCancels:        make(map[pendingCancelKey]time.Time),
		accountCleanups:       make(map[accountCleanupKey]*accountCleanupEvent),
		accountCleanupAliases: make(map[accountCleanupKey]accountCleanupKey),
		productionSettings:    config.ProductionSettings,
	}
}

func (s *Service) Execute(ctx context.Context, ownerID string, request Request) (Batch, error) {
	timeout, concurrency, err := validateRequest(request)
	if err != nil {
		return Batch{}, err
	}
	if s.resolver == nil {
		return Batch{}, fmt.Errorf("%w: no session resolver", ErrUnsupported)
	}

	runCtx, cancel := context.WithCancel(ctx)
	active := &activeBatch{ownerID: ownerID, cancel: cancel}
	key := batchKey{ownerID: ownerID, batchID: request.BatchID}
	pendingKey := pendingCancelKey{ownerID: ownerID, batchID: request.BatchID}
	err = admit(ctx, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.purgePendingCancelsLocked(s.now())
		if _, exists := s.batches[key]; exists {
			return fmt.Errorf("%w: %s", ErrBatchConflict, request.BatchID)
		}
		s.batches[key] = active
		if _, exists := s.pendingCancels[pendingKey]; exists {
			delete(s.pendingCancels, pendingKey)
			active.canceling = true
			cancel()
		}
		return nil
	})
	if err != nil {
		cancel()
		return Batch{}, err
	}
	defer func() {
		s.mu.Lock()
		if s.batches[key] == active {
			delete(s.batches, key)
		}
		s.mu.Unlock()
		cancel()
	}()

	started := s.now()
	targets, failures := s.preflight(runCtx, request.SessionIDs)
	classification, err := s.classifyProductionTargets(ctx, ownerID, targets)
	if err != nil {
		return Batch{}, fmt.Errorf("classify production targets: %w", err)
	}
	if runCtx.Err() != nil {
		return s.finishWithHistory(runCtx, ownerID, request, started, targets, canceledResults(request.SessionIDs, targets), classification.production()), nil
	}
	if len(failures) > 0 {
		preflightErr := &PreflightError{Failures: failures}
		if request.Runbook != nil {
			batch := s.finishWithHistory(runCtx, ownerID, request, started, targets, preflightFailureResults(request.SessionIDs, targets, failures), classification.production())
			return batch, preflightErr
		}
		return Batch{}, preflightErr
	}
	challenge := newProductionChallenge(request.BatchID, ownerID, request.Command, targets, classification)
	if err := validateProductionConfirmation(request.ProductionConfirmation, challenge); err != nil {
		return Batch{}, err
	}
	if err := s.auditProductionConfirmation(runCtx, ownerID, request, challenge); err != nil {
		return Batch{}, fmt.Errorf("record exec batch production confirmation: %w", err)
	}
	s.startHistory(runCtx, ownerID, request, started, targets, classification.production())

	results := make([]Result, len(targets))
	jobs := make(chan int, len(targets))
	for index := range targets {
		jobs <- index
	}
	close(jobs)
	if concurrency > len(targets) {
		concurrency = len(targets)
	}
	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				target := targets[index]
				if !s.beginExecution(runCtx, active) {
					results[index] = canceledResult(target)
					continue
				}
				results[index] = func() Result {
					defer active.executions.Done()
					return s.executeTarget(runCtx, ownerID, request, target, timeout, challenge)
				}()
			}
		}()
	}
	workers.Wait()
	return s.finishWithHistory(runCtx, ownerID, request, started, targets, results, classification.production()), nil
}

// Cancel sets canceling under this same mutex before waiting, so no Add can race its Wait.
func (s *Service) beginExecution(ctx context.Context, active *activeBatch) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if active.canceling || ctx.Err() != nil {
		return false
	}
	active.executions.Add(1)
	return true
}

func (s *Service) Cancel(_ context.Context, ownerID, batchID string) (CancelResult, error) {
	if strings.TrimSpace(batchID) == "" {
		return CancelResult{}, fmt.Errorf("%w: batchId is required", ErrInvalid)
	}
	s.mu.Lock()
	s.purgePendingCancelsLocked(s.now())
	active := s.batches[batchKey{ownerID: ownerID, batchID: batchID}]
	if active == nil {
		key := pendingCancelKey{ownerID: ownerID, batchID: batchID}
		if _, exists := s.pendingCancels[key]; !exists {
			if len(s.pendingCancels) >= maxPendingCancels || s.pendingCancelOwnerCountLocked(ownerID) >= maxPendingCancelsPerOwner {
				s.mu.Unlock()
				return CancelResult{}, fmt.Errorf("%w: %s", ErrPendingCancelLimit, batchID)
			}
			s.pendingCancels[key] = s.now().Add(pendingCancelTTL)
		}
		s.mu.Unlock()
		return CancelResult{BatchID: batchID, Canceled: true}, nil
	}
	active.canceling = true
	cancel := active.cancel
	s.mu.Unlock()
	cancel()
	active.executions.Wait()
	return CancelResult{BatchID: batchID, Canceled: true}, nil
}

type accountCleanupEvent struct {
	done      chan struct{}
	batches   []*activeBatch
	committed bool
	aborted   bool
}

// PrepareOwnerCleanup reserves an event and its current owner snapshot without canceling it; commit and rollback are mutually exclusive.
func (s *Service) PrepareOwnerCleanup(_ context.Context, ownerID, eventID string) (func(...string), func()) {
	if ownerID == "" || eventID == "" {
		return func(...string) {}, func() {}
	}
	key := accountCleanupKey{ownerID: ownerID, eventID: eventID}
	s.mu.Lock()
	if canonical, exists := s.accountCleanupAliases[key]; exists {
		key = canonical
	}
	event, exists := s.accountCleanups[key]
	if !exists {
		event = &accountCleanupEvent{done: make(chan struct{}), batches: make([]*activeBatch, 0)}
		for _, active := range s.batches {
			if active.ownerID == ownerID {
				event.batches = append(event.batches, active)
			}
		}
		s.accountCleanups[key] = event
	}
	s.mu.Unlock()
	return func(sessionIDs ...string) { s.commitOwnerCleanup(key, event, sessionIDs...) }, func() { s.abortOwnerCleanup(key, event) }
}

// CancelOwner takes one snapshot per cleanup event. Optional revoked session IDs alias a shared event, and replays wait without another snapshot.
func (s *Service) CancelOwner(ctx context.Context, ownerID, eventID string, sessionIDs ...string) {
	commit, _ := s.PrepareOwnerCleanup(ctx, ownerID, eventID)
	commit(sessionIDs...)
}

func (s *Service) commitOwnerCleanup(key accountCleanupKey, event *accountCleanupEvent, sessionIDs ...string) {
	s.mu.Lock()
	current, exists := s.accountCleanups[key]
	if !exists || current != event || event.aborted {
		s.mu.Unlock()
		return
	}
	if s.beforeCleanupCommit != nil {
		if err := s.beforeCleanupCommit(key); err != nil {
			s.mu.Unlock()
			s.abortOwnerCleanup(key, event)
			return
		}
	}
	for _, sessionID := range sessionIDs {
		if sessionID == "" || sessionID == key.eventID {
			continue
		}
		alias := accountCleanupKey{ownerID: key.ownerID, eventID: sessionID}
		if _, exists := s.accountCleanupAliases[alias]; !exists {
			s.accountCleanupAliases[alias] = key
		}
	}
	if event.committed {
		done := event.done
		s.mu.Unlock()
		<-done
		return
	}
	event.committed = true
	for _, active := range event.batches {
		active.canceling = true
	}
	s.mu.Unlock()
	for _, active := range event.batches {
		active.cancel()
	}
	for _, active := range event.batches {
		active.executions.Wait()
	}
	close(event.done)
}

func (s *Service) abortOwnerCleanup(key accountCleanupKey, event *accountCleanupEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.accountCleanups[key]
	if !exists || current != event || event.aborted || event.committed {
		return
	}
	event.aborted = true
	delete(s.accountCleanups, key)
	for alias, canonical := range s.accountCleanupAliases {
		if canonical == key {
			delete(s.accountCleanupAliases, alias)
		}
	}
	close(event.done)
}

func (s *Service) purgePendingCancelsLocked(now time.Time) {
	for key, expiresAt := range s.pendingCancels {
		if !expiresAt.After(now) {
			delete(s.pendingCancels, key)
		}
	}
}

func (s *Service) pendingCancelOwnerCountLocked(ownerID string) int {
	count := 0
	for key := range s.pendingCancels {
		if key.ownerID == ownerID {
			count++
		}
	}
	return count
}

func (s *Service) preflight(ctx context.Context, sessionIDs []string) ([]Target, []TargetFailure) {
	targets := make([]Target, len(sessionIDs))
	var failures []TargetFailure
	for index, sessionID := range sessionIDs {
		targets[index] = Target{SessionID: sessionID, SessionName: sessionID}
		target, err := s.resolver.Resolve(ctx, sessionID)
		if err != nil {
			failures = append(failures, TargetFailure{SessionID: sessionID, Err: err})
			continue
		}
		if target.Transport == nil || !target.Transport.IsAlive() {
			failures = append(failures, TargetFailure{SessionID: sessionID, Err: ErrDisconnected})
			continue
		}
		target.SessionID = sessionID
		if target.SessionName == "" {
			target.SessionName = sessionID
		}
		targets[index] = target
	}
	return targets, failures
}

func (s *Service) executeTarget(ctx context.Context, ownerID string, request Request, target Target, timeout time.Duration, challenge *productionChallenge) Result {
	result := s.executeTargetTransport(ctx, request, target, timeout)
	s.audit(ctx, ownerID, request, result, target.Environment, challenge)
	return result
}

func (s *Service) executeTargetTransport(ctx context.Context, request Request, target Target, timeout time.Duration) Result {
	started := s.now()
	result := Result{
		SessionID: target.SessionID, SessionName: target.SessionName, AssetID: target.AssetID,
		startedAt: started, environment: target.Environment,
	}
	opCtx, opCancel := context.WithTimeout(ctx, timeout)
	execResult, execErr := target.Transport.Exec(opCtx, request.Command, base.ExecOptions{
		Timeout: timeout,
		Limits: base.OutputLimits{
			Stdout: s.outputLimit,
			Stderr: s.outputLimit,
		},
		ExpectedGeneration: target.Generation,
	})
	opErr := opCtx.Err()
	opCancel()
	result.finishedAt = s.now()
	result.DurationMS = result.finishedAt.Sub(started).Milliseconds()
	result.Stdout, result.Stderr, result.Truncated = capOutputs(execResult, s.outputLimit)
	result.ExitCode = execResult.ExitCode
	result.Signal = execResult.Signal
	exitCode, remoteExit := base.ExitCode(execErr)
	if remoteExit {
		result.ExitCode = &exitCode
		var exitErr *base.ExitError
		if errors.As(execErr, &exitErr) {
			result.Signal = exitErr.Signal
		}
	}

	switch {
	case opErr != nil:
		result.Error = opErr.Error()
		if errors.Is(opErr, context.DeadlineExceeded) {
			result.Status = StatusTimeout
		} else {
			result.Status = StatusCanceled
		}
	case execErr == nil && execResult.ExitCode == nil:
		result.Status = StatusTransportError
		result.Error = base.ErrExitStatusMissing.Error()
	case execErr == nil && *execResult.ExitCode == 0:
		result.Status = StatusSuccess
	case execErr == nil:
		result.Status = StatusExitError
	case remoteExit:
		result.Status = StatusExitError
	default:
		result.Error = execErr.Error()
		switch {
		case errors.Is(execErr, context.DeadlineExceeded):
			result.Status = StatusTimeout
		case errors.Is(execErr, context.Canceled):
			result.Status = StatusCanceled
		default:
			result.Status = StatusTransportError
		}
	}
	return result
}

func (s *Service) audit(ctx context.Context, ownerID string, request Request, result Result, environment string, challenge *productionChallenge) {
	if s.auditor == nil {
		return
	}
	var exitCode *int32
	if result.ExitCode != nil {
		code := int32(*result.ExitCode)
		exitCode = &code
	}
	duration := result.DurationMS
	payload := auditPayload{
		BatchID: request.BatchID, Command: request.Command, Status: result.Status,
		Error: result.Error, Truncated: result.Truncated,
	}
	if challenge != nil {
		payload.OwnerID = ownerID
		payload.Command = ""
		payload.CommandHash = challenge.commandHash
		payload.TargetSetHash = challenge.targetSetHash
		payload.Environment = environment
	}
	auditCtx, cancel := context.WithTimeout(execAuditContext(ctx, ownerID), 5*time.Second)
	defer cancel()
	err := s.auditor.AuditInsert(auditCtx, store.AuditInput{
		SessionID: stringPointer(result.SessionID), AssetID: stringPointer(result.AssetID),
		Source: "user", Kind: "exec_batch",
		Payload: payload, ExitCode: exitCode, DurationMS: &duration,
	})
	if err != nil {
		s.onError(fmt.Errorf("record exec batch %s target %s: %w", request.BatchID, result.SessionID, err))
	}
}

func (s *Service) finishedBatch(request Request, started time.Time, results []Result) Batch {
	return Batch{
		BatchID: request.BatchID, Command: request.Command,
		StartedAt: started.UnixMilli(), FinishedAt: s.now().UnixMilli(), Results: results,
	}
}

func canceledResults(sessionIDs []string, targets []Target) []Result {
	results := make([]Result, len(sessionIDs))
	for index, sessionID := range sessionIDs {
		target := targets[index]
		if target.SessionID == "" {
			target.SessionID = sessionID
			target.SessionName = sessionID
		}
		results[index] = canceledResult(target)
	}
	return results
}

func canceledResult(target Target) Result {
	return Result{
		SessionID: target.SessionID, SessionName: target.SessionName, AssetID: target.AssetID,
		Status: StatusCanceled, Error: context.Canceled.Error(), environment: target.Environment,
	}
}

func preflightFailureResults(sessionIDs []string, targets []Target, failures []TargetFailure) []Result {
	failureByTarget := make(map[string]error, len(failures))
	for _, failure := range failures {
		failureByTarget[failure.SessionID] = failure.Err
	}
	results := make([]Result, len(sessionIDs))
	for index, sessionID := range sessionIDs {
		target := targets[index]
		if target.SessionID == "" {
			target.SessionID = sessionID
			target.SessionName = sessionID
		}
		message := "exec batch preflight aborted before execution"
		if err := failureByTarget[sessionID]; err != nil {
			message = err.Error()
		}
		results[index] = Result{
			SessionID: target.SessionID, SessionName: target.SessionName, AssetID: target.AssetID,
			Status: StatusTransportError, Error: message, environment: target.Environment,
		}
	}
	return results
}

func capOutputs(result base.ExecResult, limit int64) (string, string, bool) {
	stdout, stdoutTruncated := capOutput(result.Stdout, limit)
	stderr, stderrTruncated := capOutput(result.Stderr, limit)
	return stdout, stderr, result.Truncated || stdoutTruncated || stderrTruncated
}

func capOutput(output string, limit int64) (string, bool) {
	if int64(len(output)) <= limit {
		return output, false
	}
	end := int(limit)
	for end > 0 && !utf8.RuneStart(output[end]) {
		end--
	}
	return output[:end], true
}

func validateRequest(request Request) (time.Duration, int, error) {
	if request.BatchID == "" || len(request.BatchID) > 128 || strings.TrimSpace(request.BatchID) != request.BatchID {
		return 0, 0, fmt.Errorf("%w: batchId must be 1-128 non-space-padded characters", ErrInvalid)
	}
	if len(request.SessionIDs) == 0 || len(request.SessionIDs) > MaxTargets {
		return 0, 0, fmt.Errorf("%w: sessionIds must contain 1-%d targets", ErrInvalid, MaxTargets)
	}
	seen := make(map[string]struct{}, len(request.SessionIDs))
	for _, sessionID := range request.SessionIDs {
		if strings.TrimSpace(sessionID) == "" {
			return 0, 0, fmt.Errorf("%w: sessionId is required", ErrInvalid)
		}
		if _, exists := seen[sessionID]; exists {
			return 0, 0, fmt.Errorf("%w: duplicate sessionId %s", ErrInvalid, sessionID)
		}
		seen[sessionID] = struct{}{}
	}
	if strings.TrimSpace(request.Command) == "" || len(request.Command) > MaxCommandBytes || strings.ContainsAny(request.Command, "\r\n\x00") {
		return 0, 0, fmt.Errorf("%w: command must be one non-empty line of at most %d bytes", ErrInvalid, MaxCommandBytes)
	}
	if request.Runbook != nil {
		metadata := request.Runbook
		if metadata.RunbookID == "" || len(metadata.RunbookID) > 128 || strings.TrimSpace(metadata.RunbookID) != metadata.RunbookID ||
			metadata.RunID == "" || len(metadata.RunID) > 128 || strings.TrimSpace(metadata.RunID) != metadata.RunID {
			return 0, 0, fmt.Errorf("%w: invalid runbook history identity", ErrInvalid)
		}
		if metadata.StepNumber < 1 || metadata.StepCount < metadata.StepNumber || metadata.StepCount > 64 {
			return 0, 0, fmt.Errorf("%w: invalid runbook history step", ErrInvalid)
		}
		if metadata.GroupNumber < 1 || metadata.GroupCount < metadata.GroupNumber || metadata.GroupCount > 64 {
			return 0, 0, fmt.Errorf("%w: invalid runbook history command group", ErrInvalid)
		}
	}
	if request.TimeoutMS < 0 || request.TimeoutMS > MaxTimeout.Milliseconds() {
		return 0, 0, fmt.Errorf("%w: timeoutMs must be between 0 and %d", ErrInvalid, MaxTimeout.Milliseconds())
	}
	timeout := DefaultTimeout
	if request.TimeoutMS > 0 {
		timeout = time.Duration(request.TimeoutMS) * time.Millisecond
	}
	if request.MaxConcurrent < 0 || request.MaxConcurrent > MaxConcurrency {
		return 0, 0, fmt.Errorf("%w: maxConcurrent must be between 0 and %d", ErrInvalid, MaxConcurrency)
	}
	concurrency := request.MaxConcurrent
	if concurrency == 0 {
		concurrency = DefaultConcurrency
	}
	return timeout, concurrency, nil
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
