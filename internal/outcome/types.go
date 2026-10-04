package outcome

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type Kind string

const (
	KindCommand  Kind = "command"
	KindFile     Kind = "file"
	KindDatabase Kind = "database"
)

type ExecutionState string

const (
	ExecutionPending  ExecutionState = "pending"
	ExecutionRunning  ExecutionState = "running"
	ExecutionFinished ExecutionState = "finished"
)

type Outcome string

const (
	OutcomeRejected     Outcome = "rejected"
	OutcomeNotAttempted Outcome = "not_attempted"
	OutcomeFailed       Outcome = "failed"
	OutcomeAccepted     Outcome = "accepted"
	OutcomeUnknown      Outcome = "unknown"
)

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

type Request struct {
	IdempotenceKey  string          `json:"idempotenceKey"`
	AuthorizationID string          `json:"authorizationId"`
	Kind            Kind            `json:"kind"`
	Arguments       json.RawMessage `json:"arguments"`
}

type ExitResult struct {
	ExitCode *int   `json:"exitCode,omitempty"`
	Error    string `json:"error,omitempty"`
}

type Audit struct {
	State       AuditState `json:"state"`
	Error       string     `json:"error,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

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

type Completion struct {
	Outcome  Outcome `json:"outcome,omitempty"`
	ExitCode *int    `json:"exitCode,omitempty"`
}

type Effect func(context.Context) (Completion, error)

type Store interface {
	Reserve(ctx context.Context, record Record) (stored Record, reserved bool, err error)
	Update(ctx context.Context, record Record, expectedRevision uint64) error
	Get(ctx context.Context, key string) (Record, error)
}

type Auditor interface {
	Append(ctx context.Context, record Record) error
}

type Options struct {
	Store        Store
	Auditor      Auditor
	AuditTimeout time.Duration
	Now          func() time.Time
}

const DefaultAuditTimeout = 5 * time.Second
