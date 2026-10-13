// Package sharing implements SHARE126 host sharing.
//
// Public share links grant time-boxed access to one live terminal session on
// a daemon device: read-only unless the owner explicitly opts into write,
// revocable, expiring, with every access and input audited. Device share
// links do the same for the whole device: an anonymous visitor holding the
// link opens a fresh terminal through the host agent at any time while the
// link is valid, instead of attaching to one existing session. Registered-
// user host shares let a recipient open new terminals through the host agent
// at any time while the share is valid. Preview links do the same for
// server-hosted terminal tabs: an anonymous visitor watches the current
// screen and live output of one tab in the server's session manager;
// read-only by default, the owner can opt into read_write at creation,
// which additionally lets the visitor send binary input frames that are
// enforced per input against the current row and arbitrated through the
// session manager's guest-control lease. They share the same token-hash
// storage, expiry and revocation semantics as public share links. All
// kinds bind owner, permission and expiry, and none ever reads or returns
// stored host passwords, private keys or sync payloads: the package
// touches no credential, asset or sync table, and only SHA-256 token
// hashes are stored.
//
// Grants are snapshots, not authority: data paths must be built through
// NewLinkGate / NewHostGate, whose per-chunk rechecks revalidate current
// rows (including device ownership and revocation) and swap the refreshed
// Grant into every subsequent decision, so permission shrink and
// revocation stop input immediately and stop output once no valid row
// remains. Preview output has no chunks to ride on, so it revalidates on
// a fixed interval with RevalidatePreview: revocation and expiry
// terminate the stream within that interval regardless of traffic.
// Preview input is enforced per frame with RevalidatePreviewInput, which
// re-reads the current row and requires read_write, so revocation,
// expiry and permission shrink take effect on the very next input.
//
// Server-side HTTP mounting and fleet route integration are a separate
// slice; this package is verified with explicit fakes. Relay-capable hosts
// are not modeled yet, so both share kinds currently require a
// daemon-equipped device (a device_agent row); preview links instead bind
// tabs hosted by the server's own session manager.
package sharing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

const (
	auditSourceSharing = "sharing"

	linkTokenBytes = 32

	defaultLinkTTL      = time.Hour
	defaultHostShareTTL = 24 * time.Hour
	// defaultDeviceShareTTL is the default validity period of device public links: device-level sharing targets longer
	// collaboration windows, aligned with the registered host share default.
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

// ErrTokenNotFound means the public link token does not exist; the HTTP entry point counts random token attempts
// toward rate-limit backoff, while other denial reasons (revoked/expired/not-yet-valid/device offline) do not punish the link holder.
var ErrTokenNotFound = ipc.NewError(ipc.CodeForbidden, "分享链接无效")

func permissionFor(write bool) Permission {
	if write {
		return PermissionReadWrite
	}
	return PermissionRead
}

type Config struct {
	DB       *gorm.DB
	Accounts *account.Accounts
}

// AgentProbeFunc probes whether the device daemon agent is currently online (e.g. whether the control channel is registered);
// an error means offline. requireDaemon calls it after the database last_seen check passes,
// to instantly judge a device "process disconnected but last_seen within the staleness window" as offline.
type AgentProbeFunc func(ctx context.Context, deviceID string) error

type Service struct {
	db             *gorm.DB
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
	return s.writeAudit(ctx, kind, payload)
}

func (s *Service) writeAudit(ctx context.Context, kind string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
	}
	row := store.AuditRow{TS: s.now(), Source: auditSourceSharing, Kind: kind, PayloadJSON: string(encoded), OwnerID: ipc.OwnerID(ctx)}
	if err := query.Use(s.db).AuditRow.WithContext(ctx).Create(&row); err != nil {
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
	userDevice := query.Use(s.db).UserDevice
	device, err := userDevice.WithContext(ctx).Where(userDevice.ID.Eq(deviceID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ipc.NewError(ipc.CodeNotFound, "设备不存在")
	}
	if err != nil {
		return nil, dbError(err)
	}
	return &deviceGrant{deviceID: deviceID, userID: device.UserID, revoked: device.RevokedAt != nil}, nil
}

// authorizeDevice decides device ownership and yields outcome/reason without writing an audit.
func (s *Service) authorizeDevice(ctx context.Context, identity *account.Identity, deviceID string) (*deviceGrant, string, string, error) {
	grant, err := s.loadGrant(ctx, deviceID)
	switch {
	case err != nil:
		return nil, "deny", "not_found", err
	case grant.revoked:
		return nil, "deny", "device_revoked", ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	case grant.userID != identity.UserID:
		return nil, "deny", "not_owner", ipc.NewError(ipc.CodeForbidden, "设备不属于该用户")
	}
	return grant, "allow", "", nil
}

// authorize is the authorization point for share device actions: only the device owner is allowed, and revoked devices
// are always denied; every decision (allow or deny) is written to the audit log.
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

// checkDeviceBinding verifies that the authorization snapshot matches the current device state: the device must exist, not be revoked,
// and the current owner must match Grant.OwnerID (old authorizations become invalid immediately after the device changes hands); it returns a machine-
// readable denial reason ("" means pass).
func (s *Service) checkDeviceBinding(ctx context.Context, grant *Grant) (string, error) {
	userDevice := query.Use(s.db).UserDevice
	device, err := userDevice.WithContext(ctx).Where(userDevice.ID.Eq(grant.DeviceID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "device_not_found", ipc.NewError(ipc.CodeForbidden, "设备不存在")
	}
	if err != nil {
		return "", dbError(err)
	}
	if device.RevokedAt != nil {
		return "device_revoked", ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	}
	if device.UserID != grant.OwnerID {
		return "owner_mismatch", ipc.NewError(ipc.CodeForbidden, "设备归属与授权不一致")
	}
	return "", nil
}

// requireDaemon limits share targets to daemon hosts: a device_agent row must exist with the terminal
// feature enabled; last_seen beyond the staleness window counts as the agent being offline.
func (s *Service) requireDaemon(ctx context.Context, deviceID string) error {
	deviceAgent := query.Use(s.db).DeviceAgent
	agent, err := deviceAgent.WithContext(ctx).Where(deviceAgent.DeviceID.Eq(deviceID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ipc.NewError(ipc.CodeForbidden, "设备未接入代理")
	}
	if err != nil {
		return dbError(err)
	}
	if !agent.TerminalEnabled {
		return ipc.NewError(ipc.CodeForbidden, "设备终端功能已禁用")
	}
	if agent.LastSeenAt == nil || s.now()-*agent.LastSeenAt > s.agentStaleness.Milliseconds() {
		return ipc.NewError(ipc.CodeDisconnected, "设备代理离线")
	}
	if s.agentProbe != nil {
		if err := s.agentProbe(ctx, deviceID); err != nil {
			return err
		}
	}
	return nil
}
