package fleetserver

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// DefaultRollupInterval 是指标聚合的默认调度间隔; 小时桶边界由
// RollupMetrics 的 cutoff 对齐保证, 间隔只影响聚合延迟不影响正确性。
const DefaultRollupInterval = time.Hour

const defaultRollupAttemptTimeout = 5 * time.Minute

// RollupStatus 是最近一次聚合尝试的结果, 形状对齐 store/app 的 retention 状态。
type RollupStatus struct {
	LastAttemptAt time.Time
	LastSuccessAt time.Time
	LastError     string
}

// RollupRunner 定时执行 raw->hourly 指标聚合, 生命周期镜像 internal/app 的
// RetentionRunner: 单 goroutine 顺序写库 (与 retention 共用同一 *sql.DB 的
// 单写者语义), 间隔调度, 单次尝试带超时, Start/Shutdown 幂等可等。
type RollupRunner struct {
	service        *Service
	interval       time.Duration
	attemptTimeout time.Duration
	logger         *slog.Logger
	after          func(time.Duration) <-chan time.Time

	mu      sync.RWMutex
	status  RollupStatus
	cancel  context.CancelFunc
	done    chan struct{}
	started bool
}

func NewRollupRunner(service *Service, interval time.Duration, logger *slog.Logger) *RollupRunner {
	if interval <= 0 {
		interval = DefaultRollupInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &RollupRunner{
		service:        service,
		interval:       interval,
		attemptTimeout: defaultRollupAttemptTimeout,
		logger:         logger,
		after:          time.After,
	}
}

func (r *RollupRunner) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return errors.New("rollup runner has already started")
	}
	r.started = true
	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.done = make(chan struct{})
	done := r.done
	r.mu.Unlock()
	go r.run(runCtx, done)
	return nil
}

func (r *RollupRunner) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	for {
		r.rollup(ctx)
		select {
		case <-ctx.Done():
			return
		case <-r.after(r.interval):
		}
	}
}

func (r *RollupRunner) rollup(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
	attemptCtx, cancel := context.WithTimeout(ctx, r.attemptTimeout)
	defer cancel()
	attemptedAt := time.Now().UTC()
	err := r.service.RollupMetrics(attemptCtx)
	r.mu.Lock()
	r.status.LastAttemptAt = attemptedAt
	if err == nil {
		r.status.LastSuccessAt = time.Now().UTC()
		r.status.LastError = ""
	} else {
		r.status.LastError = err.Error()
	}
	r.mu.Unlock()
	if err != nil && !errors.Is(err, context.Canceled) {
		r.logger.Warn("scheduled device metrics rollup failed", "error", err)
	}
}

func (r *RollupRunner) Status() RollupStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status
}

func (r *RollupRunner) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	cancel := r.cancel
	done := r.done
	r.cancel = nil
	r.done = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
