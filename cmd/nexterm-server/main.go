package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	production "github.com/ProbiusOfficial/NexTerm/internal/app/production"
	fleetagent "github.com/ProbiusOfficial/NexTerm/internal/fleet/agent"
	fleetserver "github.com/ProbiusOfficial/NexTerm/internal/fleet/server"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/platform"
	"github.com/ProbiusOfficial/NexTerm/internal/server"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/supervisor"
	"github.com/ProbiusOfficial/NexTerm/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// allowedOrigins 读入本实例对外的域名，并进同源白名单（逗号分隔）。
//
// 反代部署下网关会把 Host 改写成上游地址（实测 nexterm-server:8080），而浏览器的请求
// 带的是对外域名的 Origin；上游的同源判定只比对 r.Host，于是整站被判跨源、连入口 JS
// 都拿不到（网页白屏）。这里把域名交给 server.Options.AllowedOrigins —— 该切片同时
// 喂给 transportGuard 与 WebSocket 的 OriginPatterns，一处配置两条路径都生效。
//
// 未配置时返回 nil，让 Server 沿用上游的 localhost 默认值（`allowedOrigins == nil`
// 才回落；返回空切片会把默认值顶掉）。
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
		return fleetagent.RunCLI(args[1:], os.Getenv, os.Stdout, os.Stderr)
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
	if os.Getenv("NEXTERM_MASTER_KEY") != "" {
		logger.Warn("NEXTERM_MASTER_KEY is deprecated; store the master key in a 0600 file and pass --master-key-file instead")
	}
	if invocation.Auth == core.AuthOff {
		logger.Warn("access control is disabled via --auth=off: anyone who can reach the listen address can operate the server", "listen", invocation.Listen)
	}
	masterKey, err := server.ResolveMasterKey(invocation.MasterKey, invocation.MasterKeyFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 2
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
	// 同一个 BlobStore 实例同时供 /files/blob HTTP 面与服务端 IPC 暂存解析,
	// 保证属主元数据与暂存根布局只有一份事实来源。
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
		accounts = account.New(application.Services.Store.DB(), account.WithTOTPKeyFile(filepath.Join(paths.DataDir, "totp.key")))
		if err := accounts.MigrateTOTPStorageKey(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", err)
			return 1
		}
	}
	if invocation.Auth == core.AuthOff && accounts != nil {
		if err := server.ValidateAuthOffBounds(ctx, accounts); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", err)
			return 1
		}
	}
	if accounts != nil && invocation.Auth != core.AuthOff {
		if err := printAccountInitCode(ctx, accounts, os.Stderr, logger.Logger); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", err)
			return 1
		}
	}
	var fleetService *fleetserver.Service
	if application.Services.Store != nil && accounts != nil && !invocation.SyncOnly {
		fleetService, err = fleetserver.New(fleetserver.Config{
			DB:       application.Services.Store.DB(),
			Accounts: accounts,
			AuthOff:  invocation.Auth == core.AuthOff,
			Events:   broker,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", err)
			return 1
		}
	}
	hubAdapter := server.NewHubAdapter(application.Services.Sessions.Hub())
	var settings server.SettingStore
	var audit server.AuditFunc
	var healthDB server.DBHealthSource
	if application.Services.Store != nil {
		settings = application.Services.Store
		healthDB = application.Services.Store
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
			PublicBaseURL:  invocation.PublicBaseURL,
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
		DB:             healthDB,
		Settings:       settings,
		AuditFunc:      audit,
		Logger:         logger.Logger,
		WebSocket:      webSocket,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 1
	}
	if images := transport.Images(); images != nil && !invocation.SyncOnly {
		go images.RunSweeper(ctx)
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
	switch {
	case invocation.RequireVault:
		if err := server.BootstrapVaultRequired(ctx, application.Services.Vault, masterKey); err != nil {
			logger.Error("vault is required but unavailable", "error", err)
			return 1
		}
	case masterKey == "":
		logger.Warn("no vault master key provided; credential synchronization may be unavailable")
	default:
		if err := server.BootstrapVault(ctx, application.Services.Vault, masterKey); err != nil {
			logger.Error("vault bootstrap failed", "error", err)
		}
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

// printAccountInitCode 在账号系统未初始化时于控制台输出一次性初始化码。
// 码只在首次生成时打印; 重启后若码遗失, 需清除 setting 表 auth.init_code 后重启重新生成。
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
