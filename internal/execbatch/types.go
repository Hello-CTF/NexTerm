package execbatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	DefaultConcurrency = 4
	MaxConcurrency     = 32
	MaxTargets         = 64
	MaxCommandBytes    = 64 << 10
	DefaultTimeout     = 30 * time.Second
	MaxTimeout         = 10 * time.Minute
	DefaultOutputLimit = 64 << 10
	MaxOutputLimit     = 1 << 20
)

var (
	ErrInvalid            = errors.New("invalid exec batch")
	ErrBatchNotFound      = errors.New("exec batch not found")
	ErrBatchConflict      = errors.New("exec batch already active")
	ErrPendingCancelLimit = errors.New("too many pending exec batch cancellations")
	ErrForbidden          = errors.New("exec batch forbidden")
	ErrUnsupported        = errors.New("exec batch target must be a connected SSH session")
	ErrDisconnected       = errors.New("exec batch target disconnected")
)

type Status string

const (
	StatusSuccess        Status = "success"
	StatusExitError      Status = "exit_error"
	StatusTransportError Status = "transport_error"
	StatusTimeout        Status = "timeout"
	StatusCanceled       Status = "canceled"
)

type RunbookMetadata struct {
	RunbookID   string `json:"runbookId"`
	RunID       string `json:"runId"`
	StepNumber  int32  `json:"stepNumber"`
	StepCount   int32  `json:"stepCount"`
	GroupNumber int32  `json:"groupNumber"`
	GroupCount  int32  `json:"groupCount"`
	// RunbookVersion is the persisted template revision the run started from; 0 for ad-hoc runbooks.
	RunbookVersion int32 `json:"-"`
}

type Request struct {
	BatchID                string                  `json:"batchId"`
	SessionIDs             []string                `json:"sessionIds"`
	Command                string                  `json:"command"`
	TimeoutMS              int64                   `json:"timeoutMs,omitempty"`
	MaxConcurrent          int                     `json:"maxConcurrent,omitempty"`
	ProductionConfirmation *ProductionConfirmation `json:"productionConfirmation,omitempty"`
	Runbook                *RunbookMetadata        `json:"runbook,omitempty"`
}

type Target struct {
	SessionID   string
	SessionName string
	AssetID     string
	Hostname    string
	Environment string
	Transport   base.Transport
	Generation  uint64
}

type Resolver interface {
	Resolve(context.Context, string) (Target, error)
}

type ResolverFunc func(context.Context, string) (Target, error)

func (f ResolverFunc) Resolve(ctx context.Context, sessionID string) (Target, error) {
	return f(ctx, sessionID)
}

type Result struct {
	SessionID   string `json:"sessionId"`
	SessionName string `json:"sessionName"`
	AssetID     string `json:"assetId,omitempty"`
	Status      Status `json:"status"`
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	ExitCode    *int   `json:"exitCode"`
	Signal      string `json:"signal,omitempty"`
	Error       string `json:"error,omitempty"`
	DurationMS  int64  `json:"durationMs"`
	Truncated   bool   `json:"truncated"`
	startedAt   time.Time
	finishedAt  time.Time
	environment string
}

type Batch struct {
	BatchID    string   `json:"batchId"`
	Command    string   `json:"command"`
	StartedAt  int64    `json:"startedAt"`
	FinishedAt int64    `json:"finishedAt"`
	Results    []Result `json:"results"`
}

type CancelResult struct {
	BatchID  string `json:"batchId"`
	Canceled bool   `json:"canceled"`
}

type TargetFailure struct {
	SessionID string
	Err       error
}

type PreflightError struct {
	Failures []TargetFailure
}

func (e *PreflightError) Error() string {
	parts := make([]string, 0, len(e.Failures))
	for _, failure := range e.Failures {
		parts = append(parts, fmt.Sprintf("%s: %v", failure.SessionID, failure.Err))
	}
	return "exec batch preflight failed: " + strings.Join(parts, "; ")
}

func (e *PreflightError) Unwrap() []error {
	errs := make([]error, 0, len(e.Failures))
	for _, failure := range e.Failures {
		errs = append(errs, failure.Err)
	}
	return errs
}
