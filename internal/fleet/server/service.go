package fleetserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/sharing"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// forUpdate 返回行锁子句: PostgreSQL 默认 READ COMMITTED, enroll 首台判定必须
// 锁住 app_user 行才能与并发的同账号 enroll 互斥; SQLite 单写者天然串行。
func forUpdate(backend store.Backend) string {
	if backend == store.BackendPostgres {
		return " FOR UPDATE"
	}
	return ""
}

const (
	PurposeDeviceAgent       = "device-agent"
	agentDeviceKind          = "agent"
	DefaultMetricsIntervalMS = 60000

	baseURLsSettingKey = "fleet.base_urls"
	maxBaseURLs        = 8
	maxBaseURLLength   = 2048

	maxAgentNameLength    = 128
	maxPlatformLength     = 32
	maxAppVersionLength   = 64
	enrollCodeBytes       = 32
	credentialSecretBytes = 32
	minEnrollCodeTTL      = time.Minute
	maxEnrollCodeTTL      = time.Hour

	rawMetricsRetention    = 48 * time.Hour
	hourlyMetricsRetention = 30 * 24 * time.Hour
	metricsHourMS          = 3600 * 1000
	maxMetricsSamples      = 5000

	auditSourceFleet = "fleet"
)

const (
	auditKindDeviceEnroll    = "device_enroll"
	auditKindDeviceRevoke    = "device_revoke"
	auditKindDeviceApprove   = "device_approve"
	auditKindDeviceReject    = "device_reject"
	auditKindDeviceAutostart = "device_autostart"
	auditKindDeviceFailover  = "device_failover"
	auditKindDeviceTerminal  = "device_terminal"
	auditKindDeviceOnline    = "device_online"
	auditKindDeviceOffline   = "device_offline"
)

// 设备审批状态机: 凭接入码接入默认 pending (首台设备除外, 避免无人能批准的死锁),
// 超管批准后 active 才允许同步数据; 拒绝即删除 pending 设备。
const (
	DeviceStatePending = "pending"
	DeviceStateActive  = "active"
)

type Config struct {
	DB       *sql.DB
	Backend  store.Backend
	Accounts *account.Accounts
	AuthOff  bool
	// Events 是可选的事件出口 (EventBroker); nil 时上下线只落审计不推送。
	Events ipc.Emitter
}

type Service struct {
	db         *sql.DB
	backend    store.Backend
	accounts   *account.Accounts
	authOff    bool
	now        func() int64
	enrollGate *account.LoginThrottle
	// shareResolveGate 限流匿名公开链接 resolve, 随机 token 洪水不能刷爆 audit_log。
	shareResolveGate *account.LoginThrottle
	registry         *Registry
	sharing          *sharing.Service
	events           ipc.Emitter

	// presenceMu 保护 onlineDevices: 设备上下线事件与审计只在真实状态迁移时
	// 产生一次, 顶替重连 (旧连接注销时已被顶替) 与服务关停 (Close 换空表) 不会
	// 刷出重复或额外事件。
	presenceMu    sync.Mutex
	onlineDevices map[string]bool

	sharePublicPage         http.Handler
	shareRevalidateInterval time.Duration
}

type Option func(*Service)

func WithNow(now func() int64) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// WithEvents 覆盖事件出口 (装配层传 EventBroker); nil 表示只落审计不推送。
func WithEvents(events ipc.Emitter) Option {
	return func(s *Service) {
		s.events = events
	}
}

// WithShareRevalidateInterval 覆盖空闲分享连接的周期复查间隔 (测试用小间隔
// 加速吊销/过期生效; 生产默认 defaultShareRevalidateInterval)。
func WithShareRevalidateInterval(interval time.Duration) Option {
	return func(s *Service) {
		if interval > 0 {
			s.shareRevalidateInterval = interval
		}
	}
}

// SetSharePublicPage 注入公开分享页的 SPA 静态入口 (装配层把配置好的 static
// handler 传入): 普通 GET /share/public/{token} 由它直接服务页面, 不做 token
// 校验/审计。nil 表示未配置静态入口, 普通 GET 一律 404。
func (s *Service) SetSharePublicPage(handler http.Handler) {
	s.sharePublicPage = handler
}

func New(config Config, options ...Option) (*Service, error) {
	if config.DB == nil {
		return nil, fmt.Errorf("fleet: DB is required")
	}
	if config.Accounts == nil {
		return nil, fmt.Errorf("fleet: account service is required")
	}
	s := &Service{
		db:               config.DB,
		backend:          config.Backend,
		accounts:         config.Accounts,
		authOff:          config.AuthOff,
		now:              ids.NowMS,
		enrollGate:       account.NewLoginThrottle(),
		shareResolveGate: account.NewLoginThrottle(),
		registry:         NewRegistry(),
		events:           config.Events,
		onlineDevices:    make(map[string]bool),

		shareRevalidateInterval: defaultShareRevalidateInterval,
	}
	sharingService, err := sharing.New(sharing.Config{DB: config.DB, Accounts: config.Accounts},
		sharing.WithAgentProbe(func(ctx context.Context, deviceID string) error {
			if !s.registry.AgentOnline(deviceID) {
				return ipc.NewError(ipc.CodeDisconnected, "设备代理离线")
			}
			return nil
		}))
	if err != nil {
		return nil, err
	}
	s.sharing = sharingService
	for _, option := range options {
		option(s)
	}
	return s, nil
}

func dbError(err error) error {
	return ipc.WrapError(ipc.CodeDB, "数据库错误: "+err.Error(), err)
}

func hashSecret(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(digest[:])
}

func randomSecret(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", ipc.WrapError(ipc.CodeCrypto, "加密错误: 随机密钥生成失败", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

type BaseURLEntry struct {
	URL      string `json:"url"`
	Insecure bool   `json:"insecure,omitempty"`
}

type ServiceState struct {
	Installed       bool   `json:"installed"`
	Enabled         bool   `json:"enabled"`
	Active          bool   `json:"active"`
	LastReconcileAt int64  `json:"last_reconcile_at,omitempty"`
	LastError       string `json:"last_error,omitempty"`
}

type Owner struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type AgentInfo struct {
	Platform         string       `json:"platform"`
	AppVersion       string       `json:"app_version"`
	DesiredAutostart bool         `json:"desired_autostart"`
	TerminalEnabled  bool         `json:"terminal_enabled"`
	CurrentURL       string       `json:"current_url"`
	ServiceState     ServiceState `json:"service_state"`
	LastSeenAt       int64        `json:"last_seen_at"`
}

type Device struct {
	ID         string
	Name       string
	Kind       string
	State      string
	CreatedAt  int64
	LastSeenAt int64
	RevokedAt  int64
	Owner      *Owner
	Agent      *AgentInfo
}

type MetricsSample struct {
	TS        int64   `json:"ts"`
	CPUPct    float64 `json:"cpu_pct"`
	MemUsed   int64   `json:"mem_used"`
	MemTotal  int64   `json:"mem_total"`
	DiskUsed  int64   `json:"disk_used"`
	DiskTotal int64   `json:"disk_total"`
	UptimeS   int64   `json:"uptime_s"`
}

type EnrollResult struct {
	DeviceID          string
	Secret            string
	State             string
	BaseURLs          []BaseURLEntry
	MetricsIntervalMS int64
	DesiredAutostart  bool
	TerminalEnabled   bool
}

type auditPayload struct {
	Action    string `json:"action"`
	DeviceID  string `json:"device_id"`
	Requester string `json:"requester"`
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason,omitempty"`
	URL       string `json:"url,omitempty"`
}

// audit 写 fleet 审计; payload 带设备 ID 时同步落 asset_id 列, 审计视图即可按
// 设备过滤出完整时间线 (接入/上线/离线/吊销/切换接入地址), 无需解析 payload_json。
func (s *Service) audit(ctx context.Context, kind string, payload auditPayload) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
	}
	var assetID any
	if payload.DeviceID != "" {
		assetID = payload.DeviceID
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO audit_log(ts, session_id, asset_id, source, kind, payload_json, exit_code, duration_ms)
VALUES(?,NULL,?,?,?,?,NULL,NULL)`, s.now(), assetID, auditSourceFleet, kind, string(encoded)); err != nil {
		return dbError(err)
	}
	return nil
}

// deviceOnline/deviceOffline 是设备上下线的唯一出口: 只在真实状态迁移时写审计并
// 推送 device://status, 重复调用 (顶替重连的注销/注册交错) 直接忽略。
// 调用方可能已持有取消的 ctx (控制通道断开路径), 这里统一用后台 ctx 完成落库与推送。
func (s *Service) deviceOnline(deviceID string) {
	s.transitionPresence(deviceID, true)
}

func (s *Service) deviceOffline(deviceID string) {
	s.transitionPresence(deviceID, false)
}

func (s *Service) transitionPresence(deviceID string, online bool) {
	s.presenceMu.Lock()
	if s.onlineDevices[deviceID] == online {
		s.presenceMu.Unlock()
		return
	}
	if online {
		s.onlineDevices[deviceID] = true
	} else {
		delete(s.onlineDevices, deviceID)
	}
	s.presenceMu.Unlock()

	kind, action := auditKindDeviceOnline, "online"
	if !online {
		kind, action = auditKindDeviceOffline, "offline"
	}
	ctx := context.Background()
	_ = s.audit(ctx, kind, auditPayload{Action: action, DeviceID: deviceID, Requester: deviceID, Outcome: "allow"})
	if s.events != nil {
		_ = ipc.Emit(ctx, s.events, ipc.TopicDeviceStatus, ipc.DeviceStatusEvent{DeviceID: deviceID, Online: online})
	}
}

type deviceGrant struct {
	deviceID string
	userID   string
	state    string
	revoked  bool
}

func (s *Service) loadGrant(ctx context.Context, deviceID string) (*deviceGrant, error) {
	var grant deviceGrant
	var revokedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT user_id, state, revoked_at FROM user_device WHERE id = ?", deviceID).
		Scan(&grant.userID, &grant.state, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ipc.NewError(ipc.CodeNotFound, "设备不存在")
	}
	if err != nil {
		return nil, dbError(err)
	}
	grant.deviceID = deviceID
	grant.revoked = revokedAt.Valid
	return &grant, nil
}

// authorize 是 fleet 设备动作的唯一授权点: superadmin 或设备 owner 放行,
// 已吊销设备一律拒绝; 每次判定 (放行或拒绝) 都写审计。
func (s *Service) authorize(ctx context.Context, identity *account.Identity, deviceID, action, kind string) (*deviceGrant, error) {
	grant, err := s.loadGrant(ctx, deviceID)
	outcome, reason := "allow", ""
	var decision error
	switch {
	case err != nil:
		outcome, reason, decision = "deny", "not_found", err
	case grant.revoked:
		outcome, reason, decision = "deny", "device_revoked", ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	case identity.Role != account.RoleSuperadmin && grant.userID != identity.UserID:
		outcome, reason, decision = "deny", "not_owner", ipc.NewError(ipc.CodeForbidden, "设备不属于该用户")
	}
	auditErr := s.audit(ctx, kind, auditPayload{Action: action, DeviceID: deviceID, Requester: identity.UserID, Outcome: outcome, Reason: reason})
	if decision != nil {
		return nil, decision
	}
	if auditErr != nil {
		return nil, auditErr
	}
	return grant, nil
}

// canAccess 与 authorize 同一套 owner-or-superadmin 规则, 供只读路径使用, 不写审计。
func (s *Service) canAccess(ctx context.Context, identity *account.Identity, deviceID string) (*deviceGrant, error) {
	grant, err := s.loadGrant(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	if grant.revoked {
		return nil, ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	}
	if identity.Role != account.RoleSuperadmin && grant.userID != identity.UserID {
		return nil, ipc.NewError(ipc.CodeForbidden, "设备不属于该用户")
	}
	return grant, nil
}

// DeviceOwnerID 返回设备归属用户 ID, 供事件出口按 owner 过滤 device://status
// 推送; 设备不存在时返回错误。
func (s *Service) DeviceOwnerID(ctx context.Context, deviceID string) (string, error) {
	grant, err := s.loadGrant(ctx, deviceID)
	if err != nil {
		return "", err
	}
	return grant.userID, nil
}

func (s *Service) IssueEnrollCode(ctx context.Context, identity *account.Identity, targetUserID string, ttl time.Duration) (string, int64, error) {
	ownerID := identity.UserID
	if targetUserID != "" && targetUserID != identity.UserID {
		if identity.Role != account.RoleSuperadmin {
			return "", 0, ipc.NewError(ipc.CodeForbidden, "需要超管权限")
		}
		ownerID = targetUserID
	}
	if _, err := s.accounts.GetUser(ctx, ownerID); err != nil {
		return "", 0, err
	}
	if ttl == 0 {
		ttl = account.EnrollCodeTTL
	}
	if ttl < minEnrollCodeTTL || ttl > maxEnrollCodeTTL {
		return "", 0, ipc.BadParam(fmt.Errorf("设备注册码有效期需在 %s-%s 之间", minEnrollCodeTTL, maxEnrollCodeTTL))
	}
	code, err := randomSecret(enrollCodeBytes)
	if err != nil {
		return "", 0, err
	}
	now := s.now()
	expiresAt := now + ttl.Milliseconds()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO device_enroll_code(id, user_id, code_hash, created_at, expires_at)
VALUES(?,?,?,?,?)`, ids.New(), ownerID, hashSecret(code), now, expiresAt); err != nil {
		return "", 0, dbError(err)
	}
	return code, expiresAt, nil
}

func (s *Service) ConsumeEnrollCode(ctx context.Context, code, name, platform, appVersion string) (*EnrollResult, error) {
	if name == "" || len(name) > maxAgentNameLength {
		return nil, ipc.BadParam(fmt.Errorf("设备名长度需在 1-%d 之间", maxAgentNameLength))
	}
	if len(platform) > maxPlatformLength {
		return nil, ipc.BadParam(fmt.Errorf("平台长度需在 0-%d 之间", maxPlatformLength))
	}
	if len(appVersion) > maxAppVersionLength {
		return nil, ipc.BadParam(fmt.Errorf("版本号长度需在 0-%d 之间", maxAppVersionLength))
	}
	baseURLs, err := s.BaseURLs(ctx)
	if err != nil {
		return nil, err
	}
	secret, err := randomSecret(credentialSecretBytes)
	if err != nil {
		return nil, err
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var ownerID string
	err = tx.QueryRowContext(ctx, `UPDATE device_enroll_code SET consumed_at = ?
WHERE code_hash = ? AND consumed_at IS NULL AND expires_at > ?
RETURNING user_id`, now, hashSecret(code), now).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ipc.NewError(ipc.CodeForbidden, "设备注册码无效或已过期，请重新生成")
	}
	if err != nil {
		return nil, dbError(err)
	}
	var lockedOwner string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM app_user WHERE id = ?"+forUpdate(s.backend), ownerID).Scan(&lockedOwner); err != nil {
		return nil, dbError(err)
	}
	var deviceCount int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM user_device WHERE user_id = ?", ownerID).Scan(&deviceCount); err != nil {
		return nil, dbError(err)
	}
	state := DeviceStatePending
	if deviceCount == 0 {
		state = DeviceStateActive
	}
	deviceID := ids.New()
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_device(id, user_id, name, kind, state, created_at)
VALUES(?,?,?,?,?,?)`, deviceID, ownerID, name, agentDeviceKind, state, now); err != nil {
		return nil, dbError(err)
	}
	credentialID := ids.New()
	if _, err := tx.ExecContext(ctx, `INSERT INTO sync_credential(id, user_id, device_id, purpose, secret_hash, created_at, expires_at)
VALUES(?,?,?,?,?,?,0)`, credentialID, ownerID, deviceID, PurposeDeviceAgent, hashSecret(secret), now); err != nil {
		return nil, dbError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO device_agent(device_id, credential_id, platform, app_version, created_at)
VALUES(?,?,?,?,?)`, deviceID, credentialID, platform, appVersion, now); err != nil {
		return nil, dbError(err)
	}
	payload, err := json.Marshal(auditPayload{Action: "enroll", DeviceID: deviceID, Requester: ownerID, Outcome: "allow"})
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log(ts, session_id, asset_id, source, kind, payload_json, exit_code, duration_ms)
VALUES(?,NULL,?,?,?,?,NULL,NULL)`, now, deviceID, auditSourceFleet, auditKindDeviceEnroll, string(payload)); err != nil {
		return nil, dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, dbError(err)
	}
	return &EnrollResult{
		DeviceID:          deviceID,
		Secret:            secret,
		State:             state,
		BaseURLs:          baseURLs,
		MetricsIntervalMS: DefaultMetricsIntervalMS,
		DesiredAutostart:  true,
		TerminalEnabled:   true,
	}, nil
}

// AuthenticateAgent 校验设备凭证三元组绑定 (device_id + secret + purpose),
// 凭证与设备任一吊销或过期即拒绝; 待审批设备得到 device_pending (可恢复,
// 不碰心跳); 通过后回写 last_used_at/last_seen_at。
func (s *Service) AuthenticateAgent(ctx context.Context, deviceID, secret string) (string, error) {
	var userID, boundDeviceID, deviceState string
	var expiresAt int64
	var credentialRevokedAt, deviceRevokedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT c.user_id, c.device_id, c.expires_at, c.revoked_at, d.revoked_at, d.state
FROM sync_credential c JOIN user_device d ON d.id = c.device_id
WHERE c.secret_hash = ? AND c.purpose = ?`, hashSecret(secret), PurposeDeviceAgent).
		Scan(&userID, &boundDeviceID, &expiresAt, &credentialRevokedAt, &deviceRevokedAt, &deviceState)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ipc.NewError(ipc.CodeForbidden, "设备凭证无效")
	}
	if err != nil {
		return "", dbError(err)
	}
	switch {
	case boundDeviceID != deviceID:
		return "", ipc.NewError(ipc.CodeForbidden, "设备凭证与设备不匹配")
	case credentialRevokedAt.Valid:
		return "", ipc.NewError(ipc.CodeForbidden, "设备凭证已吊销")
	case expiresAt > 0 && s.now() >= expiresAt:
		return "", ipc.NewError(ipc.CodeForbidden, "设备凭证已过期")
	case deviceRevokedAt.Valid:
		return "", ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	case deviceState == DeviceStatePending:
		return "", ipc.NewError(ipc.CodeDevicePending, "设备待审批: 管理员批准后才会上线并同步数据")
	}
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `UPDATE sync_credential SET last_used_at = ? WHERE device_id = ? AND purpose = ?`,
		now, deviceID, PurposeDeviceAgent); err != nil {
		return "", dbError(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE user_device SET last_seen_at = ? WHERE id = ?", now, deviceID); err != nil {
		return "", dbError(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE device_agent SET last_seen_at = ? WHERE device_id = ?", now, deviceID); err != nil {
		return "", dbError(err)
	}
	return userID, nil
}

func (s *Service) ListDevices(ctx context.Context, identity *account.Identity) ([]*Device, error) {
	query := `SELECT d.id, d.user_id, d.name, d.kind, d.state, d.created_at, d.last_seen_at, d.revoked_at,
u.username, a.platform, a.app_version, a.desired_autostart, a.terminal_enabled, a.current_url, a.service_state_json, a.last_seen_at
FROM user_device d
LEFT JOIN app_user u ON u.id = d.user_id
LEFT JOIN device_agent a ON a.device_id = d.id`
	args := []any{}
	if identity.Role != account.RoleSuperadmin {
		query += " WHERE d.user_id = ?"
		args = append(args, identity.UserID)
	}
	query += " ORDER BY d.created_at, d.id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	var devices []*Device
	for rows.Next() {
		var device Device
		var ownerID, ownerName string
		var lastSeenAt, revokedAt, agentLastSeenAt sql.NullInt64
		var platform, appVersion, currentURL, serviceStateJSON sql.NullString
		var desiredAutostart, terminalEnabled sql.NullInt64
		if err := rows.Scan(&device.ID, &ownerID, &device.Name, &device.Kind, &device.State, &device.CreatedAt, &lastSeenAt, &revokedAt,
			&ownerName, &platform, &appVersion, &desiredAutostart, &terminalEnabled, &currentURL, &serviceStateJSON, &agentLastSeenAt); err != nil {
			return nil, dbError(err)
		}
		if lastSeenAt.Valid {
			device.LastSeenAt = lastSeenAt.Int64
		}
		if revokedAt.Valid {
			device.RevokedAt = revokedAt.Int64
		}
		if identity.Role == account.RoleSuperadmin {
			device.Owner = &Owner{ID: ownerID, Username: ownerName}
		}
		if platform.Valid {
			agent := &AgentInfo{
				Platform:         platform.String,
				AppVersion:       appVersion.String,
				DesiredAutostart: desiredAutostart.Int64 != 0,
				TerminalEnabled:  terminalEnabled.Int64 != 0,
				CurrentURL:       currentURL.String,
			}
			if agentLastSeenAt.Valid {
				agent.LastSeenAt = agentLastSeenAt.Int64
			}
			if serviceStateJSON.Valid && serviceStateJSON.String != "" {
				if err := json.Unmarshal([]byte(serviceStateJSON.String), &agent.ServiceState); err != nil {
					return nil, dbError(err)
				}
			}
			device.Agent = agent
		}
		devices = append(devices, &device)
	}
	return devices, rows.Err()
}

func (s *Service) RevokeDevice(ctx context.Context, identity *account.Identity, deviceID string) error {
	if _, err := s.authorize(ctx, identity, deviceID, "revoke", auditKindDeviceRevoke); err != nil {
		return err
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "UPDATE user_device SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", now, deviceID); err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE user_session SET revoked_at = ? WHERE device_id = ? AND revoked_at IS NULL", now, deviceID); err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE sync_credential SET revoked_at = ? WHERE device_id = ? AND revoked_at IS NULL", now, deviceID); err != nil {
		return dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	s.registry.KickDevice(deviceID)
	// 吊销即掉线: 控制通道被踢后不会再有注销回调 (KickDevice 已摘表),
	// 这里补一次离线迁移, 让审计时间线与 device://status 推送保持完整。
	s.deviceOffline(deviceID)
	return nil
}

// authorizePending 是审批/拒绝的授权点: 仅超管, 设备须为未吊销的 pending;
// 每次判定 (放行或拒绝) 都写审计。
func (s *Service) authorizePending(ctx context.Context, identity *account.Identity, deviceID, action, kind string) (*deviceGrant, error) {
	grant, err := s.loadGrant(ctx, deviceID)
	outcome, reason := "allow", ""
	var decision error
	switch {
	case err != nil:
		outcome, reason, decision = "deny", "not_found", err
	case identity.Role != account.RoleSuperadmin:
		outcome, reason, decision = "deny", "not_superadmin", ipc.NewError(ipc.CodeForbidden, "需要超管权限")
	case grant.revoked:
		outcome, reason, decision = "deny", "device_revoked", ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	case grant.state != DeviceStatePending:
		outcome, reason, decision = "deny", "not_pending", ipc.NewError(ipc.CodeBadParam, "设备不在待审批状态")
	}
	auditErr := s.audit(ctx, kind, auditPayload{Action: action, DeviceID: deviceID, Requester: identity.UserID, Outcome: outcome, Reason: reason})
	if decision != nil {
		return nil, decision
	}
	if auditErr != nil {
		return nil, auditErr
	}
	return grant, nil
}

// ApproveDevice 批准待审批设备: state 置 active, 设备下次鉴权即放行上线。
func (s *Service) ApproveDevice(ctx context.Context, identity *account.Identity, deviceID string) error {
	if _, err := s.authorizePending(ctx, identity, deviceID, "approve", auditKindDeviceApprove); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE user_device SET state = ? WHERE id = ? AND state = ?", DeviceStateActive, deviceID, DeviceStatePending); err != nil {
		return dbError(err)
	}
	return nil
}

// RejectDevice 拒绝待审批设备: 删除设备行, 凭证/agent/指标经外键级联一并清除,
// 设备下次鉴权得到凭证无效, 按合同视同吊销停止。
func (s *Service) RejectDevice(ctx context.Context, identity *account.Identity, deviceID string) error {
	if _, err := s.authorizePending(ctx, identity, deviceID, "reject", auditKindDeviceReject); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM user_device WHERE id = ? AND state = ?", deviceID, DeviceStatePending); err != nil {
		return dbError(err)
	}
	return nil
}

// AgentDesiredConfig 返回服务端期望的 agent 配置; metrics 间隔是产品固定值
// (schema 无按设备列), 与 enroll 下发保持一致。
func (s *Service) AgentDesiredConfig(ctx context.Context, deviceID string) (AgentConfig, error) {
	var desiredAutostart, terminalEnabled int64
	err := s.db.QueryRowContext(ctx, "SELECT desired_autostart, terminal_enabled FROM device_agent WHERE device_id = ?", deviceID).
		Scan(&desiredAutostart, &terminalEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentConfig{}, ipc.NewError(ipc.CodeNotFound, "设备代理不存在")
	}
	if err != nil {
		return AgentConfig{}, dbError(err)
	}
	return AgentConfig{
		DesiredAutostart:  desiredAutostart != 0,
		MetricsIntervalMS: DefaultMetricsIntervalMS,
		TerminalEnabled:   terminalEnabled != 0,
	}, nil
}

// AgentConfig 是服务端期望的 agent 运行配置。
type AgentConfig struct {
	DesiredAutostart  bool
	MetricsIntervalMS int64
	TerminalEnabled   bool
}

func (s *Service) SetDesiredAutostart(ctx context.Context, identity *account.Identity, deviceID string, desired bool) error {
	if _, err := s.authorize(ctx, identity, deviceID, "autostart", auditKindDeviceAutostart); err != nil {
		return err
	}
	value := 0
	if desired {
		value = 1
	}
	result, err := s.db.ExecContext(ctx, "UPDATE device_agent SET desired_autostart = ? WHERE device_id = ?", value, deviceID)
	if err != nil {
		return dbError(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected == 0 {
		return ipc.NewError(ipc.CodeNotFound, "设备代理不存在")
	}
	if updated, err := s.AgentDesiredConfig(ctx, deviceID); err == nil {
		s.registry.PushControl(deviceID, controlMessage{
			Type:              "config",
			DesiredAutostart:  &updated.DesiredAutostart,
			MetricsIntervalMS: &updated.MetricsIntervalMS,
			TerminalEnabled:   &updated.TerminalEnabled,
		})
	}
	return nil
}

func (s *Service) BaseURLs(ctx context.Context) ([]BaseURLEntry, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM setting WHERE key = ?", baseURLsSettingKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return []BaseURLEntry{}, nil
	}
	if err != nil {
		return nil, dbError(err)
	}
	var entries []BaseURLEntry
	if err := json.Unmarshal([]byte(value), &entries); err != nil {
		return nil, dbError(err)
	}
	return entries, nil
}

func (s *Service) SetBaseURLs(ctx context.Context, entries []BaseURLEntry) ([]BaseURLEntry, error) {
	if len(entries) > maxBaseURLs {
		return nil, ipc.BadParam(fmt.Errorf("接入地址最多 %d 个", maxBaseURLs))
	}
	normalized := make([]BaseURLEntry, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		entry, err := normalizeBaseURL(entry)
		if err != nil {
			return nil, err
		}
		if seen[entry.URL] {
			continue
		}
		seen[entry.URL] = true
		normalized = append(normalized, entry)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		baseURLsSettingKey, string(encoded), s.now()); err != nil {
		return nil, dbError(err)
	}
	return normalized, nil
}

func normalizeBaseURL(entry BaseURLEntry) (BaseURLEntry, error) {
	raw := strings.TrimSpace(entry.URL)
	if raw == "" || len(raw) > maxBaseURLLength {
		return BaseURLEntry{}, ipc.BadParam(fmt.Errorf("接入地址长度需在 1-%d 之间", maxBaseURLLength))
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return BaseURLEntry{}, ipc.WrapError(ipc.CodeBadParam, "接入地址 URL 不合法", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Host == "" {
		return BaseURLEntry{}, ipc.NewError(ipc.CodeBadParam, "接入地址必须显式使用 http:// 或 https://，并包含主机名")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return BaseURLEntry{}, ipc.NewError(ipc.CodeBadParam, "接入地址不能包含用户信息、查询参数或片段")
	}
	parsed.Scheme = scheme
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	if scheme == "http" && !entry.Insecure && !isLocalOrPrivate(parsed.Hostname()) {
		return BaseURLEntry{}, ipc.NewError(ipc.CodeForbidden, "公网地址必须使用 HTTPS；设备凭证不能通过明文 HTTP 传输")
	}
	entry.URL = strings.TrimRight(parsed.String(), "/")
	return entry, nil
}

func isLocalOrPrivate(hostname string) bool {
	hostname = strings.ToLower(strings.TrimSuffix(hostname, "."))
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") || strings.HasSuffix(hostname, ".local") {
		return true
	}
	address, err := netip.ParseAddr(hostname)
	if err != nil {
		return false
	}
	address = address.Unmap()
	return address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast()
}

func (s *Service) RecordMetrics(ctx context.Context, deviceID string, sample MetricsSample) error {
	var revokedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT revoked_at FROM user_device WHERE id = ?", deviceID).Scan(&revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ipc.NewError(ipc.CodeNotFound, "设备不存在")
	}
	if err != nil {
		return dbError(err)
	}
	if revokedAt.Valid {
		return ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	}
	if sample.TS == 0 {
		sample.TS = s.now()
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO device_metrics(id, device_id, ts, cpu_pct, mem_used, mem_total, disk_used, disk_total, uptime_s)
VALUES(?,?,?,?,?,?,?,?,?)`, ids.New(), deviceID, sample.TS, sample.CPUPct, sample.MemUsed, sample.MemTotal,
		sample.DiskUsed, sample.DiskTotal, sample.UptimeS); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Service) DeviceMetrics(ctx context.Context, identity *account.Identity, deviceID string, since int64) ([]*MetricsSample, error) {
	if _, err := s.canAccess(ctx, identity, deviceID); err != nil {
		return nil, err
	}
	now := s.now()
	oldest := now - rawMetricsRetention.Milliseconds()
	if since <= 0 {
		since = now - 24*time.Hour.Milliseconds()
	}
	if since < oldest {
		since = oldest
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ts, cpu_pct, mem_used, mem_total, disk_used, disk_total, uptime_s
FROM device_metrics WHERE device_id = ? AND ts >= ? ORDER BY ts LIMIT ?`, deviceID, since, maxMetricsSamples)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	samples := []*MetricsSample{}
	for rows.Next() {
		var sample MetricsSample
		if err := rows.Scan(&sample.TS, &sample.CPUPct, &sample.MemUsed, &sample.MemTotal, &sample.DiskUsed, &sample.DiskTotal, &sample.UptimeS); err != nil {
			return nil, dbError(err)
		}
		samples = append(samples, &sample)
	}
	return samples, rows.Err()
}

// RollupMetrics 把超过原始保留期 (48h) 的指标聚合成小时桶 (保留 30d),
// 再清理过期的原始行与小时桶; 重复执行结果一致。
// 只聚合完整小时桶: cutoff 向下对齐到小时边界, 聚合与原始行删除共享同一 cutoff,
// 边界桶保留到下一轮整体处理, 跨批次不会整桶覆盖丢失样本。
func (s *Service) RollupMetrics(ctx context.Context) error {
	now := s.now()
	rawCutoff := now - rawMetricsRetention.Milliseconds()
	rollupCutoff := (rawCutoff / metricsHourMS) * metricsHourMS
	hourlyCutoff := now - hourlyMetricsRetention.Milliseconds()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO device_metrics_hourly(device_id, bucket_ts, cpu_pct, mem_used, mem_total, disk_used, disk_total, uptime_s, sample_count)
SELECT device_id, (ts/?)*?, avg(cpu_pct), avg(mem_used), max(mem_total), avg(disk_used), max(disk_total), max(uptime_s), count(*)
FROM device_metrics WHERE ts < ?
GROUP BY device_id, (ts/?)*?
ON CONFLICT(device_id, bucket_ts) DO UPDATE SET
cpu_pct = excluded.cpu_pct, mem_used = excluded.mem_used, mem_total = excluded.mem_total,
disk_used = excluded.disk_used, disk_total = excluded.disk_total, uptime_s = excluded.uptime_s,
sample_count = excluded.sample_count`,
		metricsHourMS, metricsHourMS, rollupCutoff, metricsHourMS, metricsHourMS); err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM device_metrics WHERE ts < ?", rollupCutoff); err != nil {
		return dbError(err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM device_metrics_hourly WHERE bucket_ts < ?", hourlyCutoff); err != nil {
		return dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

// UpdateServiceState 回写 agent 上报的服务实际状态 (installed/enabled/active)。
func (s *Service) UpdateServiceState(ctx context.Context, deviceID string, state ServiceState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
	}
	result, err := s.db.ExecContext(ctx, "UPDATE device_agent SET service_state_json = ? WHERE device_id = ?", string(encoded), deviceID)
	if err != nil {
		return dbError(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected == 0 {
		return ipc.NewError(ipc.CodeNotFound, "设备代理不存在")
	}
	return nil
}

// SetCurrentURL 记录 agent 切换后的当前接入地址并写 failover 审计。
func (s *Service) SetCurrentURL(ctx context.Context, deviceID, url, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, "UPDATE device_agent SET current_url = ? WHERE device_id = ?", url, deviceID)
	if err != nil {
		return dbError(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected == 0 {
		return ipc.NewError(ipc.CodeNotFound, "设备代理不存在")
	}
	payload, err := json.Marshal(auditPayload{Action: "failover", DeviceID: deviceID, Requester: deviceID, Outcome: "allow", Reason: reason, URL: url})
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log(ts, session_id, asset_id, source, kind, payload_json, exit_code, duration_ms)
VALUES(?,NULL,?,?,?,?,NULL,NULL)`, s.now(), deviceID, auditSourceFleet, auditKindDeviceFailover, string(payload)); err != nil {
		return dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}

// Close 断开全部设备控制通道与桥接 (服务关停)。
func (s *Service) Close() error {
	s.registry.Close()
	return nil
}
