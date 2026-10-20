package hitl

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/cloudwego/eino/adk"
)

type Kind string

const (
	KindConfirm  Kind = "confirm"
	KindQuestion Kind = "question"
)

type Decision string

const (
	DecisionAllow        Decision = "allow"
	DecisionAllowSession Decision = "allow_session"
	DecisionAllowPersist Decision = "allow_persistent"
	DecisionDeny         Decision = "deny"
)

type RunStatus string

const (
	RunStatusRunning     RunStatus = "running"
	RunStatusInterrupted RunStatus = "interrupted"
	RunStatusCompleted   RunStatus = "completed"
	RunStatusCanceled    RunStatus = "canceled"
	RunStatusFailed      RunStatus = "failed"
	RunStatusExpired     RunStatus = "expired"
)

type TerminalReason string

const (
	TerminalCompleted   TerminalReason = "completed"
	TerminalCanceled    TerminalReason = "canceled"
	TerminalInterrupted TerminalReason = "interrupted"
	TerminalFailed      TerminalReason = "failed"
	TerminalExpired     TerminalReason = "expired"
)

type EventKind string

const (
	EventInterrupted EventKind = "interrupted"
	EventResumed     EventKind = "resumed"
	EventTerminal    EventKind = "terminal"
)

var (
	ErrInvalidArgument    = errors.New("hitl: invalid argument")
	ErrManagerClosed      = errors.New("hitl: manager is closed")
	ErrRunNotFound        = errors.New("hitl: run not found")
	ErrRunExists          = errors.New("hitl: run already exists")
	ErrRunFinished        = errors.New("hitl: run is already terminal")
	ErrRunNotInterrupted  = errors.New("hitl: run is not waiting for an answer")
	ErrCheckpointNotFound = errors.New("hitl: checkpoint not found")
	ErrCheckpointChanged  = errors.New("hitl: checkpoint binding changed")
	ErrRequestNotFound    = errors.New("hitl: interrupt request not found")
	ErrRequestExpired     = errors.New("hitl: interrupt request expired")
	ErrRequestConsumed    = errors.New("hitl: interrupt request already consumed")
	ErrRequestStale       = errors.New("hitl: interrupt request is stale")
	ErrInvalidNonce       = errors.New("hitl: nonce does not match the interrupt request")
	ErrParametersChanged  = errors.New("hitl: tool parameters changed")
	ErrAnswerReused       = errors.New("hitl: answer ID belongs to another request")
	ErrAssetMismatch      = errors.New("hitl: asset identity does not match the request evidence")
	ErrAssetUnknown       = errors.New("hitl: asset identity evidence is unavailable")
	// ErrRestorePersist reports a retryable persistence failure during
	// restore: the in-memory registration was rolled back and the persisted
	// blob, run row, and checkpoint stay untouched for the next recovery.
	ErrRestorePersist = errors.New("hitl: restore persist failed")
)

type Question struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Options []string `json:"options,omitempty"`
}

type InterruptInput struct {
	RunID        string          `json:"runId"`
	CheckpointID string          `json:"checkpointId"`
	CallID       string          `json:"callId"`
	Tool         string          `json:"tool"`
	Kind         Kind            `json:"kind"`
	Parameters   json.RawMessage `json:"parameters"`
	Question     *Question       `json:"question,omitempty"`
}

type Interrupt struct {
	ID             string          `json:"id"`
	RunID          string          `json:"runId"`
	CheckpointID   string          `json:"checkpointId"`
	CheckpointHash string          `json:"checkpointHash"`
	TargetID       string          `json:"targetId"`
	CallID         string          `json:"callId"`
	Tool           string          `json:"tool"`
	Kind           Kind            `json:"kind"`
	Parameters     json.RawMessage `json:"parameters"`
	ParameterHash  string          `json:"parameterHash"`
	Question       *Question       `json:"question,omitempty"`
	Nonce          string          `json:"nonce"`
	CreatedAt      time.Time       `json:"createdAt"`
	ExpiresAt      time.Time       `json:"expiresAt"`
	Attempt        uint64          `json:"attempt"`
	Sequence       uint64          `json:"seq"`
	// AssetID is the real asset identity the request evidence was produced on,
	// resolved server-side; empty means unknown.
	AssetID string `json:"assetId,omitempty"`
}

type Answer struct {
	ID           string          `json:"id"`
	RunID        string          `json:"runId"`
	RequestID    string          `json:"requestId"`
	CheckpointID string          `json:"checkpointId"`
	TargetID     string          `json:"targetId"`
	CallID       string          `json:"callId"`
	Nonce        string          `json:"nonce"`
	Parameters   json.RawMessage `json:"parameters"`
	Decision     Decision        `json:"decision,omitempty"`
	Text         string          `json:"text,omitempty"`
	// AssetID is the answerer's echo of the asset identity the confirmation was
	// presented for; it is a claim checked against the request evidence, never
	// trusted as the source of truth.
	AssetID string `json:"assetId,omitempty"`
}

type Resume struct {
	ID           string `json:"id"`
	AnswerID     string `json:"answerId"`
	RequestID    string `json:"requestId"`
	RunID        string `json:"runId"`
	CheckpointID string `json:"checkpointId"`
	TargetID     string `json:"targetId"`
	Attempt      uint64 `json:"attempt"`
	Sequence     uint64 `json:"seq"`
	// AssetID is the verified asset evidence the resume was authorized against.
	AssetID string `json:"assetId,omitempty"`
}

type Event struct {
	RunID        string         `json:"runId"`
	CheckpointID string         `json:"checkpointId"`
	RequestID    string         `json:"requestId,omitempty"`
	Kind         EventKind      `json:"kind"`
	Reason       TerminalReason `json:"reason"`
	Message      string         `json:"message,omitempty"`
	Attempt      uint64         `json:"attempt"`
	Sequence     uint64         `json:"seq"`
	AssetID      string         `json:"assetId,omitempty"`
}

type Snapshot struct {
	RunID        string      `json:"runId"`
	CheckpointID string      `json:"checkpointId"`
	Status       RunStatus   `json:"status"`
	Attempt      uint64      `json:"attempt"`
	Sequence     uint64      `json:"seq"`
	Pending      []Interrupt `json:"pending"`
	Terminal     *Event      `json:"terminal,omitempty"`
}

const (
	DefaultTTL               = 5 * time.Minute
	DefaultTerminalRetention = time.Hour
)

type Timer interface {
	Stop() bool
}

type Config struct {
	Checkpoints       adk.CheckPointStore
	Store             Store
	TTL               time.Duration
	TerminalRetention time.Duration
	Now               func() time.Time
	AfterFunc         func(time.Duration, func()) Timer
	NewNonce          func() (string, error)
	// AssetResolver resolves the run's current real asset identity from
	// server-side state; it is the only source of asset evidence. When nil,
	// asset binding is disabled and all asset identities remain unknown.
	// It is called with the run lock held and must not call back into the Manager.
	AssetResolver func(context.Context, string) (string, error)
}

type Resumer interface {
	ResumeWithParams(context.Context, string, *adk.ResumeParams, ...adk.AgentRunOption) (*adk.AsyncIterator[*adk.AgentEvent], error)
}

var _ Resumer = (*adk.Runner)(nil)
