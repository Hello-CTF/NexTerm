package cron

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound         = errors.New("定时任务不存在，可能已被删除，请刷新后重试")
	ErrConflict         = errors.New("定时任务已被其他操作修改，请刷新后重试")
	ErrJobLimit         = errors.New("当前会话的定时任务数量已达上限，请删除不再需要的任务后再创建")
	ErrJobRunning       = errors.New("任务运行中，请等待结束后再操作")
	ErrAlreadyRunning   = errors.New("定时任务调度器已在运行")
	ErrInvalidSchedule  = errors.New("定时表达式无效，请检查格式（分 时 日 月 周，如 0 9 * * *）")
	ErrStorage          = errors.New("定时任务存储失败，请重试")
	ErrExecutionTimeout = errors.New("定时任务执行超时")
	ErrLeaseLost        = errors.New("定时任务租约丢失，本次运行已中断")
	ErrOwnerless        = errors.New("旧版本定时任务缺少所有者信息，已停用，请重新创建（或先升级到 1.2.x 最终版）")
	ErrInvalidOwner     = errors.New("定时任务所有者信息无效")
	ErrForeignScope     = errors.New("定时任务会话不属于任务所有者")
)

// ErrOpenJobUnattributed marks Open-mode jobs disabled after switching to
// account mode because their conversation no longer exists.
var ErrOpenJobUnattributed = errors.New("开放模式创建的定时任务找不到所属会话，切换到账号模式后已停用，请重新创建")

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
	// ListDueIdle returns at most limit enabled, non-running jobs whose next
	// occurrence is due at now, ordered by (next_run_at, id) so batches are
	// stable. A limit <= 0 yields no rows.
	ListDueIdle(ctx context.Context, now time.Time, limit int) ([]Job, error)
	// ListRecoveryCandidates returns at most limit running jobs whose lease and
	// run deadline have both expired at now, ordered by (run_deadline, id) so
	// batches are stable. Excluded IDs let scans pass locally active cleanups.
	// A limit <= 0 yields no rows.
	ListRecoveryCandidates(ctx context.Context, now time.Time, limit int, excludedIDs ...string) ([]Job, error)
	Create(ctx context.Context, job Job, maxPerSession int) (Job, error)
	CompareAndSwap(ctx context.Context, job Job, expectedRevision uint64) (Job, error)
	Delete(ctx context.Context, sessionID, jobID string, expectedRevision uint64) error
}

type Registration struct {
	ID             string        `json:"id,omitempty"`
	SessionID      string        `json:"sessionId"`
	Name           string        `json:"name,omitempty"`
	Prompt         string        `json:"prompt"`
	Schedule       string        `json:"schedule"`
	Timezone       string        `json:"timezone,omitempty"`
	Disabled       bool          `json:"disabled,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty"`
	ModelProfileID string        `json:"modelProfileId,omitempty"`
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
	ModelProfileID      string        `json:"modelProfileId,omitempty"`
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
	OwnerID             string        `json:"ownerId"`
	OwnerKind           OwnerKind     `json:"ownerKind"`
}

func (j Job) Running() bool { return j.Run.ID != "" }

type Trigger struct {
	RunID          string    `json:"runId"`
	JobID          string    `json:"jobId"`
	SessionID      string    `json:"sessionId"`
	Name           string    `json:"name,omitempty"`
	Prompt         string    `json:"prompt"`
	ScheduledFor   time.Time `json:"scheduledFor"`
	Coalesced      bool      `json:"coalesced"`
	ModelProfileID string    `json:"modelProfileId,omitempty"`
	OwnerID        string    `json:"ownerId"`
	OwnerKind      OwnerKind `json:"ownerKind"`
}

// Execute must return after ctx cancellation; the scheduler renews the durable lease while cancellation cleanup is in progress.
type Executor interface {
	Execute(ctx context.Context, trigger Trigger) error
}

const (
	DefaultMaxJobsPerSession       = 32
	DefaultMaxConcurrentExecutions = 4
	DefaultExecutionTimeout        = time.Minute
	DefaultLeaseDuration           = 20 * time.Second
	DefaultPollInterval            = time.Second
	DefaultFailureBackoff          = time.Second
	DefaultMaxFailureBackoff       = time.Minute
	DefaultCircuitThreshold        = 5
	DefaultCircuitCooldown         = 5 * time.Minute
	DefaultDueScanBatchSize        = 64
	DefaultRecoveryBatchSize       = 64
)

type Options struct {
	MaxJobsPerSession       int
	MaxConcurrentExecutions int
	ExecutionTimeout        time.Duration
	LeaseDuration           time.Duration
	PollInterval            time.Duration
	FailureBackoff          time.Duration
	MaxFailureBackoff       time.Duration
	CircuitThreshold        int
	CircuitCooldown         time.Duration
	DueScanBatchSize        int
	RecoveryBatchSize       int
	OwnerID                 string
	Now                     func() time.Time
	OnError                 func(error)
}
