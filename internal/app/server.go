package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
)

type ServeConfig struct {
	Listen         string
	WebRoot        string
	SyncOnly       bool
	Static         http.Handler
	Transport      http.Handler
	CloseTransport func(context.Context) error
	Stderr         io.Writer
}

type Health struct {
	OK        bool             `json:"ok"`
	Service   string           `json:"service"`
	Version   string           `json:"version"`
	SyncOnly  bool             `json:"syncOnly"`
	Commands  int              `json:"commands"`
	WebRoot   *string          `json:"webRoot"`
	Vault     any              `json:"vault"`
	Retention *RetentionHealth `json:"retention"`
}

func (a *Application) Serve(ctx context.Context, config ServeConfig) (returnErr error) {
	if err := ValidateListenAddress(config.Listen); err != nil {
		return err
	}
	if err := a.Start(ctx); err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		returnErr = errors.Join(returnErr, a.Shutdown(shutdownCtx))
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(a.health(r.Context(), config))
	})
	if !config.SyncOnly && config.Static != nil {
		mux.Handle("/", config.Static)
	}

	handler := config.Transport
	if handler == nil {
		if !config.SyncOnly {
			return fmt.Errorf("full server mode requires the server transport stack")
		}
		handler = mux
	} else if config.CloseTransport != nil {
		defer func() {
			returnErr = errors.Join(returnErr, config.CloseTransport(context.Background()))
		}()
	}
	if config.Stderr == nil {
		config.Stderr = os.Stderr
	}
	WarnIfExposed(a.logger, config.Stderr, config.Listen, config.SyncOnly)
	server := &http.Server{
		Addr:              config.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.ListenAndServe()
	}()

	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		serveErr := <-serveResult
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	}
}

func (a *Application) health(ctx context.Context, config ServeConfig) Health {
	var webRoot *string
	if config.Static != nil && config.WebRoot != "" {
		webRoot = &config.WebRoot
	}
	var vault any
	if a.vaultStatus != nil {
		status, err := a.vaultStatus(ctx)
		if err != nil {
			a.logger.Warn("vault status unavailable", "error", err)
		} else {
			vault = status
		}
	}
	var retention *RetentionHealth
	if a.retentionStatus != nil {
		status, err := a.retentionStatus(ctx)
		retention = &status
		if err != nil {
			a.logger.Warn("retention status unavailable", "error", err)
			if retention.LastError == "" {
				retention.LastError = err.Error()
			}
		}
	}
	commands := a.Dispatcher.Len()
	return Health{
		OK: true, Service: "nexterm-server", Version: a.version, SyncOnly: config.SyncOnly,
		Commands: commands, WebRoot: webRoot, Vault: vault, Retention: retention,
	}
}

func LoopbackListen(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func WarnIfExposed(logger *slog.Logger, stderr io.Writer, address string, syncOnly bool) {
	if LoopbackListen(address) {
		return
	}

	detail := "完整版已启用访问控制：/rpc、/ws 与 /files/blob 要求账号会话（浏览器登录后使用），/healthz 与页面静态资源保持公开。公网部署仍建议套 TLS 反向代理并用防火墙限制来源地址。"
	if syncOnly {
		detail = "onlyServer 模式：仅提供账号登录与同步端点；能连接此端口且持有账号会话的人可以同步资产库（端到端加密）。请使用防火墙限制对端地址。"
	}
	if logger != nil {
		logger.Warn("HTTP server is listening on a non-loopback address", "listen", address, "syncOnly", syncOnly, "risk", detail)
	}
	if stderr != nil {
		_, _ = fmt.Fprintf(stderr, "WARNING: NexTerm is listening on non-loopback address %s. %s\n", address, detail)
	}
}
