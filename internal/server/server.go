package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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

// SettingStore 是 setting 表的最小读写子集, 由装配层以 *store.Store 注入。
type SettingStore interface {
	SettingGet(ctx context.Context, key string) (string, bool, error)
	SettingSet(ctx context.Context, key, value string) error
	SettingSetManyDelete(ctx context.Context, values map[string]string, deleteKeys ...string) error
	SettingListPrefix(ctx context.Context, prefix string) (map[string]string, error)
}

// AuditFunc 写审计记录; 由装配层桥接到 store.AuditInsert。
type AuditFunc func(ctx context.Context, source, kind string, payload map[string]any) error

// DBHealthSource 是 /healthz 暴露的非秘密数据库元数据来源。
type DBHealthSource interface {
	Backend() store.Backend
	DB() *sql.DB
}

type Config struct {
	Options    Options
	Dispatcher *ipc.Dispatcher
	// PeerDispatcher 是同步对端命令面(资产包三命令), 只在 sync-only 部署挂载到 /sync/rpc 并计入健康。
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
	Static         http.Handler
	Vault          Vault
	VaultStatus    func(context.Context) (any, error)
	Retention      *RetentionConfig
	Fleet          *fleetserver.Service
	// Previews 与 Spectator 一起启用只读远程预览 (公开围观链接): 管理端点
	// 挂在账号路由下, 公开数据面是 GET /share/preview/{token}。
	Previews    *sharing.Service
	Spectator   PreviewSpectator
	DB          DBHealthSource
	Version     string
	MaxRPCBytes int64
	Logger      *slog.Logger
	WebSocket   WebSocketConfig
}

type Server struct {
	options         Options
	dispatcher      *ipc.Dispatcher
	peerDispatcher  *ipc.Dispatcher
	environment     ipc.Environment
	accounts        *account.Accounts
	accountThrottle accountThrottles
	mfaTickets      *mfaTicketStore
	gatewayAuthKey  string
	authRequired    bool
	events          *EventBroker
	channels        ChannelBinder
	channelStats    ChannelStatsFunc
	version         string
	vaultStatus     func(context.Context) (any, error)
	retention       *RetentionConfig
	handler         http.Handler
	logger          *slog.Logger
	sockets         socketTracker
	closeOnce       sync.Once
	closeErr        error
	webSocket       WebSocketConfig
	preferences     *account.Preferences
	audit           AuditFunc
	fleet           *fleetserver.Service
	previews        *sharing.Service
	spectator       PreviewSpectator
	previewResolve  *account.LoginThrottle
	// previewRevalidateInterval 是预览连接的授权复查间隔, 测试可调小。
	previewRevalidateInterval time.Duration
	db                        DBHealthSource

	readGate func()
}

type accountThrottles struct {
	login    *account.LoginThrottle
	recovery *account.LoginThrottle
	init     *account.LoginThrottle
	register *account.LoginThrottle
	enroll   *account.LoginThrottle
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

// DBHealth 是 /healthz 暴露的非秘密数据库元数据: 后端名、ping 延迟与连接池计数。
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
	if config.Dispatcher == nil {
		return nil, fmt.Errorf("RPC dispatcher is required")
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
	// 平台模式下唯一的放行凭据就是网关注入的请求头; 没有配置密钥时任何请求都会被拒,
	// 与其静默锁死, 不如启动即失败。
	if authMode == AuthPlatform && config.GatewayAuthKey == "" {
		return nil, fmt.Errorf("auth mode %q requires a gateway key (NEXTERM_GATEWAY_AUTH); without it no request can be admitted", AuthPlatform)
	}
	config.Options.Auth = authMode
	authRequired := false
	switch authMode {
	case AuthPlatform:
		// 平台模式不区分 syncOnly: 门在网关上, 不在监听地址上。
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
		options: config.Options, dispatcher: config.Dispatcher, peerDispatcher: config.PeerDispatcher, environment: config.Environment,
		accounts: config.Accounts,
		accountThrottle: accountThrottles{
			login: account.NewLoginThrottle(), recovery: account.NewLoginThrottle(),
			init: account.NewLoginThrottle(), register: account.NewLoginThrottle(), enroll: account.NewLoginThrottle(),
			mfa: account.NewLoginThrottle(),
		},
		mfaTickets:     newMFATicketStore(),
		gatewayAuthKey: config.GatewayAuthKey, authRequired: authRequired,
		events: config.Events, channels: config.Channels,
		channelStats: config.ChannelStats, version: config.Version, vaultStatus: config.VaultStatus,
		retention: config.Retention, logger: config.Logger, webSocket: config.WebSocket.withDefaults(),
		audit: config.AuditFunc, fleet: config.Fleet,
		previews: config.Previews, spectator: config.Spectator,
		previewResolve:            account.NewLoginThrottle(),
		previewRevalidateInterval: defaultPreviewRevalidateInterval,
		db:                        config.DB,
	}
	if s.environment.Events == nil {
		s.environment.Events = s.events
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
		mux.Handle("POST /sync/v2/pull", s.accountGuard(s.requireAccountSession(inject(config.SyncObjects))))
		mux.Handle("POST /sync/v2/ids", s.accountGuard(s.requireAccountSession(inject(config.SyncObjects))))
	}
	if s.options.SyncOnly {
		// 对端同步 RPC 面: 仅 digest/export/import 三命令, 与账号 RPC 同一套会话与 CSRF 把关;
		// Host 与 Origin 边界由 transportGuard 统一施加。完整版不挂此面(/rpc 已含三命令)。
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
		// 公开分享页复用同一静态入口: 普通 GET /share/public/{token} 服务
		// 既有 SPA/index, 不另造 shell。
		s.fleet.SetSharePublicPage(staticHandler)
		s.fleet.Mount(mux)
	}
	if !s.options.SyncOnly && s.previews != nil && s.spectator != nil {
		// 只读远程预览的公开数据面: 普通 GET 与公开分享页同一静态入口,
		// WS upgrade 才走 token 把关。
		mux.HandleFunc("GET /share/preview/{token}", s.serveSharePreview(staticHandler))
	}

	rpcHandler := ipc.NewRPCHandler(s.dispatcher, s.environment)
	rpcHandler.MaxBytes = config.MaxRPCBytes
	mux.Handle("POST /rpc", s.requireAuth(rpcHandler))
	mux.HandleFunc("GET /ws/events", s.serveEvents)
	mux.HandleFunc("GET /ws/channel/{id}", s.serveChannel)

	blobs := config.Blobs
	if blobs == nil && s.options.DataDir != "" {
		blobs = NewBlobStore(s.options.DataDir, s.logger)
	}
	if blobs != nil {
		// 存在任何无身份放行路径 (auth=off/回环监听/网关) 时属主概念不成立,
		// 暂存 blob 放开无属主条目; 仅当所有放行请求都带账号会话身份时保持属主判等。
		blobs.openAccess = !(s.authRequired && s.accounts != nil && s.gatewayAuthKey == "")
		mux.Handle("POST /files/blob", s.requireAuth(http.HandlerFunc(blobs.Stage)))
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
		// sync-only 只挂对端命令面, 健康如实上报其真实命令数(未挂载时为 0)。
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
