// Package outcome records execution and audit outcomes for external side effects.
package outcome

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Kind identifies the external system affected by an operation.
type Kind string

const (
	KindCommand  Kind = "command"
	KindFile     Kind = "file"
	KindDatabase Kind = "database"
)

// ExecutionState is the durable lifecycle of an operation.
type ExecutionState string

const (
	ExecutionPending  ExecutionState = "pending"
	ExecutionRunning  ExecutionState = "running"
	ExecutionFinished ExecutionState = "finished"
)

// Outcome describes what is known about the requested external effect.
type Outcome string

const (
	OutcomeRejected     Outcome = "rejected"
	OutcomeNotAttempted Outcome = "not_attempted"
	OutcomeFailed       Outcome = "failed"
	OutcomeAccepted     Outcome = "accepted"
	OutcomeUnknown      Outcome = "unknown"
)

// AuditState describes the persistence of a terminal record in the audit log.
type AuditState string

const (
	AuditPending   AuditState = "pending"
	AuditPersisted AuditState = "persisted"
	AuditFailed    AuditState = "failed"
)

var (
	ErrInvalidRequest      = errors.New("outcome: invalid request")
	ErrInvalidCompletion   = errors.New("outcome: invalid effect completion")
	ErrIdempotenceConflict = errors.New("outcome: idempotence key conflict")
	ErrInProgress          = errors.New("outcome: effect is already in progress")
	ErrNotFound            = errors.New("outcome: record not found")
	ErrRevisionConflict    = errors.New("outcome: record revision conflict")
)

// Request identifies an operation and the authorization that covers its
// canonical arguments. Arguments must contain one JSON value.
type Request struct {
	IdempotenceKey  string          `json:"idempotenceKey"`
	AuthorizationID string          `json:"authorizationId"`
	Kind            Kind            `json:"kind"`
	Arguments       json.RawMessage `json:"arguments"`
}

// ExitResult is the observable process-style result. ExitCode is nil for
// operations that do not have process exit semantics.
type ExitResult struct {
	ExitCode *int   `json:"exitCode,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Audit tracks the terminal record's audit persistence independently of the
// execution outcome.
type Audit struct {
	State       AuditState `json:"state"`
	Error       string     `json:"error,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

// Record is the durable outcome snapshot. Revision is incremented after every
// successful transition.
type Record struct {
	IdempotenceKey     string          `json:"idempotenceKey"`
	AuthorizationID    string          `json:"authorizationId"`
	Kind               Kind            `json:"kind"`
	CanonicalArguments json.RawMessage `json:"canonicalArguments"`
	State              ExecutionState  `json:"state"`
	Outcome            Outcome         `json:"outcome"`
	Result             ExitResult      `json:"result"`
	Audit              Audit           `json:"audit"`
	Revision           uint64          `json:"revision"`
	CreatedAt          time.Time       `json:"createdAt"`
	StartedAt          *time.Time      `json:"startedAt,omitempty"`
	FinishedAt         *time.Time      `json:"finishedAt,omitempty"`
}

// Completion is the result reported by an Effect. The empty Outcome accepts a
// nil error, maps a nonzero exit code to OutcomeFailed, and maps an error to
// OutcomeUnknown. An Effect must explicitly report OutcomeFailed when an error
// is known not to leave an ambiguous external result.
type Completion struct {
	Outcome  Outcome `json:"outcome,omitempty"`
	ExitCode *int    `json:"exitCode,omitempty"`
}

// Effect performs the external operation. It is called only after the running
// state has been persisted.
type Effect func(context.Context) (Completion, error)

// Store provides atomic reservation and optimistic record transitions.
// Implementations must be safe for concurrent use.
type Store interface {
	// Reserve atomically stores a new record by idempotence key. If the key is
	// already present, it returns the current record and reserved is false.
	Reserve(ctx context.Context, record Record) (stored Record, reserved bool, err error)
	// Update replaces a record only when its stored revision equals
	// expectedRevision. The new record must have revision expectedRevision+1.
	// It returns ErrRevisionConflict when the stored revision does not match.
	Update(ctx context.Context, record Record, expectedRevision uint64) error
	// Get returns the current record for an idempotence key or ErrNotFound.
	Get(ctx context.Context, key string) (Record, error)
}

// Auditor persists a terminal outcome record. Audit.State is AuditPending on
// every Append call; the ledger stores the result of that call separately.
type Auditor interface {
	Append(ctx context.Context, record Record) error
}

// Options configures a Ledger. AuditTimeout bounds each detached persistence
// operation. Non-positive values select DefaultAuditTimeout.
type Options struct {
	Store        Store
	Auditor      Auditor
	AuditTimeout time.Duration
	Now          func() time.Time
}

const DefaultAuditTimeout = 5 * time.Second
