package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	core "github.com/Hello-CTF/NexTerm/internal/app"
	production "github.com/Hello-CTF/NexTerm/internal/app/production"
	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	fleetagent "github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent/purpose"
	fleetserver "github.com/Hello-CTF/NexTerm/internal/fleet/server"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/platform"
	"github.com/Hello-CTF/NexTerm/internal/pprof"
	"github.com/Hello-CTF/NexTerm/internal/server"
	"github.com/Hello-CTF/NexTerm/internal/sharing"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/version"
)

func init() {
	fleetagent.PurposeHandlerWirer = func(r *fleetagent.Runtime) {
		dial := purpose.BridgeDialerFunc(r.DialPurposeBridge)
		r.RegisterPurposeHandler(deviceproto.PurposeFile, purpose.NewFileServer(dial).Serve)
		r.RegisterPurposeHandler(deviceproto.PurposeForward, purpose.NewDialServer(dial).Serve)
	}
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// allowedOrigins reads the public domain names of this instance and adds them
// to the same-origin allowlist (comma-separated).
//
// Behind a reverse proxy the gateway rewrites Host to the upstream address
// (observed: nexterm-server:8080), while browser requests carry an Origin with
// the public domain; the upstream same-origin check only compares r.Host, so
// the whole site is judged cross-origin and even the entry JS is blocked
// (blank page). Here the domains are handed to server.Options.AllowedOrigins,
// which feeds both transportGuard and the WebSocket OriginPatterns, so one
// setting takes effect on both paths.
//
// When unset it returns nil so the Server keeps the upstream localhost default
// (fallback happens only when `allowedOrigins == nil`; returning an empty
// slice would override the default).
func allowedOrigins() []string {
	var origins []string
	for _, item := range strings.Split(os.Getenv("NEXTERM_ALLOWED_ORIGINS"), ",") {
		if item = strings.TrimSpace(item); item != "" {
			origins = append(origins, item)
		}
	}
	return origins
}

func run(args []string) int {
	if len(args) > 0 && args[0] == supervisor.HelperCommand {
		return supervisor.RunHelperCLI(args[1:])
	}
	if len(args) > 0 && args[0] == "agent" {
		return fleetagent.RunCLIWithServicePrefix(args[1:], []string{"agent"}, os.Getenv, os.Stdout, os.Stderr)
	}
	invocation, err := core.ParseCLI(args, core.CommandServe, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 2
	}
	if invocation.Help {
		fmt.Fprint(os.Stdout, core.Usage("nexterm-server", core.CommandServe))
		return 0
	}
	if invocation.Version {
		fmt.Fprintln(os.Stdout, version.Version)
		return 0
	}

	switch invocation.Command {
	case core.CommandDesktop:
		fmt.Fprintln(os.Stderr, "nexterm-server: use the nexterm-desktop binary for the desktop command")
		return 2
	case core.CommandServe:
	default:
		fmt.Fprintln(os.Stderr, "nexterm-server: unsupported command", invocation.Command)
		return 2
	}

	paths, err := platform.ServerPaths(invocation.DataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 1
	}
	if err := paths.Prepare(); err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 1
	}
	logger, logErr := platform.NewLogger(paths, os.Stderr)
	if logErr != nil {
		logger.Warn("file logging is unavailable; using console only", "error", logErr)
	}
	defer func() {
		if err := logger.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server: close log:", err)
		}
	}()
	pprofServer, err := pprof.Start(logger.Logger, invocation.PprofAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 1
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = pprofServer.Close(shutdownCtx)
	}()
	if invocation.Auth == core.AuthOff {
		logger.Warn("access control is disabled via --auth=off: anyone who can reach the listen address can operate the server", "listen", invocation.Listen)
	}
	masterKey, err := server.ResolveMasterKey(invocation.MasterKeyFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 2
	}
	if masterKey == "" {
		masterKey, err = ensureVaultMasterKey(paths.DataDir, logger.Logger)
		if err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", err)
			return 2
		}
	}
	dbBackend, err := store.ParseBackend(invocation.DB)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 2
	}
	dbPassword, err := server.ResolveDBPassword(invocation.DBPasswordFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 2
	}

	ctx, stop := platform.NotifyContext(context.Background())
	defer stop()
	webSocket, err := server.ParseWebSocketEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 2
	}
	queueSize, err := server.ParseEventQueueEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 2
	}
	bufferSize, err := server.ParseEventBufferEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 2
	}
	broker := server.NewEventBroker()
	if queueSize > 0 {
		broker.QueueSize = queueSize
	}
	if bufferSize > 0 {
		broker.BufferSize = bufferSize
	}
	if limit, err := platform.RaiseNoFileLimit(platform.DefaultNoFileLimit); err != nil {
		logger.Warn("raise open file limit failed", "error", err)
	} else if limit > 0 {
		logger.Info("open file limit ensured", "limit", limit)
	}
	// The same BlobStore instance serves both the /files/blob HTTP surface and
	// the server-side IPC staging resolution, so owner metadata and the staging
	// root layout have a single source of truth.
	var blobs *server.BlobStore
	if paths.DataDir != "" {
		blobs = server.NewBlobStore(paths.DataDir, logger.Logger)
	}
	application, err := production.NewProduction(ctx, production.ProductionConfig{
		Config: core.Config{
			Logger: logger.Logger,
			Events: broker,
		},
		DataDir:         paths.DataDir,
		Desktop:         false,
		ForwardPlatform: os.Getenv("NEXTERM_PLATFORM"),
		DB: store.BackendConfig{
			Backend:      dbBackend,
			DSN:          invocation.DBDSN,
			Password:     dbPassword,
			MaxOpenConns: invocation.DBMaxOpenConns,
		},
		StagedBlobs: blobs,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 1
	}
	var accounts *account.Accounts
	if application.Services.Store != nil {
		accounts = account.New(application.Services.Store.GORM(), account.WithTOTPKeyFile(filepath.Join(paths.DataDir, "totp.key")))
	}
	if invocation.Auth == core.AuthOff && accounts != nil {
		if err := server.ValidateAuthOffBounds(ctx, accounts); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", err)
			return 1
		}
	}
	// In platform-managed mode accounts are created automatically with platform
	// login, so there is no one-time init code to copy down.
	if accounts != nil && invocation.Auth != core.AuthOff && invocation.Auth != core.AuthPlatform {
		if err := printAccountInitCode(ctx, accounts, os.Stderr, logger.Logger); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", err)
			return 1
		}
	}
	var fleetService *fleetserver.Service
	var previewService *sharing.Service
	if application.Services.Store != nil && accounts != nil && !invocation.SyncOnly {
		fleetService, err = fleetserver.New(fleetserver.Config{
			DB:       application.Services.Store.GORM(),
			Accounts: accounts,
			AuthOff:  invocation.Auth == core.AuthOff,
			Events:   broker,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", err)
			return 1
		}
		previewService, err = sharing.New(sharing.Config{
			DB:       application.Services.Store.GORM(),
			Accounts: accounts,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", err)
			return 1
		}
		// Device sessions join the session system through the fleet authorization
		// surface: each (re)connect validates owner/revocation/terminal switch and
		// writes a terminal_open audit; bridge and state digest come straight from
		// the registry.
		application.Services.SetDeviceConnector(&production.DeviceConnectorConfig{
			Dial: fleetService.RequestDeviceBridgePurpose,
			Digest: func(_ context.Context, deviceID string) (string, error) {
				digest, ok := fleetService.DeviceStateDigest(deviceID)
				if !ok || digest == "" {
					return "", ipc.NewError(ipc.CodeDisconnected, "设备未返回终端状态，可能已经离线")
				}
				return digest, nil
			},
			Authorize: fleetService.AuthorizeDeviceTerminal,
		})
	}
	hubAdapter := server.NewHubAdapter(application.Services.Sessions.Hub())
	var settings server.SettingStore
	var audit server.AuditFunc
	var assetOwner server.AssetOwnerFunc
	var healthDB server.DBHealthSource
	if application.Services.Store != nil {
		settings = application.Services.Store
		healthDB = application.Services.Store
		assetOwner = application.Services.Store.AssetOwnerID
		audit = func(ctx context.Context, source, kind string, payload map[string]any) error {
			return application.Services.Store.AuditInsert(ctx, store.AuditInput{Source: source, Kind: kind, Payload: payload})
		}
	}
	peerDispatcher, err := application.Services.Sync.PeerDispatcher()
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 1
	}
	transport, err := server.New(server.Config{
		Options: server.Options{
			Listen:         invocation.Listen,
			DataDir:        paths.DataDir,
			WebRoot:        invocation.WebRoot,
			SyncOnly:       invocation.SyncOnly,
			Auth:           invocation.Auth,
			AllowedOrigins: allowedOrigins(),
		},
		Blobs:          blobs,
		Dispatcher:     application.Dispatcher,
		PeerDispatcher: peerDispatcher,
		Environment:    application.Environment(""),
		SyncObjects:    application.SyncObjectHandler(),
		GatewayAuthKey: application.Services.Sync.GatewayAuthKey(),
		Accounts:       accounts,
		Events:         broker,
		Channels:       hubAdapter,
		ChannelStats:   hubAdapter.Stats,
		Vault:          application.Services.Vault,
		Retention:      serverRetentionConfig(application.Services.Retention),
		Fleet:          fleetService,
		Previews:       previewService,
		Spectator:      application.Services.Sessions,
		DB:             healthDB,
		Settings:       settings,
		AuditFunc:      audit,
		AssetOwnerFunc: assetOwner,
		Logger:         logger.Logger,
		WebSocket:      webSocket,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 1
	}
	if fleetService != nil {
		rollupRunner := fleetserver.NewRollupRunner(fleetService, 0, logger.Logger)
		if err := rollupRunner.Start(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", err)
			return 1
		}
		defer func() {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer shutdownCancel()
			_ = rollupRunner.Shutdown(shutdownCtx)
		}()
	}
	if invocation.RequireVault {
		if err := server.BootstrapVaultRequired(ctx, application.Services.Vault, masterKey); err != nil {
			logger.Error("vault is required but unavailable", "error", err)
			return 1
		}
	} else if err := server.BootstrapVault(ctx, application.Services.Vault, masterKey); err != nil {
		logger.Error("vault bootstrap failed", "error", err)
	}
	if err := application.Serve(ctx, core.ServeConfig{
		Listen:         invocation.Listen,
		WebRoot:        invocation.WebRoot,
		SyncOnly:       invocation.SyncOnly,
		Auth:           invocation.Auth,
		Transport:      transport.Handler(),
		CloseTransport: transport.CloseContext,
		Stderr:         os.Stderr,
	}); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("server stopped", "error", err)
		return 1
	}
	return 0
}

// ensureVaultMasterKey guarantees a master key in the data dir when none is
// configured explicitly: reuse the existing master.key if present (idempotent
// across restarts; overwriting with a new key would make all old ciphertext
// undecryptable), otherwise generate 32 random bytes as hex and write with
// 0600.
func ensureVaultMasterKey(dataDir string, logger *slog.Logger) (string, error) {
	keyPath := filepath.Join(dataDir, "master.key")
	if data, err := os.ReadFile(keyPath); err == nil {
		if key := strings.TrimSpace(string(data)); key != "" {
			return key, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read vault master key: %w", err)
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate vault master key: %w", err)
	}
	key := hex.EncodeToString(random)
	if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
		return "", fmt.Errorf("write vault master key: %w", err)
	}
	logger.Info("generated vault master key", "file", keyPath)
	return key, nil
}

func serverRetentionConfig(runner *core.RetentionRunner) *server.RetentionConfig {
	if runner == nil {
		return nil
	}
	return &server.RetentionConfig{
		Enabled: true,
		Status: server.RetentionStatusFunc(func(ctx context.Context) (server.RetentionStatus, error) {
			health, err := runner.Status(ctx)
			status := server.RetentionStatus{LastError: health.LastError}
			if health.LastAttemptAt != nil {
				status.LastAttemptAt = *health.LastAttemptAt
			}
			if health.LastSuccessAt != nil {
				status.LastSuccessAt = *health.LastSuccessAt
			}
			return status, err
		}),
	}
}

// printAccountInitCode prints the one-time init code to the console when the
// account system is not initialized.
// The code is printed only on first generation; if lost after a restart,
// clear auth.init_code in the setting table and restart to regenerate.
func printAccountInitCode(ctx context.Context, accounts *account.Accounts, stderr io.Writer, logger *slog.Logger) error {
	count, err := accounts.CountUsers(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	code, err := accounts.GenerateInitCode(ctx)
	if err != nil {
		if ipc.NormalizeError(err).Code == ipc.CodeForbidden {
			logger.Warn("初始化码此前已生成; 若遗失, 请清除 setting 表 auth.init_code 后重启以重新生成")
			return nil
		}
		return err
	}
	fmt.Fprintf(stderr, "\nnexterm-server: 账号系统尚未初始化\nnexterm-server: 一次性初始化码: %s\nnexterm-server: 请在浏览器中打开本服务完成超管账号初始化(初始化码仅可使用一次)\n\n", code)
	return nil
}
