// Package sharing implements SHARE126 host sharing.
//
// Public share links grant time-boxed access to one live terminal session on
// a daemon device: read-only unless the owner explicitly opts into write,
// revocable, expiring, with every access and input audited. Device share
// links do the same for the whole device: an anonymous visitor holding the
// link opens a fresh terminal through the host agent at any time while the
// link is valid, instead of attaching to one existing session. Registered-
// user host shares let a recipient open new terminals through the host agent
// at any time while the share is valid. All kinds bind owner, device,
// permission and expiry, and none ever reads or returns stored host
// passwords, private keys or sync payloads: the package touches no
// credential, asset or sync table, and only SHA-256 token hashes are stored.
//
// Grants are snapshots, not authority: data paths must be built through
// NewLinkGate / NewHostGate, whose per-chunk rechecks revalidate current
// rows (including device ownership and revocation) and swap the refreshed
// Grant into every subsequent decision, so permission shrink and
// revocation stop input immediately and stop output once no valid row
// remains.
//
// Server-side HTTP mounting and fleet route integration are a separate
// slice; this package is verified with explicit fakes. Relay-capable hosts
// are not modeled yet, so both share kinds currently require a
// daemon-equipped device (a device_agent row).
package sharing

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
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

const (
	auditSourceSharing = "sharing"

	linkTokenBytes = 32

	defaultLinkTTL      = time.Hour
	defaultHostShareTTL = 24 * time.Hour
	// defaultDeviceShareTTL 是设备公开链接的默认有效期: 设备级分享面向更长
	// 的协作窗口, 与注册主机分享的默认对齐。
	defaultDeviceShareTTL = 24 * time.Hour
	minShareTTL           = time.Minute
	maxShareTTL           = 30 * 24 * time.Hour

	defaultAgentStaleness = 5 * time.Minute
)

const (
	auditKindLinkCreate   = "share_link_create"
	auditKindLinkRevoke   = "share_link_revoke"
	auditKindLinkAccess   = "share_link_access"
	auditKindLinkInput    = "share_link_input"
	auditKindHostCreate   = "host_share_create"
	auditKindHostRevoke   = "host_share_revoke"
	auditKindHostTerminal = "host_share_terminal"

	auditKindDeviceLinkCreate = "device_share_link_create"
	auditKindDeviceLinkRevoke = "device_share_link_revoke"
	auditKindDeviceLinkAccess = "device_share_link_access"
	auditKindDeviceLinkInput  = "device_share_link_input"
)

type Permission string

const (
	PermissionRead      Permission = "read"
	PermissionReadWrite Permission = "read_write"
)

func (p Permission) AllowsWrite() bool { return p == PermissionReadWrite }

// ErrTokenNotFound 表示公开链接 token 不存在; HTTP 入口据此把随机 token 尝试
// 计入限流退避, 其余拒绝原因 (吊销/过期/未生效/设备离线) 不惩罚链接持有者。
var ErrTokenNotFound = ipc.NewError(ipc.CodeForbidden, "分享链接无效")

func permissionFor(write bool) Permission {
	if write {
		return PermissionReadWrite
	}
	return PermissionRead
}

type Config struct {
	DB       *sql.DB
	Accounts *account.Accounts
}

// AgentProbeFunc 探测设备守护代理当前是否在线 (例如控制通道是否已注册);
// 返回错误即视为离线。requireDaemon 在数据库 last_seen 检查通过后调用,
// 用于把 "进程已断开但 last_seen 未过 staleness 窗口" 的设备即时判离线。
type AgentProbeFunc func(ctx context.Context, deviceID string) error

type Service struct {
	db             *sql.DB
	accounts       *account.Accounts
	now            func() int64
	agentStaleness time.Duration
	agentProbe     AgentProbeFunc
}

type Option func(*Service)

func WithNow(now func() int64) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

func WithAgentStaleness(staleness time.Duration) Option {
	return func(s *Service) {
		if staleness > 0 {
			s.agentStaleness = staleness
		}
	}
}

func WithAgentProbe(probe AgentProbeFunc) Option {
	return func(s *Service) {
		s.agentProbe = probe
	}
}

func New(config Config, options ...Option) (*Service, error) {
	if config.DB == nil {
		return nil, fmt.Errorf("sharing: DB is required")
	}
	if config.Accounts == nil {
		return nil, fmt.Errorf("sharing: account service is required")
	}
	s := &Service{
		db:             config.DB,
		accounts:       config.Accounts,
		now:            ids.NowMS,
		agentStaleness: defaultAgentStaleness,
	}
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

func normalizeTTL(ttl, fallback time.Duration) (time.Duration, error) {
	if ttl == 0 {
		ttl = fallback
	}
	if ttl < minShareTTL || ttl > maxShareTTL {
		return 0, ipc.BadParam(fmt.Errorf("分享有效期需在 %s-%s 之间", minShareTTL, maxShareTTL))
	}
	return ttl, nil
}

type auditPayload struct {
	Action     string `json:"action"`
	ShareID    string `json:"share_id,omitempty"`
	DeviceID   string `json:"device_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	OwnerID    string `json:"owner_id,omitempty"`
	Recipient  string `json:"recipient,omitempty"`
	Requester  string `json:"requester"`
	Outcome    string `json:"outcome"`
	Reason     string `json:"reason,omitempty"`
	Permission string `json:"permission,omitempty"`
}

func (s *Service) audit(ctx context.Context, kind string, payload auditPayload) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO audit_log(ts, session_id, asset_id, source, kind, payload_json, exit_code, duration_ms)
VALUES(?,NULL,NULL,?,?,?,NULL,NULL)`, s.now(), auditSourceSharing, kind, string(encoded)); err != nil {
		return dbError(err)
	}
	return nil
}

type deviceGrant struct {
	deviceID string
	userID   string
	revoked  bool
}

func (s *Service) loadGrant(ctx context.Context, deviceID string) (*deviceGrant, error) {
	var grant deviceGrant
	var revokedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT user_id, revoked_at FROM user_device WHERE id = ?", deviceID).
		Scan(&grant.userID, &revokedAt)
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

// authorizeDevice 判定 owner-or-superadmin 并给出 outcome/reason, 不写审计。
func (s *Service) authorizeDevice(ctx context.Context, identity *account.Identity, deviceID string) (*deviceGrant, string, string, error) {
	grant, err := s.loadGrant(ctx, deviceID)
	switch {
	case err != nil:
		return nil, "deny", "not_found", err
	case grant.revoked:
		return nil, "deny", "device_revoked", ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	case identity.Role != account.RoleSuperadmin && grant.userID != identity.UserID:
		return nil, "deny", "not_owner", ipc.NewError(ipc.CodeForbidden, "设备不属于该用户")
	}
	return grant, "allow", "", nil
}

// authorize 是分享设备动作的授权点: superadmin 或设备 owner 放行, 已吊销设备
// 一律拒绝; 每次判定 (放行或拒绝) 都写审计。
func (s *Service) authorize(ctx context.Context, identity *account.Identity, deviceID, action, kind string) (*deviceGrant, error) {
	grant, outcome, reason, decision := s.authorizeDevice(ctx, identity, deviceID)
	auditErr := s.audit(ctx, kind, auditPayload{Action: action, DeviceID: deviceID, Requester: identity.UserID, Outcome: outcome, Reason: reason})
	if decision != nil {
		return nil, decision
	}
	if auditErr != nil {
		return nil, auditErr
	}
	return grant, nil
}

// checkDeviceBinding 校验授权快照与当前设备状态一致: 设备必须存在、未吊销,
// 且当前 owner 与 Grant.OwnerID 一致 (设备易主后旧授权立即失效); 返回机器
// 可读的拒绝原因 ("" 表示通过)。
func (s *Service) checkDeviceBinding(ctx context.Context, grant *Grant) (string, error) {
	var ownerID string
	var revokedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT user_id, revoked_at FROM user_device WHERE id = ?", grant.DeviceID).
		Scan(&ownerID, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "device_not_found", ipc.NewError(ipc.CodeForbidden, "设备不存在")
	}
	if err != nil {
		return "", dbError(err)
	}
	if revokedAt.Valid {
		return "device_revoked", ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	}
	if ownerID != grant.OwnerID {
		return "owner_mismatch", ipc.NewError(ipc.CodeForbidden, "设备归属与授权不一致")
	}
	return "", nil
}

// requireDaemon 限定分享目标为 daemon 主机: 必须有 device_agent 记录且终端
// 功能开启; last_seen 超过 staleness 窗口视为代理离线。
func (s *Service) requireDaemon(ctx context.Context, deviceID string) error {
	var terminalEnabled int
	var lastSeenAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT terminal_enabled, last_seen_at FROM device_agent WHERE device_id = ?", deviceID).
		Scan(&terminalEnabled, &lastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ipc.NewError(ipc.CodeForbidden, "设备未接入守护代理")
	}
	if err != nil {
		return dbError(err)
	}
	if terminalEnabled == 0 {
		return ipc.NewError(ipc.CodeForbidden, "设备终端功能已禁用")
	}
	if !lastSeenAt.Valid || s.now()-lastSeenAt.Int64 > s.agentStaleness.Milliseconds() {
		return ipc.NewError(ipc.CodeDisconnected, "设备代理离线")
	}
	if s.agentProbe != nil {
		if err := s.agentProbe(ctx, deviceID); err != nil {
			return err
		}
	}
	return nil
}
