package outcome

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Ledger coordinates durable execution records and their audit finalization.
// It contains no execution retry loop.
type Ledger struct {
	store        Store
	auditor      Auditor
	auditTimeout time.Duration
	now          func() time.Time
}

func New(options Options) (*Ledger, error) {
	if options.Store == nil {
		return nil, errors.New("outcome: store is required")
	}
	if options.Auditor == nil {
		return nil, errors.New("outcome: auditor is required")
	}
	timeout := options.AuditTimeout
	if timeout <= 0 {
		timeout = DefaultAuditTimeout
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Ledger{store: options.Store, auditor: options.Auditor, auditTimeout: timeout, now: now}, nil
}

// Get returns the current record for an idempotence key.
func (l *Ledger) Get(ctx context.Context, key string) (Record, error) {
	if strings.TrimSpace(key) == "" {
		return Record{}, fmt.Errorf("%w: idempotence key is required", ErrInvalidRequest)
	}
	record, err := l.store.Get(ctx, key)
	if err != nil {
		return Record{}, err
	}
	return cloneRecord(record), nil
}

// Reject records an authorization rejection without invoking an effect.
func (l *Ledger) Reject(ctx context.Context, request Request, reason error) (Record, error) {
	record, err := l.recordWithoutAttempt(ctx, request, OutcomeRejected, reason)
	return record, errors.Join(reason, err)
}

// NotAttempted records that no external effect was invoked.
func (l *Ledger) NotAttempted(ctx context.Context, request Request, reason error) (Record, error) {
	record, err := l.recordWithoutAttempt(ctx, request, OutcomeNotAttempted, reason)
	return record, errors.Join(reason, err)
}

// Execute reserves the request, persists the running transition, and invokes
// effect once for that reservation. A duplicate terminal key returns its
// existing record without invoking effect or appending another audit record.
// A duplicate pending key may acquire the reservation through the same
// compare-and-swap transition used by its original caller.
func (l *Ledger) Execute(ctx context.Context, request Request, effect Effect) (Record, error) {
	if effect == nil {
		return Record{}, fmt.Errorf("%w: effect is required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		record, finalizeErr := l.recordPreCanceled(ctx, request, err)
		return record, errors.Join(err, finalizeErr)
	}

	record, err := l.newRecord(request)
	if err != nil {
		return Record{}, err
	}
	stored, reserved, err := l.store.Reserve(ctx, cloneRecord(record))
	if err != nil {
		return record, fmt.Errorf("outcome: reserve record: %w", err)
	}
	record = cloneRecord(stored)
	if !reserved {
		if err := l.checkExisting(record, request); err != nil {
			return record, err
		}
		switch record.State {
		case ExecutionFinished:
			return record, nil
		case ExecutionRunning:
			return record, ErrInProgress
		case ExecutionPending:
		default:
			return record, fmt.Errorf("outcome: stored record has invalid state %q", record.State)
		}
	}

	if err := ctx.Err(); err != nil {
		if !reserved {
			return record, errors.Join(err, ErrInProgress)
		}
		record, finalizeErr := l.finishWithoutAttempt(ctx, record, err)
		return record, errors.Join(err, finalizeErr)
	}
	return l.claimAndExecute(ctx, record, request, effect)
}

func (l *Ledger) claimAndExecute(ctx context.Context, record Record, request Request, effect Effect) (Record, error) {
	expectedRevision := record.Revision
	now := l.now()
	record.State = ExecutionRunning
	record.Outcome = OutcomeUnknown
	record.Result = ExitResult{}
	record.Audit = Audit{State: AuditPending}
	record.StartedAt = &now
	record.FinishedAt = nil
	record.Revision = expectedRevision + 1
	if err := l.store.Update(ctx, cloneRecord(record), expectedRevision); err != nil {
		if errors.Is(err, ErrRevisionConflict) {
			return l.resolveClaimConflict(ctx, record, request, err)
		}
		return record, fmt.Errorf("outcome: persist running state: %w", err)
	}

	if err := ctx.Err(); err != nil {
		record, finalizeErr := l.finishWithoutAttempt(ctx, record, err)
		return record, errors.Join(err, finalizeErr)
	}

	completion, effectErr := effect(ctx)
	outcome, result, completionErr := classifyCompletion(completion, effectErr)
	finishedAt := l.now()
	expectedRevision = record.Revision
	record.State = ExecutionFinished
	record.Outcome = outcome
	record.Result = result
	record.Audit = Audit{State: AuditPending}
	record.FinishedAt = &finishedAt
	record.Revision = expectedRevision + 1
	record, finalizeErr := l.persistTerminalAndAudit(ctx, record, expectedRevision)
	return record, errors.Join(effectErr, completionErr, finalizeErr)
}

func (l *Ledger) recordPreCanceled(ctx context.Context, request Request, reason error) (Record, error) {
	detached, cancel := l.detachedContext(ctx)
	defer cancel()
	return l.recordWithoutAttempt(detached, request, OutcomeNotAttempted, reason)
}

func (l *Ledger) recordWithoutAttempt(ctx context.Context, request Request, outcome Outcome, reason error) (Record, error) {
	record, err := l.newRecord(request)
	if err != nil {
		return Record{}, err
	}
	finishedAt := l.now()
	record.State = ExecutionFinished
	record.Outcome = outcome
	record.Result.Error = errorText(reason)
	record.FinishedAt = &finishedAt

	stored, reserved, err := l.store.Reserve(ctx, cloneRecord(record))
	if err != nil {
		return record, fmt.Errorf("outcome: reserve record: %w", err)
	}
	if !reserved {
		record = cloneRecord(stored)
		if err := l.checkExisting(record, request); err != nil {
			return record, err
		}
		if record.State != ExecutionFinished {
			return record, ErrInProgress
		}
		return record, nil
	}
	return l.finalizeAudit(ctx, cloneRecord(stored))
}

func (l *Ledger) finishWithoutAttempt(ctx context.Context, record Record, reason error) (Record, error) {
	expectedRevision := record.Revision
	finishedAt := l.now()
	record.State = ExecutionFinished
	record.Outcome = OutcomeNotAttempted
	record.Result.Error = errorText(reason)
	record.Audit = Audit{State: AuditPending}
	record.FinishedAt = &finishedAt
	record.Revision = expectedRevision + 1
	return l.persistTerminalAndAudit(ctx, record, expectedRevision)
}

func (l *Ledger) persistTerminalAndAudit(ctx context.Context, record Record, expectedRevision uint64) (Record, error) {
	persistCtx, cancel := l.detachedContext(ctx)
	err := l.store.Update(persistCtx, cloneRecord(record), expectedRevision)
	cancel()
	if err != nil {
		return record, fmt.Errorf("outcome: persist terminal state: %w", err)
	}
	return l.finalizeAudit(ctx, record)
}

func (l *Ledger) finalizeAudit(ctx context.Context, record Record) (Record, error) {
	expectedRevision := record.Revision
	auditCtx, cancel := l.detachedContext(ctx)
	auditErr := l.auditor.Append(auditCtx, cloneRecord(record))
	cancel()

	completedAt := l.now()
	record.Audit.CompletedAt = &completedAt
	if auditErr != nil {
		record.Audit.State = AuditFailed
		record.Audit.Error = auditErr.Error()
	} else {
		record.Audit.State = AuditPersisted
		record.Audit.Error = ""
	}
	record.Revision = expectedRevision + 1

	statusCtx, cancel := l.detachedContext(ctx)
	statusErr := l.store.Update(statusCtx, cloneRecord(record), expectedRevision)
	cancel()
	if auditErr != nil {
		auditErr = fmt.Errorf("outcome: append audit: %w", auditErr)
	}
	if statusErr != nil {
		statusErr = fmt.Errorf("outcome: persist audit state: %w", statusErr)
	}
	return record, errors.Join(auditErr, statusErr)
}

func (l *Ledger) resolveClaimConflict(ctx context.Context, record Record, request Request, claimErr error) (Record, error) {
	stored, err := l.store.Get(ctx, record.IdempotenceKey)
	if err != nil {
		return record, errors.Join(claimErr, err)
	}
	record = cloneRecord(stored)
	if err := l.checkExisting(record, request); err != nil {
		return record, errors.Join(claimErr, err)
	}
	if record.State == ExecutionFinished {
		return record, nil
	}
	return record, errors.Join(claimErr, ErrInProgress)
}

func (l *Ledger) checkExisting(record Record, request Request) error {
	canonical, err := CanonicalizeArguments(request.Arguments)
	if err != nil {
		return fmt.Errorf("%w: canonical arguments: %v", ErrInvalidRequest, err)
	}
	if record.IdempotenceKey != request.IdempotenceKey ||
		record.AuthorizationID != request.AuthorizationID ||
		record.Kind != request.Kind ||
		!bytes.Equal(record.CanonicalArguments, canonical) {
		return ErrIdempotenceConflict
	}
	return nil
}

func (l *Ledger) newRecord(request Request) (Record, error) {
	if strings.TrimSpace(request.IdempotenceKey) == "" {
		return Record{}, fmt.Errorf("%w: idempotence key is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(request.AuthorizationID) == "" {
		return Record{}, fmt.Errorf("%w: authorization id is required", ErrInvalidRequest)
	}
	switch request.Kind {
	case KindCommand, KindFile, KindDatabase:
	default:
		return Record{}, fmt.Errorf("%w: unsupported kind %q", ErrInvalidRequest, request.Kind)
	}
	canonical, err := CanonicalizeArguments(request.Arguments)
	if err != nil {
		return Record{}, fmt.Errorf("%w: canonical arguments: %v", ErrInvalidRequest, err)
	}
	return Record{
		IdempotenceKey:     request.IdempotenceKey,
		AuthorizationID:    request.AuthorizationID,
		Kind:               request.Kind,
		CanonicalArguments: canonical,
		State:              ExecutionPending,
		Outcome:            OutcomeNotAttempted,
		Audit:              Audit{State: AuditPending},
		Revision:           1,
		CreatedAt:          l.now(),
	}, nil
}

func (l *Ledger) detachedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), l.auditTimeout)
}

func classifyCompletion(completion Completion, effectErr error) (Outcome, ExitResult, error) {
	result := ExitResult{ExitCode: cloneInt(completion.ExitCode), Error: errorText(effectErr)}
	outcome := completion.Outcome
	if outcome == "" {
		switch {
		case effectErr != nil:
			outcome = OutcomeUnknown
		case result.ExitCode != nil && *result.ExitCode != 0:
			outcome = OutcomeFailed
		default:
			outcome = OutcomeAccepted
		}
	}

	var completionErr error
	switch outcome {
	case OutcomeAccepted:
		if effectErr != nil || (result.ExitCode != nil && *result.ExitCode != 0) {
			completionErr = fmt.Errorf("%w: accepted result has an error or nonzero exit code", ErrInvalidCompletion)
		}
	case OutcomeFailed:
		if effectErr == nil && (result.ExitCode == nil || *result.ExitCode == 0) {
			completionErr = fmt.Errorf("%w: failed result needs an error or nonzero exit code", ErrInvalidCompletion)
		}
	case OutcomeUnknown:
	default:
		completionErr = fmt.Errorf("%w: %q is not valid after an attempt", ErrInvalidCompletion, outcome)
	}
	if completionErr != nil {
		outcome = OutcomeUnknown
		result.Error = errors.Join(effectErr, completionErr).Error()
	}
	return outcome, result, completionErr
}

func cloneRecord(record Record) Record {
	record.CanonicalArguments = bytes.Clone(record.CanonicalArguments)
	record.Result.ExitCode = cloneInt(record.Result.ExitCode)
	record.StartedAt = cloneTime(record.StartedAt)
	record.FinishedAt = cloneTime(record.FinishedAt)
	record.Audit.CompletedAt = cloneTime(record.Audit.CompletedAt)
	return record
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
