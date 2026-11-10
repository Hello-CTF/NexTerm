package metrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const (
	EnvAddr        = "NEXTERM_METRICS_ADDR"
	EnvAllowRemote = "NEXTERM_METRICS_ALLOW_REMOTE"
	Path           = "/metrics"
)

// Collector appends the current samples to w. It runs on every scrape.
type Collector func(w *Writer)

type Server struct {
	server   *http.Server
	listener net.Listener
}

// NormalizeAddr validates the metrics listen address. A bare ":PORT" binds
// 127.0.0.1. The endpoint has no authentication, so any host other than a
// loopback one (wildcards included) is refused unless allowRemote is set.
func NormalizeAddr(addr string, allowRemote bool) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid metrics address %q: %w", addr, err)
	}
	if portNum, err := strconv.Atoi(port); err != nil || portNum < 0 || portNum > 65535 {
		return "", fmt.Errorf("invalid metrics address %q: port must be a number between 0 and 65535", addr)
	}
	if host == "" {
		return net.JoinHostPort("127.0.0.1", port), nil
	}
	if Loopback(addr) || allowRemote {
		return addr, nil
	}
	return "", fmt.Errorf("metrics address %q must use a loopback host; set %s=1 to expose the unauthenticated endpoint on another address", addr, EnvAllowRemote)
}

// Loopback reports whether addr binds only a loopback interface.
func Loopback(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// ParseAllowRemote reads the NEXTERM_METRICS_ALLOW_REMOTE value.
func ParseAllowRemote(raw string) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid %s value %q (want 1 or 0)", EnvAllowRemote, raw)
	}
	return value, nil
}

// NewHandler serves GET /metrics from collect.
func NewHandler(collect Collector) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+Path, func(w http.ResponseWriter, _ *http.Request) {
		writer := NewWriter()
		if collect != nil {
			collect(writer)
		}
		w.Header().Set("Content-Type", ContentType)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(writer.Bytes())
	})
	return mux
}

// Start serves the endpoint on its own listener; an empty addr disables it
// and returns a nil server.
func Start(logger *slog.Logger, addr string, allowRemote bool, collect Collector) (*Server, error) {
	listenAddr, err := NormalizeAddr(addr, allowRemote)
	if err != nil {
		return nil, err
	}
	if listenAddr == "" {
		return nil, nil
	}
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("metrics listen %s: %w", listenAddr, err)
	}
	started := &Server{
		server: &http.Server{
			Handler:           NewHandler(collect),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
		},
		listener: listener,
	}
	go func() {
		if err := started.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics server failed", "error", err)
		}
	}()
	url := "http://" + listener.Addr().String() + Path
	if Loopback(listenAddr) {
		logger.Info("metrics endpoint enabled", "addr", url)
	} else {
		logger.Warn("metrics endpoint is exposed beyond loopback without authentication; restrict access with a firewall or a scraping network", "addr", url)
	}
	return started, nil
}

func (s *Server) Addr() string {
	if s == nil || s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) Close(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}
