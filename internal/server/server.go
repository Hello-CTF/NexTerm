package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	core "github.com/Hello-CTF/NexTerm/internal/app"
	fleetserver "github.com/Hello-CTF/NexTerm/internal/fleet/server"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/sharing"
	"github.com/Hello-CTF/NexTerm/internal/store"
	syncservice "github.com/Hello-CTF/NexTerm/internal/sync"
	"github.com/Hello-CTF/NexTerm/internal/version"
)

const GatewayAuthHeader = syncservice.GatewayAuthHeader

// SettingStore is the minimal read/write subset of the setting table, injected by the wiring layer as *store.Store.
type SettingStore interface {
	SettingGet(ctx context.Context, key string) (string, bool, error)
	SettingSet(ctx context.Context, key, value string) error
	SettingSetManyDelete(ctx context.Context, values map[string]string, deleteKeys ...string) error
	SettingListPrefix(ctx context.Context, prefix string) (map[string]string, error)
}

// AuditFunc writes audit records; bridged by the wiring layer to store.AuditInsert.
type AuditFunc func(ctx context.Context, source, kind string, payload map[string]any) error

// AssetOwnerFunc looks up the owner user ID by asset ID (not restricted by the caller's owner scope),
// letting /ws/events filter sensitive events by ownership; bridged by the wiring layer to store.AssetOwnerID.
type AssetOwnerFunc func(ctx context.Context, assetID string) (string, error)

// DBHealthSource is the non-secret database metadata source exposed by /healthz.
type DBHealthSource interface {
	Backend() store.Backend
	DB() *sql.DB
}

type AccountConnectionCloser interface {
	CloseAccountSession(sessionID string) error
}

// AccountCleanupFunc receives only trusted owner, cleanup event, and revoked session IDs.
// A snapshot read failure is returned explicitly so the deferred (natural-expiry)
// path records the failure instead of treating it as a successful cleanup.
type AccountCleanupFunc func(ctx context.Context, ownerID, cleanupID string, sessionIDs ...string) error

// AccountCleanupPrepareFunc reserves an owner snapshot before revocation; commit and rollback are mutually exclusive.
// A snapshot read failure is returned explicitly so the caller aborts without
// committing an empty event and can retry with the same event id.
type AccountCleanupPrepareFunc func(ctx context.Context, ownerID, cleanupID string) (commit func(sessionIDs ...string), rollback func(), err error)

type Config struct {
	Options    Options
	Dispatcher *ipc.Dispatcher
	// PeerDispatcher is the sync peer command surface (the three asset bundle commands), mounted to /sync/rpc and counted in health only in sync-only deployments.
	PeerDispatcher *ipc.Dispatcher
	Environment    ipc.Environment
	Accounts       *account.Accounts
	SyncObjects    http.Handler
	GatewayAuthKey string
	Events         *EventBroker
	Channels       ChannelBinder
	ChannelStats   ChannelStatsFunc
	Blobs          *BlobStore
	Settings       SettingStore
	AuditFunc      AuditFunc
	AssetOwnerFunc AssetOwnerFunc
	Static         http.Handler
	Vault          Vault
	VaultStatus    func(context.Context) (any, error)
	Retention      *RetentionConfig
	Fleet          *fleetserver.Service
	// Previews together with Spectator enables remote previews (public preview links): management endpoints
	// are mounted under the account routes, and the public data plane is GET /share/preview/{token}. If Spectator also
	// implements RemoteSessionRegistry (the *session.Manager injected in production does), the
	// /share/remote-sessions remote session shell management surface is additionally mounted.
	Previews              *sharing.Service
	Spectator             PreviewSpectator
	DB                    DBHealthSource
	AccountConnections    AccountConnectionCloser
	AccountCleanup        AccountCleanupFunc
	AccountCleanupPrepare AccountCleanupPrepareFunc
	Version               string
	MaxRPCBytes           int64
	Logger                *slog.Logger
	WebSocket             WebSocketConfig
}

type Server struct {
	options           Options
	trustedProxyCIDRs []netip.Prefix
	dispatcher        *ipc.Dispatcher
	peerDispatcher    *ipc.Dispatcher
	environment       ipc.Environment
	accounts          *account.Accounts
	accountThrottle   accountThrottles
	mfaTickets        *mfaTicketStore
	gatewayAuthKey    string
	authRequired      bool
	events            *EventBroker
	channels          ChannelBinder
	channelStats      ChannelStatsFunc
	version           string
	vaultStatus       func(context.Context) (any, error)
	retention         *RetentionConfig
	handler           http.Handler
	logger            *slog.Logger
	sockets           socketTracker
	closeOnce         sync.Once
	closeErr          error
	webSocket         WebSocketConfig
	preferences       *account.Preferences
	audit             AuditFunc
	assetOwner        AssetOwnerFunc
	fleet             *fleetserver.Service
	previews          *sharing.Service
	spectator         PreviewSpectator
	// remoteSessions is obtained by asserting Spectator (the same instance); when non-nil, remote session shell endpoints are mounted.
	remoteSessions RemoteSessionRegistry
	previewResolve *account.LoginThrottle
	// previewRevalidateInterval is the authorization recheck interval for preview connections; tests may lower it.
	previewRevalidateInterval time.Duration
	db                        DBHealthSource
	accountConnections        AccountConnectionCloser
	accountCleanup            AccountCleanupFunc
	accountCleanupPrepare     AccountCleanupPrepareFunc
	accountRevocationMu       *sync.Mutex
	revokeUserSessions        func(context.Context, string, string, ...func() error) ([]string, error)
	accountExecutionAdmission func(context.Context, string, *account.Identity, func() error) error

	readGate func()
}

type accountThrottles struct {
	login    *account.LoginThrottle
	recovery *account.LoginThrottle
	init     *account.LoginThrottle
	register *account.LoginThrottle
	mfa      *account.LoginThrottle
}

type Health struct {
	OK               bool             `json:"ok"`
	Service          string           `json:"service"`
	Version          string           `json:"version"`
	SyncOnly         bool             `json:"syncOnly"`
	Commands         int              `json:"commands"`
	EventSubscribers int              `json:"eventSubscribers"`
	LiveChannels     int              `json:"liveChannels"`
	PendingChannels  int              `json:"pendingChannels"`
	WebRoot          *string          `json:"webRoot"`
	Vault            any              `json:"vault"`
	Retention        *RetentionHealth `json:"retention"`
	DB               *DBHealth        `json:"db,omitempty"`
}

// DBHealth is the non-secret database metadata exposed by /healthz: backend name, ping latency, and connection pool counts.
type DBHealth struct {
	Backend string       `json:"backend"`
	PingOK  bool         `json:"pingOk"`
	PingMS  int64        `json:"pingMs"`
	Pool    *DBPoolStats `json:"pool,omitempty"`
}

type DBPoolStats struct {
	OpenConnections int   `json:"openConnections"`
	InUse           int   `json:"inUse"`
	Idle            int   `json:"idle"`
	WaitCount       int64 `json:"waitCount"`
}

func New(config Config) (*Server, error) {
	trustedProxyCIDRs := make([]netip.Prefix, 0, len(config.Options.TrustedProxyCIDRs))
	for _, raw := range config.Options.TrustedProxyCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", raw, err)
		}
		trustedProxyCIDRs = append(trustedProxyCIDRs, prefix.Masked())
	}
	if config.Dispatcher == nil {
		return nil, fmt.Errorf("RPC dispatcher is required")
	}
	if config.AccountCleanup != nil && config.AccountCleanupPrepare == nil {
		return nil, fmt.Errorf("account cleanup preparation is required when account cleanup is configured")
	}
	if config.SyncObjects == nil && config.Accounts == nil {
		return nil, fmt.Errorf("sync object handler or account service is required")
	}
	authMode := config.Options.Auth
	if authMode == "" {
		authMode = AuthLoopback
	}
	switch authMode {
	case AuthOn, AuthLoopback, AuthPlatform, AuthOff:
	default:
		return nil, fmt.Errorf("invalid auth mode %q", authMode)
	}
	// In platform mode the only admission credential is the gateway-injected request header; without a configured key every request would be rejected,
	// so failing at startup beats a silent lockout.
	if authMode == AuthPlatform && config.GatewayAuthKey == "" {
		return nil, fmt.Errorf("auth mode %q requires a gateway key (NEXTERM_GATEWAY_AUTH); without it no request can be admitted", AuthPlatform)
	}
	config.Options.Auth = authMode
	authRequired := false
	switch authMode {
	case AuthPlatform:
		// Platform mode does not distinguish syncOnly: the gate is on the gateway, not on the listen address.
		authRequired = true
	default:
		if !config.Options.SyncOnly {
			switch authMode {
			case AuthOn:
				authRequired = true
			case AuthLoopback:
				authRequired = !core.LoopbackListen(config.Options.Listen)
			}
		}
	}
	if authRequired && config.Accounts == nil {
		if authMode == AuthOn {
			return nil, fmt.Errorf("account service is required when auth mode is %q", AuthOn)
		}
		return nil, fmt.Errorf("account service is required when listening on a non-loopback address")
	}
	if err := core.ValidateListenAddress(config.Options.Listen); config.Options.Listen != "" && err != nil {
		return nil, err
	}
	if !config.Options.SyncOnly && config.Channels == nil {
		return nil, fmt.Errorf("channel binder is required in full server mode")
	}
	if config.Retention != nil && config.Retention.Enabled && config.Retention.Status == nil {
		return nil, fmt.Errorf("enabled retention requires a status provider")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Events == nil {
		config.Events = NewEventBroker()
	}
	if config.Events.OnSlowSubscriber == nil {
		config.Events.OnSlowSubscriber = func() {
			config.Logger.Warn("event subscriber dropped: client could not keep up")
		}
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
	allowedOrigins := config.Options.AllowedOrigins
	if allowedOrigins == nil {
		allowedOrigins = defaultAllowedOrigins
	}
	config.Options.AllowedOrigins = append([]string(nil), allowedOrigins...)

	s := &Server{
		options: config.Options, trustedProxyCIDRs: trustedProxyCIDRs, dispatcher: config.Dispatcher, peerDispatcher: config.PeerDispatcher, environment: config.Environment,
		accounts: config.Accounts,
		accountThrottle: accountThrottles{
			login: account.NewLoginThrottle(), recovery: account.NewLoginThrottle(),
			init: account.NewLoginThrottle(), register: account.NewLoginThrottle(),
			mfa: account.NewLoginThrottle(),
		},
		mfaTickets:     newMFATicketStore(),
		gatewayAuthKey: config.GatewayAuthKey, authRequired: authRequired,
		events: config.Events, channels: config.Channels,
		channelStats: config.ChannelStats, version: config.Version, vaultStatus: config.VaultStatus,
		retention: config.Retention, logger: config.Logger, webSocket: config.WebSocket.withDefaults(),
		audit: config.AuditFunc, fleet: config.Fleet, assetOwner: config.AssetOwnerFunc,
		previews: config.Previews, spectator: config.Spectator,
		previewResolve:            account.NewLoginThrottle(),
		previewRevalidateInterval: defaultPreviewRevalidateInterval,
		db:                        config.DB,
		accountConnections:        config.AccountConnections,
		accountCleanup:            config.AccountCleanup,
		accountCleanupPrepare:     config.AccountCleanupPrepare,
	}
	if config.Accounts != nil {
		s.revokeUserSessions = config.Accounts.RevokeUserSessionsWithIDs
		// The revocation boundary is shared with the account layer so fleet-side
		// revocations bump the same epoch inside the same critical section.
		s.accountRevocationMu = config.Accounts.RevocationMu()
	} else {
		s.accountRevocationMu = &sync.Mutex{}
	}
	s.accountExecutionAdmission = s.admitAccountExecution
	if s.environment.Events == nil {
		s.environment.Events = s.events
	}
	if remote, ok := config.Spectator.(RemoteSessionRegistry); ok {
		s.remoteSessions = remote
	}
	if config.Settings != nil {
		s.preferences = account.NewPreferences(config.Settings)
	}

	s.handler = s.transportGuard(s.routes(config))
	return s, nil
}

func (s *Server) routes(config Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.serveHealth)
	s.mountAccountRoutes(mux)
	if config.SyncObjects != nil && s.accounts != nil {
		inject := func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				identity := accountIdentityFrom(r.Context())
				if identity == nil {
					writeAccountError(w, http.StatusUnauthorized, ipc.NewError(ipc.CodeForbidden, sessionReloginMessage))
					return
				}
				next.ServeHTTP(w, r.WithContext(syncservice.WithUserID(r.Context(), identity.UserID)))
			})
		}
		mux.Handle("POST /sync/v2/push", s.accountGuard(s.requireAccountSession(s.requireAccountCSRF(inject(config.SyncObjects)))))
		mux.Handle("POST /sync/v2/delete", s.accountGuard(s.requireAccountSession(s.requireAccountCSRF(inject(config.SyncObjects)))))
		mux.Handle("POST /sync/v2/pull", s.accountGuard(s.requireAccountSession(inject(config.SyncObjects))))
		mux.Handle("POST /sync/v2/ids", s.accountGuard(s.requireAccountSession(inject(config.SyncObjects))))
		mux.Handle("POST /sync/v2/wait", s.accountGuard(s.requireAccountSession(inject(config.SyncObjects))))
	}
	if s.options.SyncOnly {
		// Peer sync RPC surface: only the digest/export/import three commands, under the same session and CSRF checks as the account RPC;
		// Host and Origin boundaries are uniformly enforced by transportGuard. The full build does not mount this surface (/rpc already has the three commands).
		if config.PeerDispatcher != nil && s.accounts != nil {
			peerRPC := ipc.NewRPCHandler(config.PeerDispatcher, s.environment)
			peerRPC.MaxBytes = config.MaxRPCBytes
			mux.Handle("POST /sync/rpc", s.accountGuard(s.requireAccountSession(s.requireAccountCSRF(peerRPC))))
		}
		return mux
	}

	staticHandler := config.Static
	if staticHandler == nil && s.options.WebRoot != "" {
		staticHandler = NewStaticHandler(s.options.WebRoot)
	}

	if s.fleet != nil {
		s.fleet.Mount(mux)
	}
	if !s.options.SyncOnly && s.previews != nil && s.spectator != nil {
		// Remote preview's public data plane: a plain GET shares the same static entry as the app shell,
		// only the WS upgrade goes through token checks; read-only/writable is decided by the link permission.
		mux.HandleFunc("GET /share/preview/{token}", s.serveSharePreview(staticHandler))
	}

	rpcHandler := ipc.NewRPCHandler(s.dispatcher, s.environment)
	rpcHandler.MaxBytes = config.MaxRPCBytes
	mux.Handle("POST /rpc", compressResponses(false, s.requireAuth(rpcHandler)))
	mux.HandleFunc("GET /ws/events", s.serveEvents)
	mux.HandleFunc("GET /ws/channel/{id}", s.serveChannel)

	blobs := config.Blobs
	if blobs == nil && s.options.DataDir != "" {
		blobs = NewBlobStore(s.options.DataDir, s.logger)
	}
	if blobs != nil {
		// When any identity-less admission path exists (auth=off/loopback listen/gateway), the ownership concept does not hold,
		// and staged blobs allow ownerless entries; owner equality is kept only when every admitted request carries an account session identity.
		blobs.openAccess = !(s.authRequired && s.accounts != nil && s.gatewayAuthKey == "")
		mux.Handle("POST /files/blob", s.requireAuth(http.HandlerFunc(blobs.Stage)))
		// Not compressed: a gzip stream has no Content-Length, which would break download progress and Range.
		mux.Handle("GET /files/blob", s.requireAuth(http.HandlerFunc(blobs.Download)))
		mux.Handle("DELETE /files/blob", s.requireAuth(http.HandlerFunc(blobs.Delete)))
		mux.Handle("POST /files/blob/reserve", s.requireAuth(http.HandlerFunc(blobs.Reserve)))
	}
	if staticHandler != nil {
		mux.Handle("/", staticHandler)
	}
	return mux
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
	var retention *RetentionHealth
	if s.retention != nil {
		status, err := s.retention.health(r.Context())
		retention = status
		if err != nil {
			s.logger.Warn("retention status unavailable", "error", err)
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
		// sync-only mounts only the peer command surface, and health truthfully reports its real command count (0 when not mounted).
		commands = 0
		if s.peerDispatcher != nil && s.accounts != nil {
			commands = s.peerDispatcher.Len()
		}
	}
	var dbHealth *DBHealth
	if s.db != nil {
		pingCtx, pingCancel := context.WithTimeout(r.Context(), 2*time.Second)
		pingStart := time.Now()
		pingErr := s.db.DB().PingContext(pingCtx)
		pingCancel()
		stats := s.db.DB().Stats()
		dbHealth = &DBHealth{
			Backend: string(s.db.Backend()),
			PingOK:  pingErr == nil,
			PingMS:  time.Since(pingStart).Milliseconds(),
			Pool: &DBPoolStats{
				OpenConnections: stats.OpenConnections,
				InUse:           stats.InUse,
				Idle:            stats.Idle,
				WaitCount:       stats.WaitCount,
			},
		}
		if pingErr != nil {
			s.logger.Warn("database ping failed", "error", pingErr)
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(Health{
		OK: true, Service: "nexterm-server", Version: s.version, SyncOnly: s.options.SyncOnly,
		Commands: commands, EventSubscribers: s.events.SubscriberCount(),
		LiveChannels: channelStats.LiveChannels, PendingChannels: channelStats.PendingChannels,
		WebRoot: webRoot, Vault: vaultStatus, Retention: retention, DB: dbHealth,
	})
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) Events() *EventBroker { return s.events }

func (s *Server) Close() error {
	return s.CloseContext(context.Background())
}

func (s *Server) CloseContext(ctx context.Context) error {
	s.closeOnce.Do(func() {
		var fleetErr error
		if s.fleet != nil {
			fleetErr = s.fleet.Close()
		}
		s.closeErr = errors.Join(s.events.Close(), s.sockets.closeAndWait(ctx), fleetErr)
	})
	return s.closeErr
}

func confirmedAccountSessionInvalid(err error) bool {
	var appErr *ipc.Error
	return errors.As(err, &appErr) && appErr.Code == ipc.CodeForbidden
}
