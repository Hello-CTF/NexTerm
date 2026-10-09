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

type Config struct {
	Checkpoints       adk.CheckPointStore
	Store             Store
	TTL               time.Duration
	TerminalRetention time.Duration
	Now               func() time.Time
	NewNonce          func() (string, error)
}

type Resumer interface {
	ResumeWithParams(context.Context, string, *adk.ResumeParams, ...adk.AgentRunOption) (*adk.AsyncIterator[*adk.AgentEvent], error)
}

var _ Resumer = (*adk.Runner)(nil)
