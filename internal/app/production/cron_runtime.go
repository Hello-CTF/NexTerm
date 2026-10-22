package production

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
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

func parseConversationScope(scopeJSON string) (tools.Scope, error) {
	var envelope struct {
		Scope tools.Scope `json:"scope"`
	}
	if err := json.Unmarshal([]byte(scopeJSON), &envelope); err != nil {
		return tools.Scope{}, err
	}
	return envelope.Scope, nil
}

func scopeFromConversationRow(row store.ConversationRow) (tools.Scope, error) {
	return parseConversationScope(row.ScopeJSON)
}

func scopeFromConversationValue(value any) (tools.Scope, error) {
	encoded, err := json.Marshal(map[string]any{"scope": value})
	if err != nil {
		return tools.Scope{}, err
	}
	return parseConversationScope(string(encoded))
}

func validatedConversationScopeRow(ctx context.Context, row store.ConversationRow, validator agent.ScopeValidator) (tools.Scope, error) {
	scope, err := scopeFromConversationRow(row)
	if err != nil {
		return tools.Scope{}, err
	}
	if validator != nil {
		if err := validator.ValidateScope(ctx, scope); err != nil {
			return tools.Scope{}, err
		}
	}
	return scope, nil
}

func validatedConversationScope(ctx context.Context, lookup conversationLookup, validator agent.ScopeValidator, conversationID string) (tools.Scope, error) {
	if lookup == nil {
		return tools.Scope{}, nil
	}
	row, err := lookup.ConvGet(ctx, conversationID)
	if err != nil {
		return tools.Scope{}, err
	}
	return validatedConversationScopeRow(ctx, row, validator)
}

func scopeForConversation(lookup conversationLookup, validators ...agent.ScopeValidator) func(context.Context, cron.Trigger) (tools.Scope, error) {
	return func(ctx context.Context, trigger cron.Trigger) (tools.Scope, error) {
		if err := ctx.Err(); err != nil {
			return tools.Scope{}, err
		}
		row, err := lookup.ConvGet(ctx, trigger.SessionID)
		if err != nil {
			return tools.Scope{}, err
		}
		if (trigger.OwnerKind == cron.OwnerKindAccount && row.OwnerID != trigger.OwnerID) ||
			(trigger.OwnerKind != cron.OwnerKindAccount && row.OwnerID != "") {
			return tools.Scope{}, cron.ErrForeignScope
		}
		var validator agent.ScopeValidator
		if len(validators) != 0 {
			validator = validators[0]
		}
		if resolver, ok := validator.(agent.ScopeResolver); ok {
			scope, err := scopeFromConversationRow(row)
			if err != nil {
				return tools.Scope{}, err
			}
			resolved, err := resolver.ResolveScope(ctx, row.ID, scope)
			if err != nil {
				// A disconnected scope asset is handed to the executor's opt-in
				// connect flow; every other resolution failure stays fatal.
				var appErr *ipc.Error
				if errors.As(err, &appErr) && appErr.Code == ipc.CodeDisconnected && strings.TrimSpace(scope.AssetID) != "" {
					return tools.Scope{}, &cron.AssetDisconnectedError{AssetID: scope.AssetID, Err: err}
				}
				return tools.Scope{}, err
			}
			// Asset-only scopes resolve without a session; surface the missing
			// live session here so the opt-in connect flow can handle it instead
			// of failing mid-run.
			if strings.TrimSpace(resolved.AssetID) != "" {
				if scopeResolver, ok := validator.(*productionScopeResolver); ok {
					if _, sessionErr := scopeResolver.liveSessionForAsset(ctx, resolved.AssetID); sessionErr != nil {
						return tools.Scope{}, &cron.AssetDisconnectedError{AssetID: resolved.AssetID, Err: sessionErr}
					}
				}
			}
			return resolved, nil
		}
		return validatedConversationScopeRow(ctx, row, validator)
	}
}

// newCronConnector wires the R-07 connect gate into the cron executor: a
// disconnected scope asset is dialed only with the per-owner explicit opt-in
// and every guard.AuthorizeConnect gate satisfied. Unknown or changed host keys
// are never auto-accepted; rejection never dials.
func newCronConnector(services *ProductionServices) *cron.Connector {
	return &cron.Connector{
		OptIn: func(ctx context.Context, _ cron.Trigger) (bool, error) {
			return cronConnectSetting(ctx, services.Store, cron.ConnectOptInSettingKeyForUser(ctx))
		},
		Evidence: cronConnectEvidence{services: services},
		Dial:     cronConnectDial(services),
		Settings: services.Store,
	}
}

func cronConnectSetting(ctx context.Context, settings guard.Settings, key string) (bool, error) {
	if settings == nil {
		return false, nil
	}
	raw, found, err := settings.SettingGet(ctx, key)
	if err != nil {
		return false, err
	}
	if !found || raw == "" {
		return false, nil
	}
	var value bool
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return false, fmt.Errorf("cron connect setting %s: %w", key, err)
	}
	return value, nil
}

// cronConnectEvidence resolves the real authorization evidence for a cron
// connect attempt against the store, host-key database, and vault.
type cronConnectEvidence struct {
	services *ProductionServices
}

func (e cronConnectEvidence) asset(ctx context.Context, assetID string) (store.AssetRow, error) {
	if e.services.Store == nil {
		return store.AssetRow{}, ipc.NewError(ipc.CodeUnsupported, "资产存储未配置")
	}
	return e.services.Store.AssetGetLive(ctx, assetID)
}

func (e cronConnectEvidence) OwnerMatch(ctx context.Context, _ cron.Trigger, assetID string) (bool, error) {
	// The scope validator enforces owner scoping for account users and shared
	// visibility for desktop/open mode.
	err := productionScopeValidator(e.services).validateScopeAsset(ctx, assetID)
	if err == nil {
		return true, nil
	}
	var appErr *ipc.Error
	if errors.As(err, &appErr) && (appErr.Code == ipc.CodeForbidden || appErr.Code == ipc.CodeNotFound) {
		return false, nil
	}
	return false, err
}

func (e cronConnectEvidence) HostKey(ctx context.Context, _ cron.Trigger, assetID string) (guard.HostKeyState, error) {
	row, err := e.asset(ctx, assetID)
	if err != nil {
		return "", err
	}
	if row.Kind != session.KindSSH {
		// Only SSH carries known-host verification; other kinds have no host key
		// to auto-accept.
		return guard.HostKeyKnown, nil
	}
	if row.Host == nil || strings.TrimSpace(*row.Host) == "" || e.services.hostKeys == nil {
		// Fail closed: an SSH asset whose host key cannot be verified against the
		// known-host database is never connected.
		return guard.HostKeyUnknown, nil
	}
	port := int32(22)
	if row.Port != nil {
		port = *row.Port
	}
	keys, err := e.services.hostKeys.ApprovedHostKeys(ctx, *row.Host, int(port))
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return guard.HostKeyUnknown, nil
	}
	return guard.HostKeyKnown, nil
}

func (e cronConnectEvidence) VaultReady(ctx context.Context, _ cron.Trigger, assetID string) (bool, error) {
	row, err := e.asset(ctx, assetID)
	if err != nil {
		return false, err
	}
	if row.CredID == nil || strings.TrimSpace(*row.CredID) == "" {
		// keyPath/agent/password auth needs no vault credential.
		return true, nil
	}
	if e.services.Vault == nil || !e.services.Vault.Status().Unlocked {
		return false, nil
	}
	if _, err := e.services.Store.CredentialGetRow(ctx, *row.CredID); err != nil {
		var appErr *ipc.Error
		if errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (e cronConnectEvidence) Environment(ctx context.Context, _ cron.Trigger, assetID string) (string, error) {
	row, err := e.asset(ctx, assetID)
	if err != nil {
		return "", err
	}
	return store.AssetEnvironment(row), nil
}

func (e cronConnectEvidence) ProductionConfirmed(ctx context.Context, _ cron.Trigger, _ string) (bool, error) {
	return cronConnectSetting(ctx, e.services.Store, cron.ConnectProductionOptInSettingKeyForUser(ctx))
}

// cronConnectDial dials the asset with strict host-key verification forced on
// every hop: auto-accepting unknown host keys is disabled even when stored
// asset options enable it, so unknown or changed keys are never auto-accepted
// and rejection adds no new trust. The strict mark travels with the asset
// options because the session manager detaches the connect context from the
// caller; interactive connects do not set it and keep their configured
// behavior.
func cronConnectDial(services *ProductionServices) func(context.Context, cron.Trigger, string) (string, error) {
	return func(ctx context.Context, trigger cron.Trigger, assetID string) (string, error) {
		return dialAssetStrict(ctx, services, trigger.OwnerID, assetID)
	}
}

// dialAssetStrict connects the asset with strict host-key verification on
// every hop; the owner ID scopes the session asset conversion.
func dialAssetStrict(ctx context.Context, services *ProductionServices, ownerID, assetID string) (string, error) {
	if services.Store == nil || services.Sessions == nil {
		return "", ipc.NewError(ipc.CodeUnsupported, "终端会话服务未配置")
	}
	row, err := services.Store.AssetGetLive(ctx, assetID)
	if err != nil {
		return "", err
	}
	asset, err := productionSessionAsset(row, false, ownerID)
	if err != nil {
		return "", err
	}
	if options, ok := asset.Options.(map[string]any); ok {
		options["strictHostKey"] = true
	}
	connected, err := services.Sessions.ConnectFor(ctx, asset)
	if err != nil {
		return "", err
	}
	return connected.ID, nil
}

type cronRuntime struct {
	scheduler      *cron.Scheduler
	conversations  conversationLookup
	profiles       profileLookup
	scopeValidator agent.ScopeValidator

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

type profileLookup interface {
	Profile(ctx context.Context, id string) (profiles.Profile, bool, error)
}

func composeCronScheduler(ctx context.Context, services *ProductionServices, options cron.Options) (*cron.Scheduler, *cron.AgentExecutor, error) {
	if services.Store == nil {
		return nil, nil, errors.New("cron runtime requires store and agent services")
	}
	cronStore, err := cron.NewSQLiteStore(ctx, services.Store.GORM(), cron.WithShared(services.desktop || owner.OpenAccess(ctx)))
	if err != nil {
		return nil, nil, err
	}
	allowLegacy := services.desktop || owner.OpenAccess(ctx)
	options.AllowOwnerlessLegacy = allowLegacy
	executor := &cron.AgentExecutor{
		ScopeFor: scopeForConversation(services.Store, productionScopeValidator(services)), AllowOwnerlessLegacy: allowLegacy,
		Connect: newCronConnector(services),
	}
	if options.OnError == nil {
		options.OnError = func(err error) {
			slog.Warn("cron scheduler asynchronous failure", "error", err)
			_ = ipc.Emit(context.Background(), services.Events, ipc.TopicAppError, ipc.AppErrorEvent{Code: "cron", Message: err.Error()})
		}
	}
	scheduler, err := cron.NewScheduler(cronStore, executor, options)
	if err != nil {
		return nil, nil, err
	}
	return scheduler, executor, nil
}

type reminderScheduler struct {
	scheduler      *cron.Scheduler
	conversations  conversationLookup
	scopeValidator agent.ScopeValidator
}

func (s reminderScheduler) ScheduleReminder(ctx context.Context, conversationID string, reminder tools.Reminder) (tools.ScheduledReminder, error) {
	if _, err := validatedConversationScope(ctx, s.conversations, s.scopeValidator, conversationID); err != nil {
		return tools.ScheduledReminder{}, err
	}
	job, err := s.scheduler.Register(ctx, cron.Registration{
		SessionID: conversationID,
		Name:      reminderName(reminder.Message),
		Prompt:    reminder.Message,
		Schedule:  cron.AtExpression(reminder.At),
		Timezone:  "UTC",
	})
	if err != nil {
		return tools.ScheduledReminder{}, err
	}
	return tools.ScheduledReminder{ID: job.ID, At: job.NextRunAt}, nil
}

func reminderName(message string) string {
	runes := []rune(message)
	if len(runes) > 40 {
		message = string(runes[:40]) + "…"
	}
	return "提醒：" + message
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
	SessionID      string `json:"sessionId"`
	Name           string `json:"name,omitempty"`
	Prompt         string `json:"prompt"`
	Schedule       string `json:"schedule"`
	Timezone       string `json:"timezone,omitempty"`
	Disabled       bool   `json:"disabled,omitempty"`
	TimeoutMS      int64  `json:"timeoutMs,omitempty"`
	ModelProfileID string `json:"modelProfileId,omitempty"`
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

func cronIPCError(err error) error {
	if err == nil {
		return nil
	}
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return err
	}
	switch {
	case errors.Is(err, profiles.ErrProfileNotFound), errors.Is(err, profiles.ErrNoActiveProfile), errors.Is(err, profiles.ErrProfileKeyUnavailable):
		return aiIPCError(err)
	case errors.Is(err, cron.ErrNotFound):
		return ipc.WrapError(ipc.CodeNotFound, cron.ErrNotFound.Error(), err)
	case errors.Is(err, cron.ErrConflict):
		return ipc.WrapError(ipc.CodeBadParam, cron.ErrConflict.Error(), err)
	case errors.Is(err, cron.ErrJobLimit):
		return ipc.WrapError(ipc.CodeBadParam, cron.ErrJobLimit.Error(), err)
	case errors.Is(err, cron.ErrJobRunning):
		return ipc.WrapError(ipc.CodeBadParam, cron.ErrJobRunning.Error(), err)
	case errors.Is(err, cron.ErrInvalidSchedule):
		return ipc.WrapError(ipc.CodeBadParam, err.Error(), err)
	case errors.Is(err, cron.ErrStorage):
		slog.Error("cron IPC storage error", "error", err)
		return ipc.WrapError(ipc.CodeInternal, cron.ErrStorage.Error(), err)
	default:
		return err
	}
}

func (c *cronRuntime) registerCommands(dispatcher *ipc.Dispatcher) error {
	registrations := []func() error{
		func() error {
			return ipc.Register(dispatcher, "cron_register", func(ctx context.Context, _ *ipc.Call, input cronRegisterRequest) (cron.Job, error) {
				if strings.TrimSpace(input.SessionID) == "" {
					return cron.Job{}, ipc.BadParam(errors.New("定时任务必须关联一个会话"))
				}
				if strings.TrimSpace(input.Prompt) == "" {
					return cron.Job{}, ipc.BadParam(errors.New("定时任务的提示词不能为空"))
				}
				if input.TimeoutMS < 0 {
					return cron.Job{}, ipc.BadParam(errors.New("超时时间不能为负数"))
				}
				if c.conversations != nil {
					if _, err := validatedConversationScope(ctx, c.conversations, c.scopeValidator, input.SessionID); err != nil {
						return cron.Job{}, cronIPCError(err)
					}
				}
				if input.ModelProfileID != "" && c.profiles != nil {
					_, ok, err := c.profiles.Profile(ctx, input.ModelProfileID)
					if err != nil {
						return cron.Job{}, cronIPCError(err)
					}
					if !ok {
						return cron.Job{}, cronIPCError(profiles.ErrProfileNotFound)
					}
				}
				job, err := c.scheduler.Register(ctx, cron.Registration{
					SessionID:      input.SessionID,
					Name:           input.Name,
					Prompt:         input.Prompt,
					Schedule:       input.Schedule,
					Timezone:       input.Timezone,
					Disabled:       input.Disabled,
					Timeout:        time.Duration(input.TimeoutMS) * time.Millisecond,
					ModelProfileID: input.ModelProfileID,
				})
				return job, cronIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "cron_list", func(ctx context.Context, _ *ipc.Call, input cronJobRequest) ([]cron.Job, error) {
				if strings.TrimSpace(input.SessionID) == "" {
					return nil, ipc.BadParam(errors.New("定时任务必须关联一个会话"))
				}
				jobs, err := c.scheduler.List(ctx, input.SessionID)
				return jobs, cronIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "cron_get", func(ctx context.Context, _ *ipc.Call, input cronJobRequest) (cron.Job, error) {
				job, err := c.scheduler.Get(ctx, input.SessionID, input.JobID)
				return job, cronIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "cron_set_enabled", func(ctx context.Context, _ *ipc.Call, input cronSetEnabledRequest) (cron.Job, error) {
				job, err := c.scheduler.SetEnabled(ctx, input.SessionID, input.JobID, input.Enabled)
				return job, cronIPCError(err)
			})
		},
		func() error {
			return ipc.Register(dispatcher, "cron_unregister", func(ctx context.Context, _ *ipc.Call, input cronJobRequest) (any, error) {
				return nil, cronIPCError(c.scheduler.Unregister(ctx, input.SessionID, input.JobID))
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
