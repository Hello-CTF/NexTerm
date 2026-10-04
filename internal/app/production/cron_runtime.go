package production

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/cron"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

func unattendedPermissions(manager *guard.Manager) func(context.Context) (guard.Config, error) {
	return func(ctx context.Context) (guard.Config, error) {
		config, err := manager.Snapshot(ctx)
		if err != nil {
			return guard.Config{}, err
		}
		if guard.UnattendedFrom(ctx) {
			config.Mode = guard.Unattended
		}
		return config, nil
	}
}

type conversationLookup interface {
	ConvGet(ctx context.Context, id string) (store.ConversationRow, error)
}

func scopeForConversation(lookup conversationLookup) func(cron.Trigger) tools.Scope {
	return func(trigger cron.Trigger) tools.Scope {
		row, err := lookup.ConvGet(context.Background(), trigger.SessionID)
		if err != nil {
			return tools.Scope{}
		}
		var envelope struct {
			Scope tools.Scope `json:"scope"`
		}
		if err := json.Unmarshal([]byte(row.ScopeJSON), &envelope); err != nil {
			return tools.Scope{}
		}
		return envelope.Scope
	}
}

type cronRuntime struct {
	scheduler     *cron.Scheduler
	conversations conversationLookup

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func composeCronRuntime(ctx context.Context, services *ProductionServices) (*cronRuntime, error) {
	return newCronRuntime(ctx, services, cron.Options{})
}

func newCronRuntime(ctx context.Context, services *ProductionServices, options cron.Options) (*cronRuntime, error) {
	if services.Store == nil || services.Agent == nil {
		return nil, errors.New("cron runtime requires store and agent services")
	}
	cronStore, err := cron.NewSQLiteStore(ctx, services.Store.DB())
	if err != nil {
		return nil, err
	}
	executor := &cron.AgentExecutor{Runner: services.Agent, ScopeFor: scopeForConversation(services.Store)}
	if options.OnError == nil {
		options.OnError = func(err error) {
			slog.Warn("cron scheduler asynchronous failure", "error", err)
		}
	}
	scheduler, err := cron.NewScheduler(cronStore, executor, options)
	if err != nil {
		return nil, err
	}
	return &cronRuntime{scheduler: scheduler, conversations: services.Store}, nil
}

func cronModule(runtime *cronRuntime) Module {
	return Module{
		Name:             "cron",
		RegisterCommands: runtime.registerCommands,
		Component:        runtime,
	}
}

func (c *cronRuntime) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		return errors.New("cron runtime is already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	c.cancel = cancel
	c.done = done
	go func() {
		defer close(done)
		if err := c.scheduler.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("cron scheduler stopped", "error", err)
		}
	}()
	return nil
}

func (c *cronRuntime) Shutdown(ctx context.Context) error {
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

type cronRegisterRequest struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name,omitempty"`
	Prompt    string `json:"prompt"`
	Schedule  string `json:"schedule"`
	Timezone  string `json:"timezone,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
	TimeoutMS int64  `json:"timeoutMs,omitempty"`
}

type cronJobRequest struct {
	SessionID string `json:"sessionId"`
	JobID     string `json:"jobId"`
}

type cronSetEnabledRequest struct {
	SessionID string `json:"sessionId"`
	JobID     string `json:"jobId"`
	Enabled   bool   `json:"enabled"`
}

func (c *cronRuntime) registerCommands(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "cron_register", func(ctx context.Context, _ *ipc.Call, input cronRegisterRequest) (cron.Job, error) {
				if c.conversations != nil {
					if _, err := c.conversations.ConvGet(ctx, input.SessionID); err != nil {
						return cron.Job{}, err
					}
				}
				return c.scheduler.Register(ctx, cron.Registration{
					SessionID: input.SessionID,
					Name:      input.Name,
					Prompt:    input.Prompt,
					Schedule:  input.Schedule,
					Timezone:  input.Timezone,
					Disabled:  input.Disabled,
					Timeout:   time.Duration(input.TimeoutMS) * time.Millisecond,
				})
			})
		},
		func() error {
			return ipc.Register(dispatcher, "cron_list", func(ctx context.Context, _ *ipc.Call, input cronJobRequest) ([]cron.Job, error) {
				return c.scheduler.List(ctx, input.SessionID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "cron_get", func(ctx context.Context, _ *ipc.Call, input cronJobRequest) (cron.Job, error) {
				return c.scheduler.Get(ctx, input.SessionID, input.JobID)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "cron_set_enabled", func(ctx context.Context, _ *ipc.Call, input cronSetEnabledRequest) (cron.Job, error) {
				return c.scheduler.SetEnabled(ctx, input.SessionID, input.JobID, input.Enabled)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "cron_unregister", func(ctx context.Context, _ *ipc.Call, input cronJobRequest) (any, error) {
				return nil, c.scheduler.Unregister(ctx, input.SessionID, input.JobID)
			})
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}
