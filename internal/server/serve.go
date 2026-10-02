package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	core "github.com/ProbiusOfficial/NexTerm/internal/app"
)

type Lifecycle interface {
	Start(context.Context) error
	Shutdown(context.Context) error
}

type ServeConfig struct {
	Server          Config
	Lifecycle       Lifecycle
	Tokens          TokenStore
	Stderr          io.Writer
	Listener        net.Listener
	ShutdownTimeout time.Duration
}

func Serve(ctx context.Context, config ServeConfig) (returnErr error) {
	if config.Server.Options.Listen == "" {
		config.Server.Options.Listen = DefaultListen
	}
	if err := core.ValidateListenAddress(config.Server.Options.Listen); err != nil {
		return err
	}
	if config.Server.Logger == nil {
		config.Server.Logger = loggerDefault()
	}
	if config.Stderr == nil {
		config.Stderr = os.Stderr
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 10 * time.Second
	}
	if config.Server.Tokens == nil && config.Tokens != nil {
		if verifier, ok := config.Tokens.(TokenVerifier); ok {
			config.Server.Tokens = verifier
		}
	}
	if config.Tokens == nil {
		config.Tokens, _ = config.Server.Tokens.(TokenStore)
	}
	if !config.Server.Options.SyncOnly && config.Server.Blobs == nil && config.Server.Options.DataDir != "" {
		config.Server.Blobs = NewBlobStore(config.Server.Options.DataDir, config.Server.Logger)
	}

	warnIfExposed(config.Server.Logger, config.Stderr, config.Server.Options.Listen, config.Server.Options.SyncOnly)
	if config.Server.Options.MasterKey == "" {
		config.Server.Logger.Warn("no vault master key provided; credential synchronization may be unavailable")
	} else if err := BootstrapVault(ctx, config.Server.Vault, config.Server.Options.MasterKey); err != nil {
		config.Server.Logger.Error("vault bootstrap failed", "error", err)
	}
	if config.Tokens != nil {
		if _, err := config.Tokens.SyncToken(ctx); err != nil {
			config.Server.Logger.Warn("sync token initialization failed", "error", err)
		}
	}

	server, err := New(config.Server)
	if err != nil {
		return err
	}
	defer server.Close()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if config.Server.Blobs != nil && !config.Server.Options.SyncOnly {
		go config.Server.Blobs.RunSweeper(runCtx)
	}
	if config.Lifecycle != nil {
		if err := config.Lifecycle.Start(runCtx); err != nil {
			return err
		}
		defer func() {
			cancel()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
			defer shutdownCancel()
			returnErr = errors.Join(returnErr, config.Lifecycle.Shutdown(shutdownCtx))
		}()
	}

	listener := config.Listener
	if listener == nil {
		listener, err = net.Listen("tcp", config.Server.Options.Listen)
		if err != nil {
			return err
		}
	}
	httpServer := &http.Server{
		Addr: config.Server.Options.Listen, Handler: server.Handler(),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second,
		BaseContext: func(net.Listener) context.Context { return runCtx },
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- httpServer.Serve(listener) }()
	config.Server.Logger.Info("NexTerm server ready", "listen", listener.Addr().String(), "syncOnly", config.Server.Options.SyncOnly)

	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		cancel()
		_ = server.Close()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
		defer shutdownCancel()
		shutdownErr := httpServer.Shutdown(shutdownCtx)
		serveErr := <-serveResult
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	}
}

func loggerDefault() *slog.Logger {
	return slog.Default()
}
