package production

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/db"
	"github.com/ProbiusOfficial/NexTerm/internal/docker"
	"github.com/ProbiusOfficial/NexTerm/internal/durable"
	"github.com/ProbiusOfficial/NexTerm/internal/forward"
	"github.com/ProbiusOfficial/NexTerm/internal/mount"
	"github.com/ProbiusOfficial/NexTerm/internal/session"
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
	DesktopSmoke            bool
	ForwardPlatform         string
	Connector               session.Connector
	Terminals               session.TerminalFactory
	TaskOptions             tasks.Options
	Docker                  *docker.Service
	DurableBinary           string
	RetentionInterval       time.Duration
	RetentionAttemptTimeout time.Duration
	TakeoverUserClientID    string
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
	if config.TakeoverUserClientID == "" {
		config.TakeoverUserClientID = "desktop"
	}
	database, err := store.OpenWithOptions(ctx, filepath.Join(config.DataDir, "data.db"), store.OpenOptions{Logger: config.Config.Logger})
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
	if connector == nil {
		defaultConnector, err := newProductionConnector(database, credentialVault, config.DataDir)
		if err != nil {
			return nil, err
		}
		connector = defaultConnector
		hostKeys = defaultConnector.hostKeys
	}
	durableBackend, durableErr := durable.New(durable.Config{
		Binary: config.DurableBinary, SocketPath: productionDurableSocketPath(config.DataDir),
		StateDir: filepath.Join(config.DataDir, "durable", "state"),
	})
	if durableErr != nil && !errors.Is(durableErr, durable.ErrUnavailable) {
		return nil, durableErr
	}
	dockerService := config.Docker
	emitter := session.AdaptEmitter(config.Config.Events)
	sessionManager = session.NewManager(session.Config{
		Connector: connector,
		Terminals: config.Terminals,
		Durable:   session.NewDurableProvider(durableBackend),
		Emitter: session.EmitterFunc(func(ctx context.Context, event session.Event) error {
			if dockerService != nil && event.Topic == session.TopicSessionStatus {
				if status, ok := event.Payload.(session.StatusEvent); ok && status.Status != session.StatusConnected && status.Status != session.StatusConnecting {
					_ = dockerService.CloseSession(status.SessionID)
				}
			}
			return emitter.EmitSessionEvent(ctx, event)
		}),
	})
	if dockerService == nil {
		dockerService = newProductionDockerService(sessionManager, database)
	}
	syncService := syncservice.New(database, credentialVault, syncservice.WithMetadata(config.Config.Version, config.Desktop))
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
		Docker:   dockerService, Retention: retention, Durable: durableBackend, DurableErr: durableErr, hostKeys: hostKeys, dataDir: config.DataDir,
		smokeAttach: config.DesktopSmoke,
	}
	if err := composeAIRuntime(ctx, &services, config.TakeoverUserClientID); err != nil {
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

func productionDurableSocketPath(dataDir string) string {
	digest := sha256.Sum256([]byte(dataDir))
	return filepath.Join(os.TempDir(), "nexterm-durable-"+fmt.Sprintf("%x", digest[:8]), "d.sock")
}
