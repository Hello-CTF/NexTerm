package production

import (
	"context"
	"errors"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	aicontext "github.com/ProbiusOfficial/NexTerm/internal/ai/context"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/takeover"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/components/model"
)

// composeAIRuntime builds the single production owner of the AI surface:
// guard settings, the shared tool registry, the agent runner and the
// takeover manager. Every binary gets the same composition; the agent and
// takeover modules only register their own runtime commands, while the
// profiles owner module keeps provider/model/conversation commands.
func composeAIRuntime(ctx context.Context, services *ProductionServices, userClientID string) error {
	if services.Store == nil || services.Sessions == nil || services.Profiles == nil {
		return errors.New("AI runtime requires store, sessions and profiles services")
	}
	guardManager, err := guard.NewManager(ctx, services.Store)
	if err != nil {
		return err
	}

	toolsDeps := tools.WithSession(tools.Dependencies{}, services.Sessions)
	toolsDeps = tools.WithStore(toolsDeps, services.Store, services.Database)
	if services.Docker != nil {
		toolsDeps = tools.WithDocker(toolsDeps, services.Docker, "")
	}
	registry := tools.NewRegistry(toolsDeps)

	contextDeps := aicontext.WithSession(aicontext.Dependencies{}, services.Sessions, services.Store, services.Database)
	if services.Docker != nil {
		contextDeps = aicontext.WithDocker(contextDeps, services.Docker)
	}
	builder := aicontext.NewBuilder(contextDeps)

	runner := agent.NewRunner(agent.Config{
		Profiles:    services.Profiles,
		Permissions: guardManager,
		Tools:       registry,
		Context:     builder,
		Store:       services.Store,
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
