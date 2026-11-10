package pprof

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"strings"
)

const EnvAddr = "NEXTERM_PPROF_ADDR"

// EnvContention opts into block and mutex profiling while pprof is enabled. Recording every blocking event slows
// a busy process, so it stays off unless asked for: N samples about one blocking event per N nanoseconds spent
// blocked and one in N mutex contention events (1 records everything); empty or 0 leaves both profiles off.
const EnvContention = "NEXTERM_PPROF_CONTENTION"

type Server struct {
	server     *http.Server
	listener   net.Listener
	contention bool
}

// ParseContention validates an EnvContention value and returns the sampling rate (0 = off).
func ParseContention(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	rate, err := strconv.Atoi(raw)
	if err != nil || rate < 0 {
		return 0, fmt.Errorf("invalid %s value %q (want 0 or a positive integer)", EnvContention, raw)
	}
	return rate, nil
}

func ResolveAddr(flagValue string, getenv func(string) string) string {
	if value := strings.TrimSpace(flagValue); value != "" {
		return value
	}
	if getenv == nil {
		return ""
	}
	return strings.TrimSpace(getenv(EnvAddr))
}

func NormalizeAddr(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid pprof address %q: %w", addr, err)
	}
	if portNum, err := strconv.Atoi(port); err != nil || portNum < 0 || portNum > 65535 {
		return "", fmt.Errorf("invalid pprof address %q: port must be a number between 0 and 65535", addr)
	}
	if host == "" {
		return net.JoinHostPort("127.0.0.1", port), nil
	}
	if strings.EqualFold(host, "localhost") {
		return addr, nil
	}
	ip, err := netip.ParseAddr(host)
	if err == nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return addr, nil
	}
	return "", fmt.Errorf("pprof address %q must use a loopback host or an explicit wildcard such as 0.0.0.0", addr)
}

func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

func Start(logger *slog.Logger, addr string) (*Server, error) {
	return start(logger, addr, os.Getenv)
}

func start(logger *slog.Logger, addr string, getenv func(string) string) (*Server, error) {
	listenAddr, err := NormalizeAddr(addr)
	if err != nil {
		return nil, err
	}
	if listenAddr == "" {
		return nil, nil
	}
	contention, err := ParseContention(getenv(EnvContention))
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("pprof listen %s: %w", listenAddr, err)
	}
	started := &Server{
		server:     &http.Server{Handler: NewHandler()},
		listener:   listener,
		contention: contention > 0,
	}
	if started.contention {
		runtime.SetBlockProfileRate(contention)
		runtime.SetMutexProfileFraction(contention)
	}
	go func() {
		if err := started.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("pprof server failed", "error", err)
		}
	}()
	logger.Info("pprof endpoints enabled", "addr", "http://"+listener.Addr().String()+"/debug/pprof/", "contentionRate", contention)
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
	if s.contention {
		runtime.SetBlockProfileRate(0)
		runtime.SetMutexProfileFraction(0)
	}
	return s.server.Shutdown(ctx)
}
