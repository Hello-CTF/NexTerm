package production

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	aicontext "github.com/ProbiusOfficial/NexTerm/internal/ai/context"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/memory"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/subagent"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/takeover"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/outcome"
	"github.com/cloudwego/eino/components/model"
)

// productionMemoryScope is the single owner scope every agent execution uses
// for the long-term semantic memory. NexTerm is a single-user product: one
// local tenant, one default subject. The composition decides it once here so
// neither the runner nor the IPC handlers ever derive or duplicate it.
var productionMemoryScope = memory.Scope{Tenant: "local", Subject: "default"}

// composeAIRuntime builds the single production owner of the AI surface:
// guard settings, the shared tool registry, the opt-in semantic memory store,
// the agent runner and the takeover manager. Every binary gets the same
// composition; the agent and takeover modules only register their own runtime
// commands, while the profiles owner module keeps provider/model/conversation
// commands.
func composeAIRuntime(ctx context.Context, services *ProductionServices, userClientID string) error {
	if services.Store == nil || services.Sessions == nil || services.Profiles == nil {
		return errors.New("AI runtime requires store, sessions and profiles services")
	}
	guardManager, err := guard.NewManager(ctx, services.Store)
	if err != nil {
		return err
	}

	// The outcome ledger is constructed exactly once, here, and reaches every
	// execution through the shared registry dependencies; the agent runner and
	// the subagent children reuse the same registry instead of holding their
	// own ledger. Recorded canonical arguments are persisted unredacted —
	// idempotence comparison needs the byte-exact arguments — so outcome_record
	// and the kind='outcome' audit payload can carry sensitive tool arguments
	// (file contents, sent keys, SQL) in full; the kind='exec' audit keeps its
	// redaction.
	outcomeStore, err := outcome.NewSQLiteStore(services.Store.DB())
	if err != nil {
		return err
	}
	outcomeLedger, err := outcome.New(outcome.Options{Store: outcomeStore, Auditor: services.Store.OutcomeAuditor()})
	if err != nil {
		return err
	}

	toolsDeps := tools.WithSession(tools.Dependencies{}, services.Sessions)
	toolsDeps = tools.WithStore(toolsDeps, services.Store, services.Database)
	toolsDeps.Outcome = outcomeLedger
	if services.Docker != nil {
		toolsDeps = tools.WithDocker(toolsDeps, services.Docker, "")
	}
	registry := tools.NewRegistry(toolsDeps)

	contextDeps := aicontext.WithSession(aicontext.Dependencies{}, services.Sessions, services.Store, services.Database)
	if services.Docker != nil {
		contextDeps = aicontext.WithDocker(contextDeps, services.Docker)
	}
	builder := aicontext.NewBuilder(contextDeps)

	// The opt-in long-term semantic memory store lives at the platform data
	// path as its own self-contained SQLite database. The runner owns it from
	// here on — Close closes the store — and the owner scope is decided once,
	// in this composition, never derived per call: every execution reads and
	// writes under productionMemoryScope, and the restricted memory_* IPC
	// commands stay scope-authorized by the store itself. An empty data dir
	// (tests composing a partial runtime) skips the store instead of leaving
	// a stray database in the working directory.
	var memoryStore *memory.Store
	if services.dataDir != "" {
		store, err := memory.Open(ctx, filepath.Join(services.dataDir, "memory.db"))
		if err != nil {
			return err
		}
		memoryStore = store
	}

	// Subagents enables the bounded spawn tool for every execution: the model
	// factory follows the active profile like the main agent, and the child
	// tool scope is intersected per execution with the parent's enabled set.
	// The Permission hook hands unattended (cron) executions the Unattended
	// decision mode; interactive executions keep the guard manager snapshot.
	runner := agent.NewRunner(agent.Config{
		Profiles:    services.Profiles,
		Permissions: guardManager,
		Permission:  unattendedPermissions(guardManager),
		Tools:       registry,
		Context:     builder,
		Store:       services.Store,
		Subagents:   &tools.SubagentConfig{Model: subagent.NewProfileModelFactory(services.Profiles)},
		Memory:      memoryStore,
		MemoryScope: productionMemoryScope,
	})

	takeoverDeps := takeover.Dependencies{
		Model:      aiModelFactory(services.Profiles),
		Permission: guardManager.Snapshot,
		Audit:      toolsDeps.Audit,
	}
	takeoverDeps = takeover.WithSession(takeoverDeps, services.Sessions, userClientID)
	takeoverManager := takeover.NewManager(takeoverDeps)

	services.Guard = guardManager
	services.Agent = runner
	services.Takeover = takeoverManager

	// The cron scheduler borrows the one agent runner and the application
	// store; the production module list registers its lifecycle component,
	// and construction failures tear it down through closeAIRuntime.
	cronRuntime, err := composeCronRuntime(ctx, services)
	if err != nil {
		return err
	}
	services.cron = cronRuntime

	services.aiRelease = takeover.InstallSessionHooks(services.Sessions, takeoverManager)
	return nil
}

// aiModelFactory resolves the active profile's Eino chat model for the
// takeover runner; the agent runner builds the same factory internally.
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
