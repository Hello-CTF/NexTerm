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
	"runtime"
	"strconv"
	"strings"
)

const EnvAddr = "NEXTERM_PPROF_ADDR"

type Server struct {
	server   *http.Server
	listener net.Listener
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
	listenAddr, err := NormalizeAddr(addr)
	if err != nil {
		return nil, err
	}
	if listenAddr == "" {
		return nil, nil
	}
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("pprof listen %s: %w", listenAddr, err)
	}
	runtime.SetBlockProfileRate(1)
	runtime.SetMutexProfileFraction(1)
	started := &Server{
		server:   &http.Server{Handler: NewHandler()},
		listener: listener,
	}
	go func() {
		if err := started.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("pprof server failed", "error", err)
		}
	}()
	logger.Info("pprof endpoints enabled", "addr", "http://"+listener.Addr().String()+"/debug/pprof/")
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
