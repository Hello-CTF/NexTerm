package production

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	aicontext "github.com/ProbiusOfficial/NexTerm/internal/ai/context"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/cron"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/memory"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/subagent"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/takeover"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/outcome"
	"github.com/cloudwego/eino/components/model"
)

var productionMemoryScope = memory.Scope{Tenant: "local", Subject: "default"}

func composeAIRuntime(ctx context.Context, services *ProductionServices) error {
	if services.Store == nil || services.Sessions == nil || services.Profiles == nil {
		return errors.New("AI runtime requires store, sessions and profiles services")
	}
	guardManager, err := guard.NewManager(ctx, services.Store)
	if err != nil {
		return err
	}

	outcomeStore, err := outcome.NewSQLiteStore(services.Store.DB())
	if err != nil {
		return err
	}
	outcomeLedger, err := outcome.New(outcome.Options{Store: outcomeStore, Auditor: services.Store.OutcomeAuditor()})
	if err != nil {
		return err
	}

	cronScheduler, cronExecutor, err := composeCronScheduler(ctx, services, cron.Options{})
	if err != nil {
		return err
	}

	toolsDeps := tools.WithSession(tools.Dependencies{}, services.Sessions)
	toolsDeps = tools.WithStore(toolsDeps, services.Store, services.Database)
	toolsDeps.Outcome = outcomeLedger
	toolsDeps.Reminders = reminderScheduler{scheduler: cronScheduler}
	if services.Docker != nil {
		toolsDeps = tools.WithDocker(toolsDeps, services.Docker, "")
	}

	grantsManager, err := guard.NewGrants(ctx, services.Store, func(ctx context.Context, event guard.GrantAuditEvent) error {
		return toolsDeps.Audit(ctx, tools.AuditEntry{AssetID: event.DeviceID, Kind: "ai_grant", Payload: map[string]any{
			"action": event.Action, "kinds": event.Kinds, "operation": event.Operation, "runId": event.RunID,
		}})
	})
	if err != nil {
		return err
	}
	toolsDeps.Grants = grantsManager

	registry := tools.NewRegistry(toolsDeps)

	contextDeps := aicontext.WithSession(aicontext.Dependencies{}, services.Sessions, services.Store, services.Database)
	if services.Docker != nil {
		contextDeps = aicontext.WithDocker(contextDeps, services.Docker)
	}
	builder := aicontext.NewBuilder(contextDeps)

	var memoryStore *memory.Store
	if services.dataDir != "" {
		store, err := memory.Open(ctx, filepath.Join(services.dataDir, "memory.db"))
		if err != nil {
			return err
		}
		memoryStore = store
	}

	runner := agent.NewRunner(agent.Config{
		Profiles:        services.Profiles,
		ModelForProfile: aiProfileModelFactory(services.Profiles),
		Permissions:     guardManager,
		Grants:          grantsManager,
		Permission:      unattendedPermissions(guardManager),
		Tools:           registry,
		Context:         builder,
		Store:           services.Store,
		Runs:            services.Store,
		Checkpoints:     agent.NewStoreCheckpoints(services.Store),
		Subagents: &tools.SubagentConfig{
			Model:           subagent.NewProfileModelFactory(services.Profiles),
			ModelForProfile: subagent.NewProfileModelFactoryByID(services.Profiles),
		},
		Memory:      memoryStore,
		MemoryScope: productionMemoryScope,
	})
	if err := runner.RecoverRuns(ctx); err != nil {
		return err
	}

	takeoverDeps := takeover.Dependencies{
		Model:       aiModelFactory(services.Profiles),
		Permission:  guardManager.Snapshot,
		Grants:      grantsManager,
		Checkpoints: agent.NewStoreCheckpoints(services.Store),
		Audit:       toolsDeps.Audit,
	}
	takeoverDeps = takeover.WithSession(takeoverDeps, services.Sessions)
	takeoverManager := takeover.NewManager(takeoverDeps)

	services.Guard = guardManager
	services.Agent = runner
	services.Takeover = takeoverManager

	cronExecutor.Runner = services.Agent
	services.cron = &cronRuntime{scheduler: cronScheduler, conversations: services.Store, profiles: services.Profiles}

	services.aiRelease = takeover.InstallSessionHooks(services.Sessions, takeoverManager)
	return nil
}

func aiModelFactory(manager *profiles.Manager) agent.ModelFactory {
	return func(ctx context.Context) (model.BaseChatModel, uint64, error) {
		if _, ok := manager.ActiveProfile(); !ok {
			return nil, 0, errors.New("未配置活动 AI 模型")
		}
		client, err := manager.ActiveClient()
		if err != nil {
			return nil, 0, err
		}
		return client.BaseChatModel(ctx)
	}
}

func aiProfileModelFactory(manager *profiles.Manager) agent.ProfileModelFactory {
	return func(ctx context.Context, profileID string) (model.BaseChatModel, uint64, error) {
		client, err := manager.ClientFor(profileID)
		if err != nil {
			return nil, 0, err
		}
		return client.BaseChatModel(ctx)
	}
}

func (s *ProductionServices) closeAIRuntime() {
	if s.cron != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.cron.Shutdown(shutdownCtx)
		s.cron = nil
	}
	if s.aiRelease != nil {
		s.aiRelease()
		s.aiRelease = nil
	}
	if s.Takeover != nil {
		_ = s.Takeover.Shutdown(context.Background())
		s.Takeover = nil
	}
	if s.Agent != nil {
		_ = s.Agent.Close()
		s.Agent = nil
	}
}
