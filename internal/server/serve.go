package server

import (
	"bufio"
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

const (
	requestReadIdleTimeout  = time.Minute
	requestWriteIdleTimeout = time.Minute
)

type Lifecycle interface {
	Start(context.Context) error
	Shutdown(context.Context) error
}

type ServeConfig struct {
	Server          Config
	Lifecycle       Lifecycle
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
	if !config.Server.Options.SyncOnly && config.Server.Blobs == nil && config.Server.Options.DataDir != "" {
		config.Server.Blobs = NewBlobStore(config.Server.Options.DataDir, config.Server.Logger)
	}

	core.WarnIfExposed(config.Server.Logger, config.Stderr, config.Server.Options.Listen, config.Server.Options.SyncOnly, config.Server.Options.Auth)
	if config.Server.Options.MasterKey == "" {
		config.Server.Logger.Warn("no vault master key provided; credential synchronization may be unavailable")
	} else if err := BootstrapVault(ctx, config.Server.Vault, config.Server.Options.MasterKey); err != nil {
		config.Server.Logger.Error("vault bootstrap failed", "error", err)
	}

	server, err := New(config.Server)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
		defer closeCancel()
		returnErr = errors.Join(returnErr, server.CloseContext(closeCtx))
	}()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if config.Server.Blobs != nil && !config.Server.Options.SyncOnly {
		go config.Server.Blobs.RunSweeper(runCtx)
	}
	if images := server.Images(); images != nil && !config.Server.Options.SyncOnly {
		go images.RunSweeper(runCtx)
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
		Addr:              config.Server.Options.Listen,
		Handler:           withRequestTimeouts(requestReadIdleTimeout, requestWriteIdleTimeout, server.Handler()),
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
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
		defer shutdownCancel()
		_ = server.CloseContext(shutdownCtx)
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

// withRequestTimeouts 给普通 HTTP 请求施加读/写 idle 超时: 请求体每次读取、
// 响应每次写入都重新续期, 慢速但持续传输的连接不受影响; 空闲 keep-alive 由
// http.Server 的 IdleTimeout 把关, 请求头由 ReadHeaderTimeout 把关。
// 连接一旦被 hijack(WebSocket 等长连接)即清除全部 deadline, 交还给上层自理。
func withRequestTimeouts(readIdle, writeIdle time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		if r.Body != nil {
			r.Body = &idleTimeoutReadCloser{ReadCloser: r.Body, controller: controller, idle: readIdle}
		}
		next.ServeHTTP(&idleTimeoutResponseWriter{ResponseWriter: w, controller: controller, idle: writeIdle}, r)
	})
}

type idleTimeoutReadCloser struct {
	io.ReadCloser
	controller *http.ResponseController
	idle       time.Duration
}

func (r *idleTimeoutReadCloser) Read(data []byte) (int, error) {
	_ = r.controller.SetReadDeadline(time.Now().Add(r.idle))
	return r.ReadCloser.Read(data)
}

type idleTimeoutResponseWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	idle       time.Duration
}

func (w *idleTimeoutResponseWriter) Write(data []byte) (int, error) {
	_ = w.controller.SetWriteDeadline(time.Now().Add(w.idle))
	return w.ResponseWriter.Write(data)
}

func (w *idleTimeoutResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	connection, readWriter, err := w.controller.Hijack()
	if err != nil {
		return nil, nil, err
	}
	_ = connection.SetDeadline(time.Time{})
	return connection, readWriter, nil
}

func (w *idleTimeoutResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
