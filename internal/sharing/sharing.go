// Package sharing implements SHARE126 host sharing.
//
// Public share links grant time-boxed access to one live terminal session on
// a daemon device: read-only unless the owner explicitly opts into write,
// revocable, expiring, with every access and input audited. Registered-user
// host shares let a recipient open new terminals through the host agent at
// any time while the share is valid. Both kinds bind owner, device,
// permission and expiry, and neither ever reads or returns stored host
// passwords, private keys or sync payloads: the package touches no
// credential, asset or sync table, and only SHA-256 token hashes are stored.
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
	minShareTTL         = time.Minute
	maxShareTTL         = 30 * 24 * time.Hour

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
)

type Permission string

const (
	PermissionRead      Permission = "read"
	PermissionReadWrite Permission = "read_write"
)

func (p Permission) AllowsWrite() bool { return p == PermissionReadWrite }

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

type Service struct {
	db             *sql.DB
	accounts       *account.Accounts
	now            func() int64
	agentStaleness time.Duration
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
		return ipc.WrapError(ipc.CodeInternal, "内部错误: JSON: "+err.Error(), err)
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
		return nil, ipc.NewError(ipc.CodeNotFound, "未找到: 设备")
	}
	if err != nil {
		return nil, dbError(err)
	}
	grant.deviceID = deviceID
	grant.revoked = revokedAt.Valid
	return &grant, nil
}

// authorize 是分享设备动作的唯一授权点: superadmin 或设备 owner 放行,
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
	return nil
}
