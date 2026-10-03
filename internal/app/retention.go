package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

const RetentionSettingKey = "retention.policy"

type RetentionPolicySource interface {
	RetentionPolicy(context.Context) (store.RetentionPolicy, error)
}

type RetentionPolicySourceFunc func(context.Context) (store.RetentionPolicy, error)

func (f RetentionPolicySourceFunc) RetentionPolicy(ctx context.Context) (store.RetentionPolicy, error) {
	return f(ctx)
}

type RetentionStore interface {
	EnforceRetention(context.Context, store.RetentionPolicy) (store.RetentionResult, error)
	RetentionStatus() store.RetentionStatus
}

type RetentionConfig struct {
	Store          RetentionStore
	Policy         RetentionPolicySource
	Interval       time.Duration
	AttemptTimeout time.Duration
	Logger         *slog.Logger
}

type RetentionHealth struct {
	Enabled       bool       `json:"enabled"`
	LastAttemptAt *time.Time `json:"lastAttemptAt"`
	LastSuccessAt *time.Time `json:"lastSuccessAt"`
	LastError     string     `json:"lastError,omitempty"`
}

type RetentionStatusFunc func(context.Context) (RetentionHealth, error)

type RetentionRunner struct {
	store          RetentionStore
	policy         RetentionPolicySource
	interval       time.Duration
	attemptTimeout time.Duration
	logger         *slog.Logger

	mu      sync.RWMutex
	status  RetentionHealth
	cancel  context.CancelFunc
	done    chan struct{}
	started bool
	after   func(time.Duration) <-chan time.Time
}

type retentionSettings struct {
	AuditMaxAgeMS     int64 `json:"auditMaxAgeMs"`
	AuditMaxCount     int64 `json:"auditMaxCount"`
	RecordingMaxAgeMS int64 `json:"recordingMaxAgeMs"`
	RecordingMaxCount int64 `json:"recordingMaxCount"`
}

func StoreRetentionPolicy(database *store.Store) RetentionPolicySource {
	return RetentionPolicySourceFunc(func(ctx context.Context) (store.RetentionPolicy, error) {
		raw, found, err := database.SettingGet(ctx, RetentionSettingKey)
		if err != nil {
			return store.RetentionPolicy{}, err
		}
		if !found || strings.TrimSpace(raw) == "" {
			return store.RetentionPolicy{}, nil
		}
		var settings retentionSettings
		if err := json.Unmarshal([]byte(raw), &settings); err != nil {
			return store.RetentionPolicy{}, fmt.Errorf("decode %s: %w", RetentionSettingKey, err)
		}
		auditAge, err := retentionDuration("auditMaxAgeMs", settings.AuditMaxAgeMS)
		if err != nil {
			return store.RetentionPolicy{}, err
		}
		recordingAge, err := retentionDuration("recordingMaxAgeMs", settings.RecordingMaxAgeMS)
		if err != nil {
			return store.RetentionPolicy{}, err
		}
		if settings.AuditMaxCount < 0 || settings.RecordingMaxCount < 0 {
			return store.RetentionPolicy{}, fmt.Errorf("%s counts must not be negative", RetentionSettingKey)
		}
		return store.RetentionPolicy{
			AuditMaxAge:       auditAge,
			AuditMaxCount:     settings.AuditMaxCount,
			RecordingMaxAge:   recordingAge,
			RecordingMaxCount: settings.RecordingMaxCount,
		}, nil
	})
}

func retentionDuration(name string, milliseconds int64) (time.Duration, error) {
	if milliseconds < 0 {
		return 0, fmt.Errorf("%s must not be negative", name)
	}
	if milliseconds > math.MaxInt64/int64(time.Millisecond) {
		return 0, fmt.Errorf("%s is too large", name)
	}
	return time.Duration(milliseconds) * time.Millisecond, nil
}

func NewRetentionRunner(config RetentionConfig) (*RetentionRunner, error) {
	if config.Store == nil {
		return nil, fmt.Errorf("retention store is required")
	}
	if config.Policy == nil {
		return nil, fmt.Errorf("retention policy source is required")
	}
	if config.Interval <= 0 {
		config.Interval = 6 * time.Hour
	}
	if config.AttemptTimeout <= 0 {
		config.AttemptTimeout = 5 * time.Minute
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &RetentionRunner{
		store: config.Store, policy: config.Policy, interval: config.Interval,
		attemptTimeout: config.AttemptTimeout, logger: config.Logger, after: time.After,
	}, nil
}

func (r *RetentionRunner) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return fmt.Errorf("retention runner has already started")
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

func (r *RetentionRunner) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	for {
		r.enforce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-r.after(r.interval):
		}
	}
}

func (r *RetentionRunner) enforce(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
	attemptCtx, cancel := context.WithTimeout(ctx, r.attemptTimeout)
	defer cancel()
	attemptedAt := time.Now().UTC()
	policy, err := r.policy.RetentionPolicy(attemptCtx)
	enabled := retentionEnabled(policy)
	if err == nil && enabled {
		_, err = r.store.EnforceRetention(attemptCtx, policy)
	}
	r.mu.Lock()
	r.status.Enabled = enabled
	r.status.LastAttemptAt = retentionTime(attemptedAt)
	r.status.LastError = ""
	if err == nil {
		r.status.LastSuccessAt = retentionTime(time.Now().UTC())
	} else {
		r.status.LastError = err.Error()
	}
	r.mu.Unlock()
	if err != nil && !errors.Is(err, context.Canceled) {
		r.logger.Warn("scheduled retention cleanup failed", "error", err)
	}
}

func retentionEnabled(policy store.RetentionPolicy) bool {
	return policy.AuditMaxAge > 0 || policy.AuditMaxCount > 0 || policy.RecordingMaxAge > 0 || policy.RecordingMaxCount > 0
}

func (r *RetentionRunner) Status(_ context.Context) (RetentionHealth, error) {
	r.mu.RLock()
	status := r.status
	r.mu.RUnlock()
	if status.LastAttemptAt == nil {
		stored := r.store.RetentionStatus()
		status.LastAttemptAt = retentionTime(stored.LastAttemptAt)
		status.LastSuccessAt = retentionTime(stored.LastSuccessAt)
		status.LastError = stored.LastError
	}
	return status, nil
}

func retentionTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func (r *RetentionRunner) Shutdown(ctx context.Context) error {
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
