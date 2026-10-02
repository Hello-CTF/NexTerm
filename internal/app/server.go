package app

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

type ServeConfig struct {
	Listen   string
	WebRoot  string
	SyncOnly bool
	SyncRPC  http.Handler
	Static   http.Handler
}

type Health struct {
	OK       bool    `json:"ok"`
	Service  string  `json:"service"`
	Version  string  `json:"version"`
	SyncOnly bool    `json:"syncOnly"`
	Commands int     `json:"commands"`
	WebRoot  *string `json:"webRoot"`
	Vault    any     `json:"vault"`
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
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		var webRoot *string
		if config.Static != nil && config.WebRoot != "" {
			webRoot = &config.WebRoot
		}
		_ = json.NewEncoder(w).Encode(Health{
			OK:       true,
			Service:  "nexterm-server",
			Version:  a.version,
			SyncOnly: config.SyncOnly,
			Commands: a.Dispatcher.Len(),
			WebRoot:  webRoot,
		})
	})
	if !config.SyncOnly {
		mux.Handle("/rpc", ipc.NewRPCHandler(a.Dispatcher, a.Environment("")))
	}
	if config.SyncRPC != nil {
		mux.Handle("/sync/rpc", config.SyncRPC)
	}
	if config.Static != nil {
		mux.Handle("/", config.Static)
	}

	if !loopbackListen(config.Listen) {
		a.logger.Warn("HTTP server is listening on a non-loopback address", "listen", config.Listen)
	}
	server := &http.Server{
		Addr:              config.Listen,
		Handler:           mux,
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

func loopbackListen(address string) bool {
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
