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

// unattendedPermissions is the agent runner's Permission hook: cron-triggered
// executions carry guard.WithUnattended(ctx) and receive the Unattended
// decision mode, so NeedsConfirm and Forbidden rulings are denied explicitly
// instead of being executed silently or parked on a confirmation no one will
// answer. Interactive executions keep the user's configured guard mode, and
// remembered approvals never authorize unattended execution.
func unattendedPermissions(manager *guard.Manager) func(context.Context) (guard.Config, error) {
	return func(ctx context.Context) (guard.Config, error) {
		if guard.UnattendedFrom(ctx) {
			return guard.Config{Mode: guard.Unattended}, nil
		}
		return manager.Snapshot(ctx)
	}
}

// conversationLookup is the slice of the application store the cron surface
// needs: a trigger's session ID is executed as the agent conversation ID, so
// registration must target an existing conversation instead of creating a
// job that fails on every occurrence.
type conversationLookup interface {
	ConvGet(ctx context.Context, id string) (store.ConversationRow, error)
}

// scopeForConversation resolves the execution scope a cron trigger runs
// with: the scope the owning conversation was created under (both the agent
// runner and ai_conversation_create persist {"scope": ...}). A conversation
// without a stored scope yields the empty scope, which keeps session-bound
// tools disabled rather than guessing a target.
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

// cronRuntime owns the composed cron scheduler: the durable SQLite store
// over the application handle, the unattended agent executor driving the one
// shared agent runner, and the scheduler whose lifecycle follows the
// application. Store and runner are borrowed from the composition root,
// never duplicated.
type cronRuntime struct {
	scheduler     *cron.Scheduler
	conversations conversationLookup

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// composeCronRuntime builds the production cron runtime with the default
// scheduler options.
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

// cronModule exposes the cron RPC surface and ties the scheduler's bounded
// lifecycle to the application. It is registered after the agent module so
// shutdown stops the scheduler — canceling in-flight unattended runs through
// the executor — before the agent runner closes.
func cronModule(runtime *cronRuntime) Module {
	return Module{
		Name:             "cron",
		RegisterCommands: runtime.registerCommands,
		Component:        runtime,
	}
}

// Start runs the scheduler until the application context ends or a storage
// operation fails; a terminal scheduler failure is logged, never swallowed.
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

// Shutdown stops the scheduler and waits — bounded by ctx — for in-flight
// executions to finish: the executor cancels the agent run of every claimed
// trigger, and the scheduler records the interruption durably before Run
// returns. Shutdown never deletes job rows; other sessions' jobs stay intact.
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

// registerCommands maps the cron RPC surface directly onto the scheduler:
// unregister is the only deletion path, every operation is scoped by session
// and job identity, and storage errors surface to the caller unwrapped.
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
