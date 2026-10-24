package production

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/profiles"
	"github.com/Hello-CTF/NexTerm/internal/ai/skills"
	"github.com/Hello-CTF/NexTerm/internal/docker"
	"github.com/Hello-CTF/NexTerm/internal/forward"
	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/mount"
	"github.com/Hello-CTF/NexTerm/internal/session"
	sshdaemon "github.com/Hello-CTF/NexTerm/internal/ssh"
	"github.com/Hello-CTF/NexTerm/internal/store"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/update"
	"github.com/Hello-CTF/NexTerm/internal/upgrade"
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
	Docker                  *docker.Service
	RetentionInterval       time.Duration
	RetentionAttemptTimeout time.Duration
	DB                      store.BackendConfig
	// StagedBlobs is injected by the server-side assembly (nexterm-server), resolving relative paths from blob Stage/Reserve responses to staged file paths; nil on desktop.
	StagedBlobs StagedBlobResolver
	// ForwardReaperInterval tunes the chunked-upload crash staging reaper sweep interval; non-positive values default to one hour.
	ForwardReaperInterval time.Duration
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
	legacyTasksDir := filepath.Join(config.DataDir, "tasks")
	if _, err := os.Stat(legacyTasksDir); err == nil {
		config.Config.Logger.Info("legacy persistent task data directory preserved; tasks are no longer loaded", "path", legacyTasksDir)
	}
	if config.Config.Version == "" {
		config.Config.Version = version.Version
	}
	database, err := openProductionStore(ctx, config)
	if err != nil {
		return nil, err
	}
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
		_ = database.Close()
	}()

	shared := config.Desktop || owner.OpenAccess(ctx)
	database.SetAIHistoryShared(shared)
	dataUpgrade := &productionUpgradeCoordinator{
		database: database, shared: shared, logger: config.Config.Logger, events: config.Config.Events,
	}
	if err := dataUpgrade.migrate(ctx); err != nil {
		return nil, err
	}
	credentialVault := vault.Load(ctx, database)
	if config.Desktop && !credentialVault.Status().Initialized {
		if err := credentialVault.InitMaster(ctx, ""); err != nil {
			return nil, err
		}
		config.Config.Logger.Info("credential vault was not initialized; completed passwordless initialization automatically")
	}
	profileManager, err := profiles.NewManager(ctx, database, profiles.WithShared(config.Desktop))
	if err != nil {
		return nil, err
	}
	connector := config.Connector
	var hostKeys *productionHostKeyStore
	var sshConnector *productionConnector
	if connector == nil {
		defaultConnector, err := newProductionConnector(ctx, database, credentialVault, config.DataDir, config.Config.Events, config.Desktop || owner.OpenAccess(ctx))
		if err != nil {
			return nil, err
		}
		connector = defaultConnector
		hostKeys = defaultConnector.hostKeys
		sshConnector = defaultConnector
		report := hostKeys.migration
		if report.Conflicts > 0 || report.Skipped > 0 {
			config.Config.Logger.Warn("legacy SSH host key migration left entries unattended", "conflicts", report.Conflicts, "skipped", report.Skipped)
		} else if report.BackupCreated || report.Copied > 0 {
			config.Config.Logger.Info("migrated legacy SSH host keys", "backup_created", report.BackupCreated, "copied", report.Copied, "already_present", report.Existing)
		}
	}
	dockerService := config.Docker
	var forwardService *forward.Service
	var forwardRuntime *forwardRuntime
	fsUploads := newFSChunkUploadRegistry(config.Config.Logger)
	emitter := session.AdaptEmitter(config.Config.Events)
	transcriptWriter := newTranscriptWriter(transcriptWriterConfig{
		Database: database, Logger: config.Config.Logger,
		RequireOwner: !(config.Desktop || owner.OpenAccess(ctx)),
	})
	var durableResolver session.DurableResolver
	var sshResolver *sshdaemon.Resolver
	executablePath, executableErr := os.Executable()
	if executableErr == nil {
		sshResolver = &sshdaemon.Resolver{Executable: executablePath, UploadBytesPerSecond: helperUploadLimitFromEnv(config.Config.Logger)}
	} else {
		config.Config.Logger.Warn("resolve executable for the remote session daemon; SSH durable tabs are disabled", "error", executableErr)
	}
	helperService := &productionHelperService{ssh: sshResolver}
	durableResolver = productionDurableResolver{ssh: sshResolver, events: config.Config.Events, helpers: helperService}
	sessionManager = session.NewManager(session.Config{
		Connector:            connector,
		Terminals:            config.Terminals,
		DurableResolver:      durableResolver,
		TerminalModeResolver: newProductionTerminalModeResolver(database),
		TranscriptOffsets:    durableTranscriptOffsets{database: database},
		TabTitles:            durableTabTitles{database: database},
		Transcripts:          transcriptWriter,
		Commands:             commandLogSink{database: database, logger: config.Config.Logger},
		Logger:               config.Config.Logger,
		OpenAccess:           config.Desktop || owner.OpenAccess(ctx),
		Emitter: session.EmitterFunc(func(ctx context.Context, event session.Event) error {
			if event.Topic == session.TopicSessionStatus {
				if status, ok := event.Payload.(session.StatusEvent); ok {
					if dockerService != nil && status.Status != session.StatusConnected && status.Status != session.StatusConnecting {
						if err := dockerService.CloseSession(status.SessionID); err != nil {
							config.Config.Logger.Warn("close docker session on status change failed", "session", status.SessionID, "error", err)
						}
					}
					if forwardService != nil {
						switch status.Status {
						case session.StatusReconnecting:
							forwardService.SuspendRemote(status.SessionID)
						case session.StatusConnected:
							go forwardService.RestoreRemote(productionSessionRequestContext(context.WithoutCancel(ctx)), status.SessionID)
							if forwardRuntime != nil {
								go forwardRuntime.SessionConnected(productionSessionRequestContext(context.WithoutCancel(ctx)), status.SessionID)
							}
						case session.StatusDisconnected, session.StatusFailed:
							reason := status.Error
							if reason == "" {
								reason = "会话已断开，请连接后重试转发"
							}
							forwardService.FailSession(status.SessionID, reason)
						}
					}
					if status.Status == session.StatusDisconnected || status.Status == session.StatusFailed {
						if err := fsUploads.closeSession(status.SessionID); err != nil {
							config.Config.Logger.Warn("close chunked uploads on session status failed", "session", status.SessionID, "error", err)
						}
					}
				}
			}
			if services.watches != nil {
				services.watches.onSessionEvent(ctx, event)
			}
			return emitter.EmitSessionEvent(ctx, event)
		}),
	})
	helperService.sessions = sessionManager
	streams := config.Config.Streams
	if streams == nil {
		streams = hub.StreamFactory{Hub: sessionManager.Hub()}
	}
	config.Config.Streams = &channelOwnerStreamFactory{factory: streams, sessions: sessionManager}
	if dockerService == nil {
		dockerService = newProductionDockerService(sessionManager, database)
	}
	syncService := syncservice.New(database, credentialVault, syncservice.WithMetadata(config.Config.Version, config.Desktop), syncservice.WithGatewayAuthKey(os.Getenv("NEXTERM_GATEWAY_AUTH")), syncservice.WithEvents(config.Config.Events), syncservice.WithProfilesReload(profileManager.Reload), syncservice.WithLogger(config.Config.Logger))
	if config.Desktop && sshConnector != nil {
		sshConnector.device.Store(&DeviceConnectorConfig{
			Dial: syncService.Remote().DialDeviceBridge,
			Platform: func(ctx context.Context, deviceID string) (string, error) {
				devices, err := syncService.Remote().ListDevices(ctx)
				if err != nil {
					return "", err
				}
				for _, listed := range devices.Devices {
					if listed.ID == deviceID && listed.Agent != nil {
						return listed.Agent.Platform, nil
					}
				}
				return "", nil
			},
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
	forwardService = forward.NewService(forward.Config{
		Provider: forward.DialerProviderFunc(sessionManager.CurrentDialerFor),
		Rules:    database,
		IsProduction: func(ctx context.Context, environment string) (bool, error) {
			return guard.IsProductionEnvironment(ctx, database, environment)
		},
		SessionHost: func(ctx context.Context, sessionID string) (string, error) {
			connected, err := sessionManager.SessionFor(productionSessionRequestContext(ctx), sessionID)
			if err != nil {
				return "", err
			}
			return connected.Asset().ID, nil
		},
		Policy:  forward.Policy{Desktop: config.Desktop, Platform: config.ForwardPlatform},
		OnError: func(err error) { config.Config.Logger.Warn("forward lifecycle error", "error", err) },
	})
	// The forward runtime drives auto-start rules on connect, opt-in startup
	// recovery, and the crash staging reaper over the chunked-upload registry's
	// known staging directories; upload begin resumes crashed staging through
	// the runtime so resume and reaping share one lifecycle.
	fsUploads.targetsPath = filepath.Join(config.DataDir, "chunked-staging-targets.json")
	fsUploads.sessionAsset = func(ctx context.Context, sessionID string) (string, error) {
		connected, err := sessionManager.SessionFor(productionSessionRequestContext(ctx), sessionID)
		if err != nil {
			return "", err
		}
		return connected.Asset().ID, nil
	}
	fsUploads.reapFilesystem = func(ctx context.Context, owner, assetID string) (base.FileSystem, error) {
		ownerCtx := ownerContext(ctx, owner)
		infos, err := sessionManager.ListSessionsFor(ownerCtx)
		if err != nil {
			return nil, err
		}
		for _, info := range infos {
			if info.AssetID != assetID {
				continue
			}
			transport, err := sessionManager.TransportFor(ownerCtx, info.ID)
			if err != nil {
				return nil, err
			}
			provider, ok := transport.(base.FileTransport)
			if !ok {
				return nil, fmt.Errorf("%w: session transport has no filesystem", base.ErrUnsupported)
			}
			return provider.FileSystem(ownerCtx)
		}
		return nil, fmt.Errorf("no live session for asset %s: %w", assetID, session.ErrSessionNotFound)
	}
	forwardRuntime = newForwardRuntime(forwardRuntimeConfig{
		Forward:        forwardService,
		Sessions:       sessionManager,
		Store:          database,
		Logger:         config.Config.Logger,
		ReaperInterval: config.ForwardReaperInterval, ReaperMinAge: defaultForwardReaperMinAge,
		ReapTargets:          fsUploads.stagingTargets,
		RecoverySettingsPath: filepath.Join(config.DataDir, "forward-recovery.json"),
	})
	fsUploads.resumeStaging = forwardRuntime.ResumeUploadStaging
	services = ProductionServices{
		Store: database, Vault: credentialVault, Sync: syncService, Profiles: profileManager,
		Database: newProductionDatabaseService(database, credentialVault, sessionManager),
		Mount: mount.NewService(mount.Config{Auditor: database, SessionHost: func(ctx context.Context, sessionID string) (string, error) {
			connected, err := sessionManager.SessionFor(productionSessionRequestContext(ctx), sessionID)
			if err != nil {
				return "", err
			}
			return connected.Asset().ID, nil
		}}),
		Sessions: sessionManager,
		Forward:  forwardService,
		Docker:   dockerService, Retention: retention, hostKeys: hostKeys, sshConnector: sshConnector, dataDir: config.DataDir,
		desktop:        config.Desktop,
		hostKeysShared: config.Desktop || owner.OpenAccess(ctx),
		stagedBlobs:    config.StagedBlobs,
		fsUploads:      fsUploads,
		Transcripts:    transcriptWriter,
		aiRetention:    newAIRetentionComponent(database, config.RetentionInterval),
		Events:         config.Config.Events,
	}
	services.channelBridge = newTerminalBridge(sessionManager, streams)
	services.retryDeferredUpgrade = dataUpgrade.retry
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
	// The skills manager is built before the AI runtime so composeAIRuntime
	// can hand the very same instance to the tools registry (load_skill) that
	// the ai-skills IPC module later registers: one manager, one per-owner
	// cache, no split state.
	skillsManager, err := skills.NewManager(ctx, database, skills.WithShared(config.Desktop || owner.OpenAccess(ctx)))
	if err != nil {
		return nil, err
	}
	services.Skills = skillsManager
	if err := composeAIRuntime(ctx, &services); err != nil {
		services.closeAIRuntime()
		return nil, err
	}
	config.Config.Modules = append([]Module{
		{Name: "ai-skills", RegisterCommands: func(dispatcher *ipc.Dispatcher) error {
			return skills.RegisterIPC(dispatcher, services.Skills)
		}},
		{Name: "watch", RegisterCommands: services.watches.registerCommands, Component: services.watches},
		{Name: "forward-runtime", RegisterCommands: forwardRuntime.registerCommands, Component: forwardRuntime},
	}, config.Config.Modules...)
	production, err := NewProductionWithServices(config.Config, services)
	if err != nil {
		services.closeAIRuntime()
		return nil, err
	}
	production.Services.terminalCommands.helpers = helperService
	return production, nil
}

type productionUpgradeCoordinator struct {
	database *store.Store
	shared   bool
	logger   *slog.Logger
	events   ipc.Emitter

	mu       sync.Mutex
	deferred bool
}

func (c *productionUpgradeCoordinator) migrate(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	report, err := upgrade.Migrate(ctx, c.database.GORM(), c.shared)
	if err != nil {
		return err
	}
	c.deferred = report.DeferredNoActiveSuperadmin
	c.report(ctx, report)
	return nil
}

func (c *productionUpgradeCoordinator) retry(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.deferred {
		return
	}
	report, err := upgrade.Migrate(ctx, c.database.GORM(), c.shared)
	if err != nil {
		c.logger.Error("data upgrade retry after active superadmin creation failed", "error", err)
		c.emit(ctx, "首个超级管理员创建后的数据升级重试失败；原始数据已保留，请查看服务器日志")
		return
	}
	c.deferred = report.DeferredNoActiveSuperadmin
	c.report(ctx, report)
}

func (c *productionUpgradeCoordinator) report(ctx context.Context, report upgrade.Report) {
	aiSettings := report.AISettings.Total()
	values := []any{
		"ownership_assigned", report.Ownership.Assigned,
		"ownership_conflicts", report.Ownership.Conflicts,
		"layout_copied", report.Layout.Copied,
		"layout_existing", report.Layout.Existing,
		"layout_conflicts", report.Layout.Conflicts,
		"cron_assigned", report.AI.CronJobsAssigned,
		"cron_quarantined", report.AI.CronJobsQuarantined,
		"runs_assigned", report.AI.RunsAssigned,
		"messages_assigned", report.AI.MessagesAssigned,
		"run_events_assigned", report.AI.RunEventsAssigned,
		"hitl_runs_assigned", report.AI.HitlRunsAssigned,
		"checkpoints_assigned", report.AI.CheckpointsAssigned,
		"unattributed", report.AI.Unattributed,
		"ai_settings_migrated", aiSettings.Migrated,
		"ai_settings_existing", aiSettings.Existing,
		"ai_settings_conflicts", aiSettings.Conflicts,
		"ai_settings_tombstoned", aiSettings.Tombstoned,
		"ai_settings_ledger_entries", report.AISettings.LedgerEntries,
	}
	if report.DeferredNoActiveSuperadmin {
		c.logger.Warn("data upgrade deferred until an active superadmin is created", values...)
		c.emit(ctx, "数据升级已暂缓：等待首个 active superadmin；保留与孤立记录计数请查看服务器日志")
		return
	}
	conflicts := report.Ownership.Conflicts + report.Layout.Conflicts + aiSettings.Conflicts
	if conflicts > 0 || report.AI.CronJobsQuarantined > 0 || report.AI.Unattributed > 0 || aiSettings.Tombstoned > 0 {
		c.logger.Warn("data upgrade completed with preserved or unattended entries", values...)
		c.emit(ctx, "数据升级存在未自动处理的冲突或孤立记录；原始记录已保留，请查看服务器日志")
		return
	}
	changed := report.Ownership.Assigned+report.Layout.Copied+aiSettings.Migrated+aiSettings.Tombstoned+report.AISettings.LedgerEntries > 0 || report.AI.Changed()
	if changed {
		c.logger.Info("completed data upgrade", values...)
	}
}

func (c *productionUpgradeCoordinator) emit(ctx context.Context, message string) {
	_ = ipc.Emit(ctx, c.events, ipc.TopicAppError, ipc.AppErrorEvent{Code: "data_upgrade", Message: message})
}

type channelOwnerStreamFactory struct {
	mu       sync.Mutex
	factory  ipc.StreamFactory
	sessions *session.Manager
}

type channelOwnerBinaryStream struct {
	ipc.BinaryStream
	sessions   *session.Manager
	channelID  string
	generation uint64
}

func (s *channelOwnerBinaryStream) Close() error {
	err := s.BinaryStream.Close()
	s.sessions.UnregisterChannelOwner(s.channelID, s.generation)
	return err
}

func (s *channelOwnerBinaryStream) CloseGracefully() error {
	stream, ok := s.BinaryStream.(interface{ CloseGracefully() error })
	if !ok {
		return s.Close()
	}
	return stream.CloseGracefully()
}

func (s *channelOwnerBinaryStream) TrySendBinary(ctx context.Context, data []byte) error {
	if stream, ok := s.BinaryStream.(interface {
		TrySendBinary(context.Context, []byte) error
	}); ok {
		return stream.TrySendBinary(ctx, data)
	}
	return s.BinaryStream.SendBinary(ctx, data)
}

type channelOwnerJSONStream struct {
	ipc.JSONStream
	sessions   *session.Manager
	channelID  string
	generation uint64
}

func (s *channelOwnerJSONStream) Close() error {
	err := s.JSONStream.Close()
	s.sessions.UnregisterChannelOwner(s.channelID, s.generation)
	return err
}

func (s *channelOwnerJSONStream) CloseGracefully() error {
	stream, ok := s.JSONStream.(interface{ CloseGracefully() error })
	if !ok {
		return s.Close()
	}
	return stream.CloseGracefully()
}

func (s *channelOwnerJSONStream) TrySendJSON(ctx context.Context, data json.RawMessage) error {
	if stream, ok := s.JSONStream.(interface {
		TrySendJSON(context.Context, json.RawMessage) error
	}); ok {
		return stream.TrySendJSON(ctx, data)
	}
	return s.JSONStream.SendJSON(ctx, data)
}

func (f *channelOwnerStreamFactory) OpenBinary(ctx context.Context, channel ipc.ChannelRef) (ipc.BinaryStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ownerID, _ := ipc.UserIDFromContext(ctx)
	if err := f.sessions.CheckChannelOwner(channel.ID, ownerID); err != nil {
		return nil, err
	}
	stream, err := f.factory.OpenBinary(ctx, channel)
	if err != nil {
		return nil, err
	}
	generation, err := f.sessions.RegisterChannelOwner(channel.ID, ownerID)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	return &channelOwnerBinaryStream{BinaryStream: stream, sessions: f.sessions, channelID: channel.ID, generation: generation}, nil
}

func (f *channelOwnerStreamFactory) OpenJSON(ctx context.Context, channel ipc.ChannelRef) (ipc.JSONStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ownerID, _ := ipc.UserIDFromContext(ctx)
	if err := f.sessions.CheckChannelOwner(channel.ID, ownerID); err != nil {
		return nil, err
	}
	stream, err := f.factory.OpenJSON(ctx, channel)
	if err != nil {
		return nil, err
	}
	generation, err := f.sessions.RegisterChannelOwner(channel.ID, ownerID)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	return &channelOwnerJSONStream{JSONStream: stream, sessions: f.sessions, channelID: channel.ID, generation: generation}, nil
}

func openProductionStore(ctx context.Context, config ProductionConfig) (*store.Store, error) {
	if config.DB.Backend != "" && config.DB.Backend != store.BackendSQLite {
		return store.OpenBackend(ctx, config.DB, store.OpenOptions{Logger: config.Config.Logger})
	}
	return store.OpenWithOptions(ctx, filepath.Join(config.DataDir, "data.db"), store.OpenOptions{Logger: config.Config.Logger})
}

// helperUploadLimitFromEnv reads the optional helper upload cap in KiB/s; unset or invalid means unlimited.
func helperUploadLimitFromEnv(logger *slog.Logger) int64 {
	raw := strings.TrimSpace(os.Getenv("NEXTERM_HELPER_UPLOAD_KIB_PER_SEC"))
	if raw == "" {
		return 0
	}
	kib, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || kib <= 0 || kib > maxHelperUploadKiBPerSec {
		logger.Warn("ignoring NEXTERM_HELPER_UPLOAD_KIB_PER_SEC; the helper upload stays unlimited", "value", raw)
		return 0
	}
	return kib << 10
}

// maxHelperUploadKiBPerSec keeps the byte conversion far from overflow.
const maxHelperUploadKiBPerSec = 1 << 30
