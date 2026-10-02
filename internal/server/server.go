package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	core "github.com/ProbiusOfficial/NexTerm/internal/app"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/version"
)

const (
	TokenHeader        = "X-NexTerm-Sync-Token"
	PlatformUserHeader = "X-HC-User-ID"
)

var syncOnlyCommands = [...]string{"sync_digest", "sync_export", "sync_import"}

type TokenVerifier interface {
	VerifyToken(context.Context, string) (bool, error)
}

type TokenVerifierFunc func(context.Context, string) (bool, error)

func (f TokenVerifierFunc) VerifyToken(ctx context.Context, token string) (bool, error) {
	return f(ctx, token)
}

type Config struct {
	Options      Options
	Dispatcher   *ipc.Dispatcher
	Environment  ipc.Environment
	Tokens       TokenVerifier
	SyncRPC      http.Handler
	Events       *EventBroker
	Channels     ChannelBinder
	ChannelStats ChannelStatsFunc
	Blobs        *BlobStore
	Static       http.Handler
	Vault        Vault
	VaultStatus  func(context.Context) (any, error)
	Version      string
	MaxRPCBytes  int64
	Logger       *slog.Logger
}

type Server struct {
	options        Options
	dispatcher     *ipc.Dispatcher
	syncDispatcher *ipc.Dispatcher
	environment    ipc.Environment
	tokens         TokenVerifier
	events         *EventBroker
	channels       ChannelBinder
	channelStats   ChannelStatsFunc
	version        string
	vaultStatus    func(context.Context) (any, error)
	handler        http.Handler
	logger         *slog.Logger
	sockets        socketTracker
}

type Health struct {
	OK               bool    `json:"ok"`
	Service          string  `json:"service"`
	Version          string  `json:"version"`
	SyncOnly         bool    `json:"syncOnly"`
	Commands         int     `json:"commands"`
	EventSubscribers int     `json:"eventSubscribers"`
	LiveChannels     int     `json:"liveChannels"`
	PendingChannels  int     `json:"pendingChannels"`
	WebRoot          *string `json:"webRoot"`
	Vault            any     `json:"vault"`
}

func New(config Config) (*Server, error) {
	if config.Dispatcher == nil {
		return nil, fmt.Errorf("RPC dispatcher is required")
	}
	if config.SyncRPC == nil && config.Tokens == nil {
		return nil, fmt.Errorf("sync RPC handler or token verifier is required")
	}
	if err := core.ValidateListenAddress(config.Options.Listen); config.Options.Listen != "" && err != nil {
		return nil, err
	}
	if !config.Options.SyncOnly && config.Channels == nil {
		return nil, fmt.Errorf("channel binder is required in full server mode")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Events == nil {
		config.Events = NewEventBroker()
	}
	if config.Version == "" {
		config.Version = version.Version
	}
	if config.MaxRPCBytes == 0 {
		config.MaxRPCBytes = ipc.DefaultMaxRPCBytes
	}
	if config.VaultStatus == nil && config.Vault != nil {
		config.VaultStatus = func(context.Context) (any, error) {
			return config.Vault.Status(), nil
		}
	}

	s := &Server{
		options: config.Options, dispatcher: config.Dispatcher, environment: config.Environment,
		tokens: config.Tokens, events: config.Events, channels: config.Channels,
		channelStats: config.ChannelStats, version: config.Version, vaultStatus: config.VaultStatus,
		logger: config.Logger,
	}
	if s.environment.Events == nil {
		s.environment.Events = s.events
	}

	syncDispatcher, err := NewSyncOnlyDispatcher(config.Dispatcher)
	if err != nil {
		return nil, err
	}
	s.syncDispatcher = syncDispatcher
	s.handler = s.routes(config)
	return s, nil
}

func NewSyncOnlyDispatcher(full *ipc.Dispatcher) (*ipc.Dispatcher, error) {
	if full == nil {
		return nil, fmt.Errorf("RPC dispatcher is required")
	}
	available := make(map[string]bool)
	for _, command := range full.Commands() {
		available[command] = true
	}
	restricted := ipc.NewDispatcher()
	for _, command := range syncOnlyCommands {
		if !available[command] {
			return nil, fmt.Errorf("sync-only dispatcher is missing %s", command)
		}
		if err := restricted.RegisterRaw(command, func(ctx context.Context, call *ipc.Call) (any, error) {
			response := full.Dispatch(ctx, ipc.Request{
				Command: call.Command, Args: call.Args, Channel: call.Channel, ClientID: call.ClientID,
			}, ipc.Environment{ClientID: call.ClientID, Events: call.Events, Streams: call.Streams})
			if !response.OK {
				return nil, response.Error
			}
			return response.Data, nil
		}); err != nil {
			return nil, err
		}
	}
	return restricted, nil
}

func SyncOnlyCommands() []string {
	commands := make([]string, len(syncOnlyCommands))
	copy(commands, syncOnlyCommands[:])
	return commands
}

func (s *Server) routes(config Config) http.Handler {
	mux := http.NewServeMux()
	if config.SyncRPC != nil {
		mux.Handle("POST /sync/rpc", config.SyncRPC)
	} else {
		syncRPC := ipc.NewRPCHandler(s.syncDispatcher, s.environment)
		syncRPC.MaxBytes = config.MaxRPCBytes
		mux.Handle("POST /sync/rpc", s.authenticatedSync(syncRPC))
	}
	mux.HandleFunc("GET /healthz", s.serveHealth)
	if s.options.SyncOnly {
		return mux
	}

	rpcHandler := ipc.NewRPCHandler(s.dispatcher, s.environment)
	rpcHandler.MaxBytes = config.MaxRPCBytes
	mux.Handle("POST /rpc", rpcHandler)
	mux.HandleFunc("GET /ws/events", s.serveEvents)
	mux.HandleFunc("GET /ws/channel/{id}", s.serveChannel)

	blobs := config.Blobs
	if blobs == nil && s.options.DataDir != "" {
		blobs = NewBlobStore(s.options.DataDir, s.logger)
	}
	if blobs != nil {
		mux.HandleFunc("POST /files/blob", blobs.Stage)
		mux.HandleFunc("GET /files/blob", blobs.Download)
		mux.HandleFunc("DELETE /files/blob", blobs.Delete)
		mux.HandleFunc("POST /files/blob/reserve", blobs.Reserve)
	}
	staticHandler := config.Static
	if staticHandler == nil && s.options.WebRoot != "" {
		staticHandler = NewStaticHandler(s.options.WebRoot)
	}
	if staticHandler != nil {
		mux.Handle("/", staticHandler)
	}
	return mux
}

func (s *Server) authenticatedSync(next *ipc.RPCHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !hasPlatformUser(r) {
			valid, err := s.tokens.VerifyToken(r.Context(), r.Header.Get(TokenHeader))
			if err != nil {
				writeRPCError(w, http.StatusInternalServerError, ipc.NormalizeError(err))
				return
			}
			if !valid {
				writeRPCError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, "同步令牌无效或缺失"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func hasPlatformUser(r *http.Request) bool {
	for name := range r.Header {
		if strings.EqualFold(name, PlatformUserHeader) {
			return true
		}
	}
	return false
}

func writeRPCError(w http.ResponseWriter, status int, err *ipc.Error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ipc.Failure(err))
}

func (s *Server) serveHealth(w http.ResponseWriter, r *http.Request) {
	var vaultStatus any
	if s.vaultStatus != nil {
		status, err := s.vaultStatus(r.Context())
		if err != nil {
			s.logger.Warn("vault status unavailable", "error", err)
		} else {
			vaultStatus = status
		}
	}
	var webRoot *string
	if !s.options.SyncOnly && s.options.WebRoot != "" {
		webRoot = &s.options.WebRoot
	}
	channelStats := ChannelStats{}
	if s.channelStats != nil {
		channelStats = s.channelStats()
	}
	commands := s.dispatcher.Len()
	if s.options.SyncOnly {
		commands = s.syncDispatcher.Len()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(Health{
		OK: true, Service: "nexterm-server", Version: s.version, SyncOnly: s.options.SyncOnly,
		Commands: commands, EventSubscribers: s.events.SubscriberCount(),
		LiveChannels: channelStats.LiveChannels, PendingChannels: channelStats.PendingChannels,
		WebRoot: webRoot, Vault: vaultStatus,
	})
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) Events() *EventBroker { return s.events }

func (s *Server) Close() error {
	err := s.events.Close()
	s.sockets.closeAndWait()
	return err
}
