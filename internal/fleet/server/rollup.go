package fleetserver

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// DefaultRollupInterval is the default scheduling interval of metrics rollup; the hour-bucket boundary is
// guaranteed by the cutoff alignment in RollupMetrics, and the interval only affects rollup latency, not correctness.
const DefaultRollupInterval = time.Hour

const defaultRollupAttemptTimeout = 5 * time.Minute

// RollupStatus is the result of the most recent rollup attempt, shaped to match the retention status of store/app.
type RollupStatus struct {
	LastAttemptAt time.Time
	LastSuccessAt time.Time
	LastError     string
}

// RollupRunner periodically runs raw->hourly metrics rollup, its lifecycle mirroring internal/app's
// RetentionRunner: a single goroutine writes to the database sequentially (sharing the single-writer semantics of the same *sql.DB as retention),
// scheduled at intervals, each attempt with a timeout, and Start/Shutdown are idempotent and waitable.
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
