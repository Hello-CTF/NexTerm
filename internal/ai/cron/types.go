package cron

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound         = errors.New("cron job not found")
	ErrConflict         = errors.New("cron job revision conflict")
	ErrJobLimit         = errors.New("cron job session limit reached")
	ErrJobRunning       = errors.New("cron job is running")
	ErrAlreadyRunning   = errors.New("cron scheduler is already running")
	ErrInvalidSchedule  = errors.New("invalid cron schedule")
	ErrStorage          = errors.New("cron storage failure")
	ErrExecutionTimeout = errors.New("cron execution timeout")
	ErrLeaseLost        = errors.New("cron job lease lost")
)

type StorageError struct {
	Op    string
	JobID string
	Err   error
}

func (e *StorageError) Error() string {
	if e.JobID == "" {
		return fmt.Sprintf("%s: %s: %v", ErrStorage, e.Op, e.Err)
	}
	return fmt.Sprintf("%s: %s job %s: %v", ErrStorage, e.Op, e.JobID, e.Err)
}

func (e *StorageError) Unwrap() error { return e.Err }

func (e *StorageError) Is(target error) bool { return target == ErrStorage }

func storageError(op, jobID string, err error) error {
	if err == nil {
		return nil
	}
	return &StorageError{Op: op, JobID: jobID, Err: err}
}

type Store interface {
	List(ctx context.Context) ([]Job, error)
	Create(ctx context.Context, job Job, maxPerSession int) (Job, error)
	CompareAndSwap(ctx context.Context, job Job, expectedRevision uint64) (Job, error)
	Delete(ctx context.Context, sessionID, jobID string, expectedRevision uint64) error
}

type Registration struct {
	ID        string        `json:"id,omitempty"`
	SessionID string        `json:"sessionId"`
	Name      string        `json:"name,omitempty"`
	Prompt    string        `json:"prompt"`
	Schedule  string        `json:"schedule"`
	Timezone  string        `json:"timezone,omitempty"`
	Disabled  bool          `json:"disabled,omitempty"`
	Timeout   time.Duration `json:"timeout,omitempty"`
}

type Lease struct {
	Owner     string    `json:"owner,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
}

type RunState struct {
	ID           string    `json:"id"`
	ScheduledFor time.Time `json:"scheduledFor"`
	StartedAt    time.Time `json:"startedAt"`
	Deadline     time.Time `json:"deadline"`
	Coalesced    bool      `json:"coalesced,omitempty"`
}

type Job struct {
	ID                  string        `json:"id"`
	SessionID           string        `json:"sessionId"`
	Name                string        `json:"name,omitempty"`
	Prompt              string        `json:"prompt"`
	Schedule            string        `json:"schedule"`
	Timezone            string        `json:"timezone"`
	Enabled             bool          `json:"enabled"`
	Timeout             time.Duration `json:"timeout"`
	CreatedAt           time.Time     `json:"createdAt"`
	UpdatedAt           time.Time     `json:"updatedAt"`
	Revision            uint64        `json:"revision"`
	NextRunAt           time.Time     `json:"nextRunAt"`
	RetryAt             time.Time     `json:"retryAt,omitzero"`
	CircuitOpenUntil    time.Time     `json:"circuitOpenUntil,omitzero"`
	ConsecutiveFailures int           `json:"consecutiveFailures,omitempty"`
	LastRunAt           time.Time     `json:"lastRunAt,omitzero"`
	LastScheduledFor    time.Time     `json:"lastScheduledFor,omitzero"`
	LastCoalesced       bool          `json:"lastCoalesced,omitempty"`
	LastError           string        `json:"lastError,omitempty"`
	Lease               Lease         `json:"lease"`
	Run                 RunState      `json:"run"`
}

func (j Job) Running() bool { return j.Run.ID != "" }

type Trigger struct {
	RunID        string    `json:"runId"`
	JobID        string    `json:"jobId"`
	SessionID    string    `json:"sessionId"`
	Name         string    `json:"name,omitempty"`
	Prompt       string    `json:"prompt"`
	ScheduledFor time.Time `json:"scheduledFor"`
	Coalesced    bool      `json:"coalesced"`
}

type Executor interface {
	Execute(ctx context.Context, trigger Trigger) error
}

type ExecutorFunc func(context.Context, Trigger) error

func (f ExecutorFunc) Execute(ctx context.Context, trigger Trigger) error {
	return f(ctx, trigger)
}

const (
	DefaultMaxJobsPerSession = 32
	DefaultExecutionTimeout  = time.Minute
	DefaultLeaseDuration     = 20 * time.Second
	DefaultPollInterval      = time.Second
	DefaultFailureBackoff    = time.Second
	DefaultMaxFailureBackoff = time.Minute
	DefaultCircuitThreshold  = 5
	DefaultCircuitCooldown   = 5 * time.Minute
)

type Options struct {
	MaxJobsPerSession int
	ExecutionTimeout  time.Duration
	LeaseDuration     time.Duration
	PollInterval      time.Duration
	FailureBackoff    time.Duration
	MaxFailureBackoff time.Duration
	CircuitThreshold  int
	CircuitCooldown   time.Duration
	OwnerID           string
	NewID             func() string
	Now               func() time.Time
	OnError           func(error)
}
