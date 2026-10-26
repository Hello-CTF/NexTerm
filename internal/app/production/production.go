package production

import (
	"context"
	"net/http"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/skills"
	"github.com/Hello-CTF/NexTerm/internal/ai/takeover"
	core "github.com/Hello-CTF/NexTerm/internal/app"
	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/forward"
	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/localhelper"
	"github.com/Hello-CTF/NexTerm/internal/mount"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
	"github.com/Hello-CTF/NexTerm/internal/update"
	"github.com/Hello-CTF/NexTerm/internal/vault"
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

// ScreenInsets is the system bar (status bar/navigation bar/notch) occupation on the four edges, in dp.
// Injected by the desktop via the wails Screen API; when server is nil, the system_insets command is not registered.
type ScreenInsets struct {
	Top    int `json:"top"`
	Bottom int `json:"bottom"`
	Left   int `json:"left"`
	Right  int `json:"right"`
}

type ProductionServices struct {
	Store       *store.Store
	Vault       *vault.Vault
	Sync        *syncservice.Service
	Profiles    *profiles.Manager
	Database    *db.Service
	Mount       *mount.Service
	Sessions    *session.Manager
	ExecBatches *execbatch.Service
	Runbook     *runbookIPCService
	Forward     *forward.Service
	Docker      *docker.Service
	Retention   *RetentionRunner
	Guard       *guard.Manager
	Agent       *agent.Runner
	Takeover    *takeover.Manager
	// Skills is the single skills manager instance shared by the ai-skills
	// IPC surface and the agent tools registry (load_skill), so per-owner
	// caches and overrides never split across two managers.
	Skills *skills.Manager
	// LocalHelper runs local terminals in a detached helper; nil keeps them
	// in this process.
	LocalHelper      *localhelper.Service
	Events           ipc.Emitter
	Update           *update.Manager
	ScreenInsets     func(context.Context) (ScreenInsets, error)
	channelBridge    *terminalBridge
	previewRelay     *previewRelayManager
	terminalCommands *terminalCommandService
	fsUploads        *fsChunkUploadRegistry
	hostKeys         *productionHostKeyStore
	sshConnector     *productionConnector
	stagedBlobs      StagedBlobResolver
	dataDir          string
	desktop          bool
	hostKeysShared   bool
	aiRelease        func()
	cron             *cronRuntime
	watches          *watchService
	Transcripts      *transcriptWriter
	aiRetention      *aiRetentionComponent
	inventory        *inventoryService

	retryDeferredUpgrade func(context.Context)
}

type Production struct {
	*Application
	Services       ProductionServices
	accountCleanup accountCleanupState
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
	if services.ExecBatches == nil && services.Sessions != nil {
		var auditor execbatch.Auditor
		var history execbatch.History
		if services.Store != nil {
			auditor = services.Store
			history = services.Store
		}
		services.ExecBatches = newExecBatchServiceWithSettings(services.Sessions, services.Store, auditor, services.Store, history)
	}
	if services.Runbook == nil && services.Sessions != nil {
		services.Runbook = newProductionRunbookIPC(services.ExecBatches, services.Store)
	}
	if services.channelBridge == nil {
		services.channelBridge = newTerminalBridge(services.Sessions, config.Streams)
	}
	if services.previewRelay == nil && services.Sync != nil {
		services.previewRelay = newPreviewRelayManager(services.Sessions, services.Sync.Remote(), services.Sync, nil)
	}
	if services.fsUploads == nil {
		services.fsUploads = newFSChunkUploadRegistry(config.Logger)
	}
	if services.terminalCommands == nil {
		services.terminalCommands = newTerminalCommandService(services.Store, services.Sessions, services.Docker, services.channelBridge, services.hostKeys, services.sshConnector, services.Events, services.stagedBlobs, services.desktop)
	}
	services.terminalCommands.fsUploads = services.fsUploads
	services.terminalCommands.localHelper = services.LocalHelper
	if services.Database != nil {
		services.terminalCommands.databases = services.Database
	}
	if config.RetentionStatus == nil && services.Retention != nil {
		config.RetentionStatus = services.Retention.Status
	}
	if services.inventory == nil && services.Store != nil {
		services.inventory = newInventoryService(services.Store, services.Events, services.desktop, services.dataDir,
			withInventoryDockerAccess(services.Sessions, services.Docker))
	}
	config.Modules = append(productionModules(services), config.Modules...)
	application, err := core.New(config)
	if err != nil {
		return nil, err
	}
	production := &Production{Application: application, Services: services}
	// The runbook service is built above (or supplied); wire it into the
	// account cleanup lifecycle so an owner cleanup also cancels that owner's
	// live runbook runs.
	if services.Runbook != nil {
		production.SetRunbookCleanup(services.Runbook)
	}
	return production, nil
}

func (p *Production) SyncObjectHandler() http.Handler {
	if p.Services.Sync == nil {
		return nil
	}
	return p.Services.Sync.ObjectHandler()
}

// RetryDeferredUpgrade runs only after startup deferred data migration for
// lack of an active superadmin. Errors are reported through logs and AppError.
func (p *Production) RetryDeferredUpgrade(ctx context.Context) {
	if p.Services.retryDeferredUpgrade != nil {
		p.Services.retryDeferredUpgrade(ctx)
	}
}

func productionModules(services ProductionServices) []Module {
	modules := make([]Module, 0, 10)
	if services.Store != nil {
		modules = append(modules, Module{
			Name: "store",
			RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
				if err := registerStoreCommandsWithHostKeyMode(dispatcher, services.Store, services.hostKeys, services.dataDir, services.desktop, services.hostKeysShared); err != nil {
					return err
				}
				if services.Vault != nil {
					return registerSSHImportCommands(dispatcher, services.Store, services.Vault, services.desktop)
				}
				return nil
			},
			Component: closeStoreComponent(services.Store, services.Agent),
		})
		modules = append(modules, Module{
			Name: "production-settings",
			RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
				return registerProductionSettingsCommands(dispatcher, services.Store)
			},
		})
	}
	if services.inventory != nil {
		modules = append(modules, Module{
			Name: "inventory", RegisterCommands: services.inventory.registerCommands, Component: services.inventory,
		})
	}
	if services.Vault != nil {
		modules = append(modules, Module{
			Name: "vault",
			RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
				return registerVaultCommands(dispatcher, services.Vault, services.Store, services.Events)
			},
			Component: newVaultComponent(services.Vault),
		})
	}
	if services.Sync != nil {
		modules = append(modules, Module{Name: "sync", RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
			if err := services.Sync.RegisterCommands(dispatcher); err != nil {
				return err
			}
			if err := registerSyncApplyCommands(dispatcher, services.Sync); err != nil {
				return err
			}
			if err := registerSyncCompareCommands(dispatcher, services.Sync); err != nil {
				return err
			}
			if err := registerRemoteCommands(dispatcher, services.Sync); err != nil {
				return err
			}
			if services.previewRelay != nil {
				if err := registerPreviewRelayCommands(dispatcher, services.previewRelay); err != nil {
					return err
				}
			}
			return nil
		}, Component: services.Sync})
		if services.previewRelay != nil {
			modules = append(modules, Module{Name: "preview-relay", Component: services.previewRelay})
		}
		hostShare := newHostShareIPC(services.Sync.Remote())
		modules = append(modules, Module{
			Name: "host-share-terminal",
			RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
				return registerHostShareCommands(dispatcher, hostShare)
			},
			Component: hostShare,
		})
	}
	if services.Profiles != nil {
		modules = append(modules, Module{
			Name: "ai-profiles",
			RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
				return registerAICommands(dispatcher, services.Profiles, services.Store, productionScopeValidator(&services))
			},
		})
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
		logTails := newLogTailIPCService(services.Sessions, services.desktop)
		runbookIPC := services.Runbook
		if runbookIPC == nil {
			runbookIPC = newProductionRunbookIPC(services.ExecBatches, services.Store)
		}
		modules = append(modules, Module{
			Name: "session",
			RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
				if err := services.terminalCommands.registerSession(dispatcher); err != nil {
					return err
				}
				if err := services.terminalCommands.registerTranscripts(dispatcher); err != nil {
					return err
				}
				if err := registerExecBatchCommands(dispatcher, services.ExecBatches, services.Store); err != nil {
					return err
				}
				if services.Store != nil {
					if err := registerTargetSetCommands(dispatcher, execbatch.NewTargetSetService(services.Store)); err != nil {
						return err
					}
				}
				if err := registerExecBatchProgressCommands(dispatcher, services.ExecBatches); err != nil {
					return err
				}
				if err := registerExecBatchHealthCommands(dispatcher, services.ExecBatches, services.terminalCommands, services.Store); err != nil {
					return err
				}
				if err := runbookIPC.register(dispatcher); err != nil {
					return err
				}
				return registerAssetMaintenanceCommands(dispatcher, services.Store, services.ExecBatches, services.terminalCommands)
			},
			Component: services.Sessions,
		})
		modules = append(modules, Module{
			Name:      "runbook-runs",
			Component: ComponentFuncs{ShutdownFunc: runbookIPC.Shutdown},
		})
		modules = append(modules, Module{
			Name:             "log-tail",
			RegisterCommands: logTails.register,
			Component:        ComponentFuncs{ShutdownFunc: logTails.Shutdown},
		})
	}
	if services.fsUploads != nil {
		modules = append(modules, Module{Name: "fs-chunk-uploads", Component: services.fsUploads})
	}
	if services.Sessions != nil {
		localHelper := services.LocalHelper
		modules = append(modules, Module{
			Name: "local-helper",
			RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
				return registerLocalHelperCommands(dispatcher, localHelper)
			},
			// Shutdown stops watching the helper; the helper and its local
			// terminals keep running after the app exits.
			Component: ComponentFuncs{
				// The desktop starts the helper at launch so the first local
				// terminal is durable; a server starts it with its first local
				// session instead of running a helper nobody uses.
				StartFunc: func(context.Context) error {
					if localHelper != nil && services.desktop {
						localHelper.Start()
					}
					return nil
				},
				ShutdownFunc: func(context.Context) error {
					if localHelper != nil {
						localHelper.Close()
					}
					return nil
				},
			},
		})
	}
	if services.channelBridge != nil {
		modules = append(modules, Module{Name: "terminal-channel-bridge", Component: services.channelBridge})
	}
	if services.Forward != nil {
		modules = append(modules, Module{Name: "forward", RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
			if err := services.Forward.RegisterCommands(dispatcher); err != nil {
				return err
			}
			return services.Forward.RegisterRuleCommands(dispatcher)
		}, Component: services.Forward})
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
	if services.Update != nil {
		modules = append(modules, Module{Name: "update", RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
			return registerUpdateCommands(dispatcher, services.Update)
		}})
	}
	modules = append(modules, Module{Name: "biometric", RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
		return registerBiometricCommands(dispatcher, platformBiometric{}, biometricVaultState(services))
	}})
	if services.dataDir != "" {
		diagnostics := newDiagnosticsService(services.dataDir, services.desktop)
		modules = append(modules, Module{Name: "diagnostics", RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
			return registerDiagnosticsCommands(dispatcher, diagnostics)
		}})
	}
	modules = append(modules, Module{Name: "system-insets", RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
		return ipc.Register(dispatcher, "system_insets", func(ctx context.Context, _ *ipc.Call, _ struct{}) (ScreenInsets, error) {
			if services.ScreenInsets == nil {
				return ScreenInsets{}, nil
			}
			return services.ScreenInsets(ctx)
		})
	}})
	if services.aiRelease != nil {
		modules = append(modules, Module{Name: "ai-session-hooks", Component: aiHooksComponent{release: services.aiRelease}})
	}
	return modules
}

type aiHooksComponent struct{ release func() }

func biometricVaultState(services ProductionServices) vaultUnlockState {
	if services.Vault == nil {
		return nil
	}
	return services.Vault
}

func (c aiHooksComponent) Start(context.Context) error { return nil }

func (c aiHooksComponent) Shutdown(context.Context) error {
	c.release()
	return nil
}
