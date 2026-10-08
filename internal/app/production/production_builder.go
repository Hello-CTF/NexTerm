package production

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/db"
	"github.com/ProbiusOfficial/NexTerm/internal/docker"
	"github.com/ProbiusOfficial/NexTerm/internal/forward"
	"github.com/ProbiusOfficial/NexTerm/internal/mount"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
	sshdaemon "github.com/ProbiusOfficial/NexTerm/internal/ssh"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	syncservice "github.com/ProbiusOfficial/NexTerm/internal/sync"
	"github.com/ProbiusOfficial/NexTerm/internal/tasks"
	"github.com/ProbiusOfficial/NexTerm/internal/vault"
	"github.com/ProbiusOfficial/NexTerm/internal/version"
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
	emitter := session.AdaptEmitter(config.Config.Events)
	transcriptWriter := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: config.Config.Logger,
	})
	var durableResolver session.DurableResolver
	if executable, err := os.Executable(); err == nil {
		durableResolver = &sshdaemon.Resolver{Executable: executable}
	} else {
		config.Config.Logger.Warn("resolve executable for the remote session daemon; SSH durable tabs are disabled", "error", err)
	}
	sessionManager = session.NewManager(session.Config{
		Connector:         connector,
		Terminals:         config.Terminals,
		DurableResolver:   durableResolver,
		TranscriptOffsets: durableTranscriptOffsets{database: database},
		Transcripts:       transcriptWriter,
		Logger:            config.Config.Logger,
		Emitter: session.EmitterFunc(func(ctx context.Context, event session.Event) error {
			if dockerService != nil && event.Topic == session.TopicSessionStatus {
				if status, ok := event.Payload.(session.StatusEvent); ok && status.Status != session.StatusConnected && status.Status != session.StatusConnecting {
					if err := dockerService.CloseSession(status.SessionID); err != nil {
						config.Config.Logger.Warn("close docker session on status change failed", "session", status.SessionID, "error", err)
					}
				}
			}
			return emitter.EmitSessionEvent(ctx, event)
		}),
	})
	if dockerService == nil {
		dockerService = newProductionDockerService(sessionManager, database)
	}
	syncService := syncservice.New(database, credentialVault, syncservice.WithMetadata(config.Config.Version, config.Desktop), syncservice.WithGatewayAuthKey(os.Getenv("NEXTERM_GATEWAY_AUTH")))
	retention, err := NewRetentionRunner(RetentionConfig{
		Store: database, Policy: StoreRetentionPolicy(database), Interval: config.RetentionInterval,
		AttemptTimeout: config.RetentionAttemptTimeout, Logger: config.Config.Logger,
	})
	if err != nil {
		return nil, err
	}
	services = ProductionServices{
		Store: database, Vault: credentialVault, Sync: syncService, Profiles: profileManager,
		Tasks:    taskManager,
		Database: db.NewService(db.NewStoreAssetResolver(database, credentialVault)),
		Mount:    mount.NewService(mount.Config{Auditor: database}),
		Sessions: sessionManager,
		Forward:  forward.NewService(forward.Config{Provider: sessionManager, Policy: forward.Policy{Desktop: config.Desktop, Platform: config.ForwardPlatform}}),
		Docker:   dockerService, Retention: retention, hostKeys: hostKeys, sshConnector: sshConnector, dataDir: config.DataDir,
		desktop:     config.Desktop,
		Transcripts: transcriptWriter,
		aiRetention: newAIRetentionComponent(database, config.RetentionInterval),
		Events:      config.Config.Events,
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
