package production

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/db"
	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/Hello-CTF/NexTerm/internal/forward"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/mount"
	"github.com/Hello-CTF/NexTerm/internal/session"
	sshdaemon "github.com/Hello-CTF/NexTerm/internal/ssh"
	"github.com/Hello-CTF/NexTerm/internal/store"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
	"github.com/Hello-CTF/NexTerm/internal/tasks"
	"github.com/Hello-CTF/NexTerm/internal/update"
	"github.com/Hello-CTF/NexTerm/internal/vault"
	"github.com/Hello-CTF/NexTerm/internal/version"
)

type ProductionConfig struct {
	Config                  Config
	DataDir                 string
	Desktop                 bool
	ForwardPlatform         string
	Connector               session.Connector
	Terminals               session.TerminalFactory
	TaskOptions             tasks.Options
	Docker                  *docker.Service
	RetentionInterval       time.Duration
	RetentionAttemptTimeout time.Duration
	DB                      store.BackendConfig
	// StagedBlobs is injected by the server-side assembly (nexterm-server), resolving relative paths from blob Stage/Reserve responses to staged file paths; nil on desktop.
	StagedBlobs StagedBlobResolver
	// RestartFunc is injected by the desktop: start a new process and exit the current wails app; when server is nil,
	// app_restart degrades to an unavailability reason.
	RestartFunc func(context.Context, string) error
	// ScreenInsets is injected by the desktop via the wails Screen API; nil on the server.
	ScreenInsets func(context.Context) (ScreenInsets, error)
}

func NewProduction(ctx context.Context, config ProductionConfig) (_ *Production, returnErr error) {
	if config.DataDir == "" {
		return nil, fmt.Errorf("production data directory is required")
	}
	dataDir, err := filepath.Abs(config.DataDir)
	if err != nil {
		return nil, err
	}
	config.DataDir = dataDir
	if err := os.MkdirAll(config.DataDir, 0o700); err != nil {
		return nil, err
	}
	if config.Config.Logger == nil {
		config.Config.Logger = slog.Default()
	}
	if config.Config.Version == "" {
		config.Config.Version = version.Version
	}
	database, err := openProductionStore(ctx, config)
	if err != nil {
		return nil, err
	}
	var taskManager *tasks.Manager
	var sessionManager *session.Manager
	var services ProductionServices
	defer func() {
		if returnErr == nil {
			return
		}
		if services.Docker != nil {
			_ = services.Docker.Close()
		}
		if services.Forward != nil {
			_ = services.Forward.Close()
		}
		if sessionManager != nil {
			_ = sessionManager.Close()
		}
		if services.Database != nil {
			_ = services.Database.Close()
		}
		if taskManager != nil {
			_ = taskManager.Close(context.Background())
		}
		_ = database.Close()
	}()

	credentialVault := vault.Load(ctx, database)
	if config.Desktop && !credentialVault.Status().Initialized {
		if err := credentialVault.InitMaster(ctx, ""); err != nil {
			return nil, err
		}
		config.Config.Logger.Info("凭据库未初始化，已自动完成无密码初始化")
	}
	profileManager, err := profiles.NewManager(ctx, database)
	if err != nil {
		return nil, err
	}
	taskOptions := config.TaskOptions
	taskOptions.Dir = filepath.Join(config.DataDir, "tasks")
	taskManager, err = tasks.Open(taskOptions)
	if err != nil {
		return nil, err
	}
	connector := config.Connector
	var hostKeys *productionHostKeyStore
	var sshConnector *productionConnector
	if connector == nil {
		defaultConnector, err := newProductionConnector(database, credentialVault, config.DataDir, config.Config.Events)
		if err != nil {
			return nil, err
		}
		connector = defaultConnector
		hostKeys = defaultConnector.hostKeys
		sshConnector = defaultConnector
	}
	dockerService := config.Docker
	var forwardService *forward.Service
	emitter := session.AdaptEmitter(config.Config.Events)
	transcriptWriter := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: config.Config.Logger,
	})
	var durableResolver session.DurableResolver
	var sshResolver *sshdaemon.Resolver
	executablePath, executableErr := os.Executable()
	if executableErr == nil {
		sshResolver = &sshdaemon.Resolver{Executable: executablePath}
	} else {
		config.Config.Logger.Warn("resolve executable for the remote session daemon; SSH durable tabs are disabled", "error", executableErr)
	}
	durableResolver = productionDurableResolver{ssh: sshResolver}
	sessionManager = session.NewManager(session.Config{
		Connector:         connector,
		Terminals:         config.Terminals,
		DurableResolver:   durableResolver,
		TranscriptOffsets: durableTranscriptOffsets{database: database},
		TabTitles:         durableTabTitles{database: database},
		Transcripts:       transcriptWriter,
		Commands:          commandLogSink{database: database, logger: config.Config.Logger},
		Logger:            config.Config.Logger,
		Emitter: session.EmitterFunc(func(ctx context.Context, event session.Event) error {
			if event.Topic == session.TopicSessionStatus {
				if status, ok := event.Payload.(session.StatusEvent); ok && status.Status != session.StatusConnected && status.Status != session.StatusConnecting {
					if dockerService != nil {
						if err := dockerService.CloseSession(status.SessionID); err != nil {
							config.Config.Logger.Warn("close docker session on status change failed", "session", status.SessionID, "error", err)
						}
					}
					if forwardService != nil && status.Status != session.StatusReconnecting {
						forwardService.CloseSession(status.SessionID)
					}
				}
			}
			return emitter.EmitSessionEvent(ctx, event)
		}),
	})
	if dockerService == nil {
		dockerService = newProductionDockerService(sessionManager, database)
	}
	syncService := syncservice.New(database, credentialVault, syncservice.WithMetadata(config.Config.Version, config.Desktop), syncservice.WithGatewayAuthKey(os.Getenv("NEXTERM_GATEWAY_AUTH")), syncservice.WithEvents(config.Config.Events), syncservice.WithProfilesReload(profileManager.Reload), syncservice.WithLogger(config.Config.Logger))
	if config.Desktop && sshConnector != nil {
		sshConnector.device.Store(&DeviceConnectorConfig{
			Dial: syncService.Remote().DialDeviceBridge,
			Digest: func(ctx context.Context, deviceID string) (string, error) {
				devices, err := syncService.Remote().ListDevices(ctx)
				if err != nil {
					return "", err
				}
				for _, listed := range devices.Devices {
					if listed.ID != deviceID {
						continue
					}
					if listed.Agent == nil || listed.Agent.StateDigest == "" {
						return "", ipc.NewError(ipc.CodeDisconnected, "设备未返回终端状态，可能已经离线")
					}
					return listed.Agent.StateDigest, nil
				}
				return "", ipc.NewError(ipc.CodeNotFound, "设备不存在或已吊销")
			},
		})
	}
	retention, err := NewRetentionRunner(RetentionConfig{
		Store: database, Policy: StoreRetentionPolicy(database), Interval: config.RetentionInterval,
		AttemptTimeout: config.RetentionAttemptTimeout, Logger: config.Config.Logger,
	})
	if err != nil {
		return nil, err
	}
	forwardService = forward.NewService(forward.Config{Provider: sessionManager, Policy: forward.Policy{Desktop: config.Desktop, Platform: config.ForwardPlatform}})
	services = ProductionServices{
		Store: database, Vault: credentialVault, Sync: syncService, Profiles: profileManager,
		Tasks:    taskManager,
		Database: db.NewService(db.NewStoreAssetResolver(database, credentialVault)),
		Mount:    mount.NewService(mount.Config{Auditor: database}),
		Sessions: sessionManager,
		Forward:  forwardService,
		Docker:   dockerService, Retention: retention, hostKeys: hostKeys, sshConnector: sshConnector, dataDir: config.DataDir,
		desktop:     config.Desktop,
		stagedBlobs: config.StagedBlobs,
		Transcripts: transcriptWriter,
		aiRetention: newAIRetentionComponent(database, config.RetentionInterval),
		Events:      config.Config.Events,
	}
	services.ScreenInsets = config.ScreenInsets
	if executableErr == nil {
		services.Update = &update.Manager{
			CurrentVersion: config.Config.Version,
			GOOS:           runtime.GOOS,
			GOARCH:         runtime.GOARCH,
			CanInstall:     config.Desktop,
			ExePath:        executablePath,
			RestartFunc:    config.RestartFunc,
			Emit: func(ctx context.Context, progress update.Progress) {
				_ = ipc.Emit(ctx, config.Config.Events, ipc.TopicUpdateProgress, progress)
			},
		}
	}
	if err := composeAIRuntime(ctx, &services); err != nil {
		services.closeAIRuntime()
		return nil, err
	}
	production, err := NewProductionWithServices(config.Config, services)
	if err != nil {
		services.closeAIRuntime()
		return nil, err
	}
	return production, nil
}

func openProductionStore(ctx context.Context, config ProductionConfig) (*store.Store, error) {
	if config.DB.Backend == store.BackendPostgres {
		return store.OpenBackend(ctx, config.DB, store.OpenOptions{Logger: config.Config.Logger})
	}
	return store.OpenWithOptions(ctx, filepath.Join(config.DataDir, "data.db"), store.OpenOptions{Logger: config.Config.Logger})
}
