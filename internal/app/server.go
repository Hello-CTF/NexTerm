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
	"reflect"
	"time"
)

// transportCloseTimeout bounds how long shutdown waits for open WebSockets to
// finish their going-away close handshake before they are dropped.
var transportCloseTimeout = 10 * time.Second

type ServeConfig struct {
	Listen         string
	WebRoot        string
	SyncOnly       bool
	Auth           string
	Static         http.Handler
	Transport      http.Handler
	CloseTransport func(context.Context) error
	Stderr         io.Writer
	// RequireVault makes /healthz report unavailable (503) while the vault is locked.
	RequireVault bool

	// Listener replaces the listener opened for Listen; nil keeps ListenAndServe behavior.
	Listener net.Listener
	// ReadIdleTimeout and WriteIdleTimeout bound request-body and response-write stalls; zero uses one minute.
	ReadIdleTimeout  time.Duration
	WriteIdleTimeout time.Duration
}

// HealthSummary is the /healthz payload every caller may read: fleet agents check
// ok and service, and clients read version for one-line agent install commands.
type HealthSummary struct {
	OK      bool   `json:"ok"`
	Service string `json:"service"`
	Version string `json:"version"`
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
	mux.HandleFunc("GET /healthz", a.serveHealth(config))
	mux.HandleFunc("GET /livez", a.serveLive)
	if !config.SyncOnly && config.Static != nil {
		mux.Handle("/", config.Static)
	}

	handler := config.Transport
	transportClosed := false
	closeTransport := func() error {
		if transportClosed || config.Transport == nil || config.CloseTransport == nil {
			return nil
		}
		transportClosed = true
		closeCtx, cancel := context.WithTimeout(context.Background(), transportCloseTimeout)
		defer cancel()
		return config.CloseTransport(closeCtx)
	}
	if handler == nil {
		if !config.SyncOnly {
			return fmt.Errorf("full server mode requires the server transport stack")
		}
		handler = mux
	} else {
		defer func() { returnErr = errors.Join(returnErr, closeTransport()) }()
	}
	if config.Stderr == nil {
		config.Stderr = os.Stderr
	}
	WarnIfExposed(a.logger, config.Stderr, config.Listen, config.SyncOnly, config.Auth)
	requestBase, cancelRequests := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRequests()
	readIdle, writeIdle := config.ReadIdleTimeout, config.WriteIdleTimeout
	if readIdle <= 0 {
		readIdle = defaultRequestReadIdleTimeout
	}
	if writeIdle <= 0 {
		writeIdle = defaultRequestWriteIdleTimeout
	}
	server := &http.Server{
		Addr:              config.Listen,
		Handler:           withRequestTimeouts(readIdle, writeIdle, handler),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// net/http reports accept failures, handler panics and protocol misuse here; keep them in the app log.
		ErrorLog: slog.NewLogLogger(a.logger.With("component", "http").Handler(), slog.LevelWarn),
		// Requests outlive ctx until the transport has closed its WebSockets: a hijacked connection whose read
		// context is cancelled is dropped without a close frame, so the going-away close would never arrive.
		BaseContext: func(net.Listener) context.Context {
			return requestBase
		},
	}
	serveResult := make(chan error, 1)
	go func() {
		if config.Listener != nil {
			serveResult <- server.Serve(config.Listener)
			return
		}
		serveResult <- server.ListenAndServe()
	}()

	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		transportErr := closeTransport()
		cancelRequests()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr := errors.Join(transportErr, server.Shutdown(shutdownCtx))
		serveErr := <-serveResult
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	}
}

// serveHealth is the readiness probe of the transport-less sync-only mux.
func (a *Application) serveHealth(config ServeConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		health := a.health(r.Context(), config)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if !health.OK {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		if !directLoopbackPeer(r) {
			_ = json.NewEncoder(w).Encode(HealthSummary{OK: health.OK, Service: health.Service, Version: health.Version})
			return
		}
		_ = json.NewEncoder(w).Encode(health)
	}
}

func (a *Application) serveLive(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(HealthSummary{OK: true, Service: "nexterm-server", Version: a.version})
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
		OK: !config.RequireVault || VaultUnlocked(vault), Service: "nexterm-server", Version: a.version, SyncOnly: config.SyncOnly,
		Commands: commands, WebRoot: webRoot, Vault: vault, Retention: retention,
	}
}

// VaultUnlocked reports whether a vault status value (vault.Status or a pointer to it) describes an unlocked vault.
// It reads the Unlocked field by reflection so this package does not import internal/vault, which would make
// vault's own tests (which reach this package through internal/upgrade) an import cycle.
func VaultUnlocked(status any) bool {
	value := reflect.ValueOf(status)
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return false
	}
	field := value.FieldByName("Unlocked")
	return field.IsValid() && field.Kind() == reflect.Bool && field.Bool()
}

// directLoopbackPeer reports a loopback peer that did not relay another caller.
func directLoopbackPeer(r *http.Request) bool {
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" || r.Header.Get("X-Real-IP") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

func WarnIfExposed(logger *slog.Logger, stderr io.Writer, address string, syncOnly bool, auth string) {
	if LoopbackListen(address) {
		return
	}
	mode := auth
	// On a non-loopback listen, loopback equals on, consistent with the server-side effectiveAuthMode.
	if mode == AuthLoopback {
		mode = AuthOn
	}

	var detail string
	switch {
	case syncOnly && mode == AuthOff:
		detail = "onlyServer 模式（--auth=off）：账号功能已关闭，同步端点拒绝所有请求，仅 /healthz 保持可用。请使用防火墙限制对端地址，或改用 --auth=on。"
	case syncOnly:
		detail = "onlyServer 模式：仅提供账号登录与同步端点；能连接此端口且持有账号会话的人可以同步资产库（客户端加密，服务端只保存密文）。请使用防火墙限制对端地址。"
	case mode == AuthPlatform:
		detail = "平台托管访问控制（--auth=platform）：只有携带 NEXTERM_GATEWAY_AUTH 对应请求头（由前置网关注入）的请求被放行，其余一律拒绝；首个通过网关访问的用户将成为平台所有者，此模式不使用初始化码。请确保该监听端口只对前置网关可达。"
	case mode == AuthOff:
		detail = "完整版访问控制已关闭（--auth=off）：/rpc、/ws 与 /files/blob 不要求任何会话，任何能连接此端口的人都能操作服务器；账号与管理员路由保持关闭。请立即用防火墙限制来源地址，或改用 --auth=on。"
	default:
		detail = "完整版已启用访问控制：/rpc、/ws 与 /files/blob 要求账号会话（浏览器登录后使用），/healthz 与页面静态资源保持公开。公网部署仍建议使用 TLS 反向代理并用防火墙限制来源地址。"
	}
	if logger != nil {
		logger.Warn("HTTP server is listening on a non-loopback address", "listen", address, "syncOnly", syncOnly, "auth", mode, "risk", detail)
	}
	if stderr != nil {
		_, _ = fmt.Fprintf(stderr, "WARNING: NexTerm is listening on non-loopback address %s. %s\n", address, detail)
	}
}
