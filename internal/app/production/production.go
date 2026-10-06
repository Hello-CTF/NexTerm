package production

import (
	"context"
	"net/http"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/takeover"
	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/db"
	"github.com/ProbiusOfficial/NexTerm/internal/docker"
	"github.com/ProbiusOfficial/NexTerm/internal/forward"
	"github.com/ProbiusOfficial/NexTerm/internal/hub"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/mount"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	syncservice "github.com/ProbiusOfficial/NexTerm/internal/sync"
	"github.com/ProbiusOfficial/NexTerm/internal/tasks"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
)

type Application = core.Application
type Config = core.Config
type Module = core.Module
type Component = core.Component
type ComponentFuncs = core.ComponentFuncs
type RetentionRunner = core.RetentionRunner
type RetentionConfig = core.RetentionConfig

var NewRetentionRunner = core.NewRetentionRunner
var StoreRetentionPolicy = core.StoreRetentionPolicy

type ProductionServices struct {
	Store            *store.Store
	Vault            *vault.Vault
	Sync             *syncservice.Service
	Profiles         *profiles.Manager
	Tasks            *tasks.Manager
	Database         *db.Service
	Mount            *mount.Service
	Sessions         *session.Manager
	Forward          *forward.Service
	Docker           *docker.Service
	Retention        *RetentionRunner
	Guard            *guard.Manager
	Agent            *agent.Runner
	Takeover         *takeover.Manager
	Events           ipc.Emitter
	channelBridge    *terminalBridge
	terminalCommands *terminalCommandService
	hostKeys         *productionHostKeyStore
	sshConnector     *productionConnector
	dataDir          string
	aiRelease        func()
	cron             *cronRuntime
	Transcripts      *transcriptWriter
	aiRetention      *aiRetentionComponent
}

type Production struct {
	*Application
	Services ProductionServices
}

func NewProductionWithServices(config Config, services ProductionServices) (*Production, error) {
	if config.VaultStatus == nil && services.Vault != nil {
		config.VaultStatus = func(context.Context) (any, error) {
			return services.Vault.Status(), nil
		}
	}
	if config.Streams == nil && services.Sessions != nil {
		config.Streams = hub.StreamFactory{Hub: services.Sessions.Hub()}
	}
	if services.channelBridge == nil {
		services.channelBridge = newTerminalBridge(services.Sessions, config.Streams)
	}
	if services.terminalCommands == nil {
		services.terminalCommands = newTerminalCommandService(services.Store, services.Sessions, services.Docker, services.channelBridge, services.hostKeys, services.sshConnector)
	}
	if config.RetentionStatus == nil && services.Retention != nil {
		config.RetentionStatus = services.Retention.Status
	}
	config.Modules = append(productionModules(services), config.Modules...)
	application, err := core.New(config)
	if err != nil {
		return nil, err
	}
	return &Production{Application: application, Services: services}, nil
}

func (p *Production) SyncObjectHandler() http.Handler {
	if p.Services.Sync == nil {
		return nil
	}
	return p.Services.Sync.ObjectHandler()
}

func productionModules(services ProductionServices) []Module {
	modules := make([]Module, 0, 10)
	if services.Store != nil {
		modules = append(modules, Module{
			Name: "store",
			RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
				if err := registerFilesCommands(dispatcher, services.Store); err != nil {
					return err
				}
				return registerStoreCommands(dispatcher, services.Store, services.hostKeys, services.dataDir)
			},
			Component: closeStoreComponent(services.Store, services.Agent),
		})
	}
	if services.Vault != nil {
		modules = append(modules, Module{
			Name: "vault",
			RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
				return registerVaultCommands(dispatcher, services.Vault, services.Store)
			},
			Component: newVaultComponent(services.Vault),
		})
	}
	if services.Sync != nil {
		modules = append(modules, Module{Name: "sync", RegisterCommands: services.Sync.RegisterCommands, Component: services.Sync})
	}
	if services.Profiles != nil {
		modules = append(modules, Module{
			Name: "ai-profiles",
			RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
				return registerAICommands(dispatcher, services.Profiles, services.Store)
			},
		})
	}
	if services.Tasks != nil {
		modules = append(modules, Module{Name: "tasks", Component: closeTasksComponent(services.Tasks)})
	}
	if services.Database != nil {
		modules = append(modules, Module{Name: "database", RegisterCommands: services.Database.RegisterCommands, Component: services.Database})
	}
	if services.Mount != nil {
		modules = append(modules, Module{Name: "mount", RegisterCommands: services.Mount.RegisterCommands, Component: services.Mount})
	}
	if services.Transcripts != nil {
		modules = append(modules, Module{Name: "transcript-writer", Component: services.Transcripts})
	}
	if services.Sessions != nil {
		modules = append(modules, Module{
			Name: "session",
			RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
				if err := services.terminalCommands.registerSession(dispatcher); err != nil {
					return err
				}
				return services.terminalCommands.registerTranscripts(dispatcher)
			},
			Component: services.Sessions,
		})
	}
	if services.channelBridge != nil {
		modules = append(modules, Module{Name: "terminal-channel-bridge", Component: services.channelBridge})
	}
	if services.Forward != nil {
		modules = append(modules, Module{Name: "forward", RegisterCommands: services.Forward.RegisterCommands, Component: services.Forward})
	}
	if services.Docker != nil {
		modules = append(modules, Module{Name: "docker", RegisterCommands: services.terminalCommands.registerDocker, Component: closeDockerComponent(services.Docker)})
	}
	if services.Retention != nil {
		modules = append(modules, Module{Name: "retention", Component: services.Retention})
	}
	if services.Agent != nil {
		modules = append(modules, agent.Module(services.Agent))
	}
	if services.cron != nil {
		modules = append(modules, cronModule(services.cron))
	}
	if services.aiRetention != nil {
		modules = append(modules, Module{Name: "ai-retention", Component: services.aiRetention})
	}
	if services.Takeover != nil {
		modules = append(modules, takeover.Module(services.Takeover))
	}
	if services.aiRelease != nil {
		modules = append(modules, Module{Name: "ai-session-hooks", Component: aiHooksComponent{release: services.aiRelease}})
	}
	return modules
}

type aiHooksComponent struct{ release func() }

func (c aiHooksComponent) Start(context.Context) error { return nil }

func (c aiHooksComponent) Shutdown(context.Context) error {
	c.release()
	return nil
}
