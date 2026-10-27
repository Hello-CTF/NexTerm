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
	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	core "github.com/Hello-CTF/NexTerm/internal/app"
	production "github.com/Hello-CTF/NexTerm/internal/app/production"
	"github.com/Hello-CTF/NexTerm/internal/applog"
	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/diagcapture"
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
		hub := purpose.NewListenHub()
		r.RegisterPurposeHandler(deviceproto.PurposeFile, purpose.NewFileServer(dial).Serve)
		r.RegisterPurposeHandler(deviceproto.PurposeForward, purpose.NewDialServer(dial).Serve)
		r.RegisterPurposeHandler(deviceproto.PurposeListen, purpose.NewListenServer(dial, hub).Serve)
		r.RegisterPurposeHandler(deviceproto.PurposeAccept, purpose.NewAcceptServer(dial, hub).Serve)
	}
}

func startupContextForAuth(ctx context.Context, auth, listen string) context.Context {
	if auth == core.AuthOff || auth == core.AuthLoopback && core.LoopbackListen(listen) {
		return owner.WithOpenAccess(ctx)
	}
	return ctx
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

func trustedProxyCIDRs() []string {
	var cidrs []string
	for _, item := range strings.Split(os.Getenv("NEXTERM_TRUSTED_PROXY_CIDRS"), ",") {
		if item = strings.TrimSpace(item); item != "" {
			cidrs = append(cidrs, item)
		}
	}
	return cidrs
}

func run(args []string) int {
	if len(args) > 0 && args[0] == supervisor.HelperCommand {
		return supervisor.RunHelperCLI(args[1:])
	}
	if len(args) > 0 && args[0] == "agent" {
		return fleetagent.RunCLIWithServicePrefix(args[1:], []string{"agent"}, os.Getenv, os.Stdout, os.Stderr)
	}
	if len(args) > 0 && args[0] == "init-admin" {
		return runInitAdmin(args[1:], os.Getenv, os.Stdout, os.Stderr)
	}
	invocation, err := core.ParseCLI(args, core.CommandServe, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
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
		fmt.Fprintln(os.Stderr, "nexterm-server: unsupported command", applog.RedactText(string(invocation.Command)))
		return 2
	}

	paths, err := platform.ServerPaths(invocation.DataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
		return 1
	}
	if err := paths.Prepare(); err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
		return 1
	}
	logger, logErr := applog.NewLogger(paths, os.Stderr)
	if logErr != nil {
		logger.Warn("file logging is unavailable; using console only", "error", logErr)
	}
	defer func() {
		if err := logger.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server: close log:", applog.RedactText(err.Error()))
		}
	}()
	previousLogger := slog.Default()
	slog.SetDefault(logger.Logger)
	defer slog.SetDefault(previousLogger)
	if capture := diagcapture.StartIfRequested(context.Background(), paths, logger, diagcapture.Info{Mode: "server", Args: args}, invocation.DebugCapture); capture != nil {
		defer capture.Stop()
	}
	pprofServer, err := pprof.Start(logger.Logger, invocation.PprofAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
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
		fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
		return 2
	}
	if masterKey == "" {
		masterKey, err = ensureVaultMasterKey(paths.DataDir, logger.Logger)
		if err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
			return 2
		}
	}
	dbBackend, err := store.ParseBackend(invocation.DB)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
		return 2
	}
	dbPassword, err := server.ResolveDBPassword(invocation.DBPasswordFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
		return 2
	}

	ctx, stop := platform.NotifyContext(context.Background())
	defer stop()
	webSocket, err := server.ParseWebSocketEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
		return 2
	}
	queueSize, err := server.ParseEventQueueEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
		return 2
	}
	bufferSize, err := server.ParseEventBufferEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
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
	ctx = startupContextForAuth(ctx, invocation.Auth, invocation.Listen)
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
		// Applies only without accounts: one helper cannot be shared between accounts.
		LocalHelper: true,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
		return 1
	}
	var accounts *account.Accounts
	if application.Services.Store != nil {
		accounts = account.New(
			application.Services.Store.GORM(),
			account.WithTOTPKeyFile(filepath.Join(paths.DataDir, "totp.key")),
			account.WithActiveSuperadminListener(application.RetryDeferredUpgrade),
		)
	}
	if invocation.Auth == core.AuthOff && accounts != nil {
		if err := server.ValidateAuthOffBounds(ctx, accounts); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
			return 1
		}
	}
	// In platform-managed mode accounts are created automatically with platform
	// login, so there is no one-time init code to copy down.
	if accounts != nil && invocation.Auth != core.AuthOff && invocation.Auth != core.AuthPlatform {
		if err := printAccountInitCode(ctx, accounts, os.Stderr, logger.Logger); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
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
			fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
			return 1
		}
		previewService, err = sharing.New(sharing.Config{
			DB:       application.Services.Store.GORM(),
			Accounts: accounts,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
			return 1
		}
		// Device sessions join the session system through the fleet authorization
		// surface: each (re)connect validates owner/revocation/terminal switch and
		// writes a terminal_open audit; bridge and state digest come straight from
		// the registry.
		application.Services.SetDeviceConnector(&production.DeviceConnectorConfig{
			Dial:     fleetService.RequestDeviceBridgePurpose,
			Platform: fleetService.DevicePlatform,
			Digest: func(_ context.Context, deviceID string) (string, error) {
				digest, ok := fleetService.DeviceStateDigest(deviceID)
				if !ok || digest == "" {
					return "", ipc.NewError(ipc.CodeDisconnected, "设备未返回终端状态，可能已经离线")
				}
				return digest, nil
			},
			Authorize: fleetService.AuthorizeDeviceTerminal,
			// The cross-host session listing reaches the account's devices
			// through the bridge, so a phone sees them with the desktop closed.
			Devices: func(ctx context.Context, userID string) ([]production.AggregateDevice, error) {
				devices, err := fleetService.TerminalDevices(ctx, userID)
				if err != nil {
					return nil, err
				}
				result := make([]production.AggregateDevice, 0, len(devices))
				for _, device := range devices {
					result = append(result, production.AggregateDevice{
						ID: device.ID, Name: device.Name, Kind: device.Kind, Platform: device.Platform,
						Online: device.Digest != "", Digest: device.Digest, TerminalEnabled: device.TerminalEnabled,
					})
				}
				return result, nil
			},
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
		fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
		return 1
	}
	transport, err := server.New(server.Config{
		Options: server.Options{
			Listen:            invocation.Listen,
			DataDir:           paths.DataDir,
			WebRoot:           invocation.WebRoot,
			SyncOnly:          invocation.SyncOnly,
			Auth:              invocation.Auth,
			AllowedOrigins:    allowedOrigins(),
			TrustedProxyCIDRs: trustedProxyCIDRs(),
		},
		Blobs:                 blobs,
		Dispatcher:            application.Dispatcher,
		PeerDispatcher:        peerDispatcher,
		Environment:           application.Environment(""),
		SyncObjects:           application.SyncObjectHandler(),
		GatewayAuthKey:        application.Services.Sync.GatewayAuthKey(),
		Accounts:              accounts,
		Events:                broker,
		Channels:              hubAdapter,
		ChannelStats:          hubAdapter.Stats,
		Vault:                 application.Services.Vault,
		Retention:             serverRetentionConfig(application.Services.Retention),
		Fleet:                 fleetService,
		Previews:              previewService,
		Spectator:             application.Services.Sessions,
		DB:                    healthDB,
		AccountConnections:    application.Services.Database,
		AccountCleanup:        application.CleanupAccount,
		AccountCleanupPrepare: application.PrepareAccountCleanup,
		Settings:              settings,
		AuditFunc:             audit,
		AssetOwnerFunc:        assetOwner,
		Logger:                logger.Logger,
		WebSocket:             webSocket,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
		return 1
	}
	if fleetService != nil {
		rollupRunner := fleetserver.NewRollupRunner(fleetService, 0, logger.Logger)
		if err := rollupRunner.Start(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", applog.RedactText(err.Error()))
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
			logger.Warn("initialization code was already generated; if it is lost, clear auth.init_code in the setting table and restart to regenerate it")
			return nil
		}
		return err
	}
	fmt.Fprintf(stderr, "\nnexterm-server: 账号系统尚未初始化\nnexterm-server: 一次性初始化码: %s\nnexterm-server: 请在浏览器中打开本服务完成超级管理员账号初始化(初始化码仅可使用一次)\n\n", code)
	return nil
}

const (
	// initAdminExitUnavailable reports that no bootstrap was needed: accounts
	// already exist, or the auth mode manages accounts without an initial
	// admin. Nothing is printed to stdout.
	initAdminExitUnavailable = 3
	// initAdminExitCodeIssued reports that a one-time init code was already
	// generated elsewhere (for example the running server printed it to its
	// own log), so the local bootstrap must not create an account or touch
	// that code.
	initAdminExitCodeIssued = 4
)

// initAdminPasswordMarker prefixes the one-time initial-password line the
// init-admin command prints to stdout after the admin was actually created.
const initAdminPasswordMarker = "初始密码: "

// initAdminSettingKey mirrors internal/account's unexported initCodeSettingKey.
// The key is stable and documented by the server's own operational guidance
// ("clear auth.init_code in the setting table and restart to regenerate it").
const initAdminSettingKey = "auth.init_code"

func initAdminUsage() string {
	return `Usage:
  nexterm-server init-admin [flags]

Create the first superadmin through the server's account initialization
contract and print the initial password to this console exactly once,
only after the account was actually created. With no
NEXTERM_ADMIN_PASSWORD a cryptographically random password is generated
and must be changed at first login; a supplied initial password is kept
as-is (no forced rotation) so install systems such as LazyCat can inject
it. Never prints on upgrade, existing-account or re-run paths, and never
persists the password to env files, disk or logs.

Exit codes:
  0  admin created; the initial password was printed to stdout
  3  nothing to do: accounts exist, or the auth mode manages accounts itself
  4  an init code was already issued and this run cannot prove it usable;
     install.sh fails the first install so the operator can recover
  1  creation failed; nothing was printed
  2  usage error

Flags:
  --data-dir PATH     Data directory (env NEXTERM_DATA_DIR)
  --db BACKEND        Server storage: sqlite, postgres, mysql, or mariadb
                      (env NEXTERM_DB, default sqlite)
  --db-dsn DSN        Database connection string (env NEXTERM_DB_DSN)
  --db-password-file PATH  Read the database password from a 0600 file
                      (env NEXTERM_DB_PASSWORD_FILE)
  --username NAME     Superadmin username (env NEXTERM_ADMIN_USERNAME, default admin)
  --auth MODE         Only used to refuse off/platform modes, which manage
                      accounts themselves (env NEXTERM_AUTH)
  -h, --help          Print this help

The initial password is read from the NEXTERM_ADMIN_PASSWORD environment
variable only: argv is visible to other processes via ps, so there is
deliberately no password flag.
`
}

type initAdminOptions struct {
	dataDir        string
	db             string
	dbDSN          string
	dbPasswordFile string
	username       string
	auth           string
	help           bool
	version        bool
}

func parseInitAdminArgs(args []string, getenv func(string) string) (initAdminOptions, error) {
	options := initAdminOptions{
		dataDir:        getenv("NEXTERM_DATA_DIR"),
		db:             getenv("NEXTERM_DB"),
		dbDSN:          getenv("NEXTERM_DB_DSN"),
		dbPasswordFile: getenv("NEXTERM_DB_PASSWORD_FILE"),
		username:       getenv("NEXTERM_ADMIN_USERNAME"),
		auth:           getenv("NEXTERM_AUTH"),
	}
	if options.username == "" {
		options.username = "admin"
	}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "-h" || argument == "--help" {
			options.help = true
			continue
		}
		if argument == "--version" {
			options.version = true
			continue
		}
		if !strings.HasPrefix(argument, "--") {
			return initAdminOptions{}, fmt.Errorf("unknown argument %q", argument)
		}
		name, value, hasValue := strings.Cut(argument, "=")
		if !hasValue {
			index++
			if index >= len(args) {
				return initAdminOptions{}, fmt.Errorf("option %s requires a value", name)
			}
			value = args[index]
		}
		switch name {
		case "--data-dir":
			options.dataDir = value
		case "--db":
			options.db = value
		case "--db-dsn":
			options.dbDSN = value
		case "--db-password-file":
			options.dbPasswordFile = value
		case "--username":
			options.username = value
		case "--auth":
			options.auth = value
		default:
			return initAdminOptions{}, fmt.Errorf("unknown option %s", name)
		}
	}
	return options, nil
}

// runInitAdmin implements the init-admin command: the installer-side half of
// the first-install super-admin bootstrap. The account itself is only ever
// created through the account package's InitSuperadmin contract (one-time init
// code included); this command adds no second creation path and writes no
// account rows directly.
func runInitAdmin(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	options, err := parseInitAdminArgs(args, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "nexterm-server init-admin:", applog.RedactText(err.Error()))
		return 2
	}
	if options.help {
		fmt.Fprint(stdout, initAdminUsage())
		return 0
	}
	if options.version {
		fmt.Fprintln(stdout, version.Version)
		return 0
	}
	switch options.auth {
	case core.AuthOff, core.AuthPlatform:
		fmt.Fprintf(stderr, "nexterm-server init-admin: NEXTERM_AUTH=%s 模式下账号由部署环境自行管理，跳过超级管理员引导\n", options.auth)
		return initAdminExitUnavailable
	}
	ctx := context.Background()
	paths, err := platform.ServerPaths(options.dataDir)
	if err != nil {
		fmt.Fprintln(stderr, "nexterm-server init-admin:", applog.RedactText(err.Error()))
		return 1
	}
	if err := paths.Prepare(); err != nil {
		fmt.Fprintln(stderr, "nexterm-server init-admin:", applog.RedactText(err.Error()))
		return 1
	}
	database, err := openInitAdminStore(ctx, options, paths.DataDir)
	if err != nil {
		fmt.Fprintln(stderr, "nexterm-server init-admin:", applog.RedactText(err.Error()))
		return 1
	}
	defer func() { _ = database.Close() }()
	accounts := account.New(database.GORM())
	count, err := accounts.CountUsers(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "nexterm-server init-admin:", applog.RedactText(err.Error()))
		return 1
	}
	if count > 0 {
		fmt.Fprintln(stderr, "nexterm-server init-admin: 账号系统已初始化，跳过超级管理员引导")
		return initAdminExitUnavailable
	}
	policy := account.InitialPasswordRandom
	supplied := getenv("NEXTERM_ADMIN_PASSWORD")
	if supplied != "" {
		policy = account.InitialPasswordSupplied
	}
	code, err := accounts.GenerateInitCode(ctx)
	if err != nil {
		if ipc.NormalizeError(err).Code == ipc.CodeForbidden {
			fmt.Fprintln(stderr, "nexterm-server init-admin: 一次性初始化码已生成，请改用服务器日志中的初始化码在浏览器中完成初始化")
			return initAdminExitCodeIssued
		}
		fmt.Fprintln(stderr, "nexterm-server init-admin:", applog.RedactText(err.Error()))
		return 1
	}
	user, generated, err := accounts.InitSuperadminWithPolicy(ctx, code, options.username, supplied, nil, policy)
	if err != nil {
		// The init code was generated by this very process and never disclosed.
		// Release it through the account contract so a retry (or the server's
		// own first-start flow) can issue a fresh code instead of dead-ending
		// on "already generated"; a code issued by anyone else fails the
		// contract's hash match and is left untouched.
		if releaseErr := accounts.ReleaseInitCode(ctx, code); releaseErr != nil {
			fmt.Fprintln(stderr, "nexterm-server init-admin: 清理未使用的初始化码失败（若重跑提示初始化码已生成，请清除 setting 表 "+initAdminSettingKey+" 后重试）:", applog.RedactText(releaseErr.Error()))
		}
		fmt.Fprintln(stderr, "nexterm-server init-admin:", applog.RedactText(err.Error()))
		return 1
	}
	password := generated
	if policy == account.InitialPasswordSupplied {
		password = supplied
	}
	fmt.Fprintf(stdout, "\nnexterm-server: 超级管理员账号已创建\nnexterm-server: 用户名: %s\nnexterm-server: %s%s（仅显示一次，请立即妥善保存）\n", user.Username, initAdminPasswordMarker, password)
	if policy == account.InitialPasswordRandom {
		fmt.Fprintln(stdout, "nexterm-server: 该密码为随机首装密码，首次登录时必须修改")
	} else {
		fmt.Fprintln(stdout, "nexterm-server: 该密码由安装方指定，首次登录不强制修改")
	}
	fmt.Fprintln(stdout)
	return 0
}

// openInitAdminStore mirrors how the serve command opens the account store:
// SQLite lives at <data-dir>/data.db, remote backends go through the shared
// BackendConfig path.
func openInitAdminStore(ctx context.Context, options initAdminOptions, dataDir string) (*store.Store, error) {
	backend, err := store.ParseBackend(options.db)
	if err != nil {
		return nil, err
	}
	if backend != store.BackendSQLite {
		dbPassword, err := server.ResolveDBPassword(options.dbPasswordFile)
		if err != nil {
			return nil, err
		}
		return store.OpenBackend(ctx, store.BackendConfig{Backend: backend, DSN: options.dbDSN, Password: dbPassword}, store.OpenOptions{})
	}
	return store.OpenWithOptions(ctx, filepath.Join(dataDir, "data.db"), store.OpenOptions{})
}
