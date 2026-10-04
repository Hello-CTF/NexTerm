package production

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

const aiRetentionSettingKey = "ai.retention.policy"

const (
	defaultAIRetentionMaxAge   = 30 * 24 * time.Hour
	defaultAIRetentionMaxCount = int64(5000)
	defaultAIRetentionInterval = 6 * time.Hour
)

type aiRetentionSettings struct {
	MaxAgeMS int64 `json:"maxAgeMs"`
	MaxCount int64 `json:"maxCount"`
}

type aiRetentionComponent struct {
	store    *store.Store
	interval time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newAIRetentionComponent(database *store.Store, interval time.Duration) *aiRetentionComponent {
	if interval <= 0 {
		interval = defaultAIRetentionInterval
	}
	return &aiRetentionComponent{store: database, interval: interval}
}

func (c *aiRetentionComponent) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	c.cancel = cancel
	c.done = done
	go func() {
		defer close(done)
		c.enforce(runCtx)
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				c.enforce(runCtx)
			}
		}
	}()
	return nil
}

func (c *aiRetentionComponent) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.cancel, c.done = nil, nil
	c.mu.Unlock()
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

func (c *aiRetentionComponent) enforce(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
	policy, err := c.policy(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("AI run retention policy unavailable", "error", err)
		}
		return
	}
	if policy.AIRunMaxAge == 0 && policy.AIRunMaxCount == 0 {
		return
	}
	attemptCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := c.store.EnforceRetention(attemptCtx, policy); err != nil && ctx.Err() == nil {
		slog.Warn("AI run retention cleanup failed", "error", err)
	}
}

func (c *aiRetentionComponent) policy(ctx context.Context) (store.RetentionPolicy, error) {
	raw, found, err := c.store.SettingGet(ctx, aiRetentionSettingKey)
	if err != nil {
		return store.RetentionPolicy{}, err
	}
	if !found || strings.TrimSpace(raw) == "" {
		return store.RetentionPolicy{AIRunMaxAge: defaultAIRetentionMaxAge, AIRunMaxCount: defaultAIRetentionMaxCount}, nil
	}
	var settings aiRetentionSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return store.RetentionPolicy{}, err
	}
	if settings.MaxAgeMS < 0 || settings.MaxCount < 0 {
		return store.RetentionPolicy{}, nil
	}
	return store.RetentionPolicy{
		AIRunMaxAge:   time.Duration(settings.MaxAgeMS) * time.Millisecond,
		AIRunMaxCount: settings.MaxCount,
	}, nil
}
