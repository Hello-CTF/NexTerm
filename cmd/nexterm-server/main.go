package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	production "github.com/ProbiusOfficial/NexTerm/internal/app/production"
	"github.com/ProbiusOfficial/NexTerm/internal/platform"
	"github.com/ProbiusOfficial/NexTerm/internal/server"
	"github.com/ProbiusOfficial/NexTerm/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
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
	case core.CommandToken, core.CommandRotateToken:
		if err := runServerTokenCommand(invocation); err != nil {
			fmt.Fprintln(os.Stderr, "nexterm-server:", err)
			return 1
		}
		return 0
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
	broker := server.NewEventBroker()
	if queueSize > 0 {
		broker.QueueSize = queueSize
	}
	if limit, err := platform.RaiseNoFileLimit(platform.DefaultNoFileLimit); err != nil {
		logger.Warn("raise open file limit failed", "error", err)
	} else if limit > 0 {
		logger.Info("open file limit ensured", "limit", limit)
	}
	application, err := production.NewProduction(ctx, production.ProductionConfig{
		Config: core.Config{
			Logger: logger.Logger,
			Events: broker,
		},
		DataDir:         paths.DataDir,
		Desktop:         false,
		ForwardPlatform: os.Getenv("NEXTERM_PLATFORM"),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 1
	}
	syncDispatcher, err := application.Services.Sync.PeerDispatcher()
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 1
	}
	hubAdapter := server.NewHubAdapter(application.Services.Sessions.Hub())
	transport, err := server.New(server.Config{
		Options: server.Options{
			Listen:   invocation.Listen,
			DataDir:  paths.DataDir,
			WebRoot:  invocation.WebRoot,
			SyncOnly: invocation.SyncOnly,
		},
		Dispatcher:   application.Dispatcher,
		Environment:  application.Environment(""),
		SyncRPC:      application.SyncRPCHandler(),
		Events:       broker,
		Channels:     hubAdapter,
		ChannelStats: hubAdapter.Stats,
		Vault:        application.Services.Vault,
		Retention:    serverRetentionConfig(application.Services.Retention),
		Logger:       logger.Logger,
		WebSocket:    webSocket,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "nexterm-server:", err)
		return 1
	}
	if invocation.MasterKey == "" {
		logger.Warn("no vault master key provided; credential synchronization may be unavailable")
	} else if err := server.BootstrapVault(ctx, application.Services.Vault, invocation.MasterKey); err != nil {
		logger.Error("vault bootstrap failed", "error", err)
	}
	if err := application.Serve(ctx, core.ServeConfig{
		Listen:         invocation.Listen,
		WebRoot:        invocation.WebRoot,
		SyncOnly:       invocation.SyncOnly,
		SyncRPC:        application.SyncRPCHandler(),
		SyncDispatcher: syncDispatcher,
		Transport:      transport.Handler(),
		CloseTransport: transport.CloseContext,
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

func runServerTokenCommand(invocation core.Invocation) error {
	paths, err := platform.ServerPaths(invocation.DataDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	application, err := production.NewProduction(ctx, production.ProductionConfig{DataDir: paths.DataDir, Desktop: false})
	if err != nil {
		return err
	}
	if err := application.Start(ctx); err != nil {
		return err
	}
	defer func() { _ = application.Shutdown(context.Background()) }()
	return core.RunTokenCommand(ctx, invocation.Command, application.TokenStore(), os.Stdout)
}
