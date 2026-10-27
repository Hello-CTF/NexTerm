package fleetserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/sharing"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen/field"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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
	defaultEnrollCodeTTL  = 15 * time.Minute
	minEnrollCodeTTL      = time.Minute
	maxEnrollCodeTTL      = time.Hour

	rawMetricsRetention    = 48 * time.Hour
	hourlyMetricsRetention = 30 * 24 * time.Hour
	metricsHourMS          = 3600 * 1000
	maxMetricsSamples      = 5000

	auditSourceFleet = "fleet"
)

const (
	auditKindDeviceEnroll         = "device_enroll"
	auditKindDeviceRevoke         = "device_revoke"
	auditKindDeviceAutostart      = "device_autostart"
	auditKindDeviceTerminalToggle = "device_terminal_toggle"
	auditKindDeviceFailover       = "device_failover"
	auditKindDeviceTerminal       = "device_terminal"
	auditKindDeviceOnline         = "device_online"
	auditKindDeviceOffline        = "device_offline"
)

type Config struct {
	DB       *gorm.DB
	Accounts *account.Accounts
	AuthOff  bool
	// Events is the optional event outlet (EventBroker); when nil, online/offline only writes audits and does not push.
	Events ipc.Emitter
}

type Service struct {
	db         *gorm.DB
	accounts   *account.Accounts
	authOff    bool
	now        func() int64
	enrollGate *account.LoginThrottle
	registry   *Registry
	sharing    *sharing.Service
	events     ipc.Emitter

	// presenceMu guards onlineDevices: device online/offline events and audits are produced only once per real state transition,
	// and replacement reconnects (the old connection's unregistration is already replaced) and service shutdown (Close swaps in an empty table) never
	// emit duplicate or extra events.
	presenceMu    sync.Mutex
	onlineDevices map[string]bool

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

// WithEvents overrides the event outlet (the wiring layer passes EventBroker); nil means only audits are written, nothing is pushed.
func WithEvents(events ipc.Emitter) Option {
	return func(s *Service) {
		s.events = events
	}
}

// WithShareRevalidateInterval overrides the periodic recheck interval for idle share connections (tests use a small interval
// to speed up revocation/expiry taking effect; production default defaultShareRevalidateInterval).
func WithShareRevalidateInterval(interval time.Duration) Option {
	return func(s *Service) {
		if interval > 0 {
			s.shareRevalidateInterval = interval
		}
	}
}

func New(config Config, options ...Option) (*Service, error) {
	if config.DB == nil {
		return nil, fmt.Errorf("fleet: DB is required")
	}
	if config.Accounts == nil {
		return nil, fmt.Errorf("fleet: account service is required")
	}
	s := &Service{
		db:            config.DB,
		accounts:      config.Accounts,
		authOff:       config.AuthOff,
		now:           ids.NowMS,
		enrollGate:    account.NewLoginThrottle(),
		registry:      NewRegistry(),
		events:        config.Events,
		onlineDevices: make(map[string]bool),

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
	var appErr *ipc.Error
	if errors.As(err, &appErr) {
		return err
	}
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
	BaseURLs          []BaseURLEntry
	MetricsIntervalMS int64
	DesiredAutostart  bool
	TerminalEnabled   bool
	// Kind echoes the requested enroll kind.
	Kind string
}

type auditPayload struct {
	Action    string `json:"action"`
	DeviceID  string `json:"device_id"`
	Requester string `json:"requester"`
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason,omitempty"`
	URL       string `json:"url,omitempty"`
}

// audit writes a fleet audit; when the payload carries a device ID the asset_id column is populated as well, so the audit view can filter
// a complete per-device timeline (enroll/online/offline/revoke/switch access address) without parsing payload_json.
func (s *Service) audit(ctx context.Context, kind string, payload auditPayload) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
	}
	row := store.AuditRow{
		TS: s.now(), Source: auditSourceFleet, Kind: kind,
		PayloadJSON: string(encoded), OwnerID: ipc.OwnerID(ctx),
	}
	if payload.DeviceID != "" {
		row.AssetID = &payload.DeviceID
	}
	if err := query.Use(s.db).AuditRow.WithContext(ctx).Create(&row); err != nil {
		return dbError(err)
	}
	return nil
}

// deviceOnline/deviceOffline are the only exit points for device presence: they write an audit and push device://status only on real state transitions,
// and repeated calls (the unregister/register interleave of a replacement reconnect) are ignored outright.
// The caller may already hold a canceled ctx (control channel disconnect path); a background ctx is used uniformly here to finish the database write and the push.
// refreshAgentAppVersion persists the agent-reported version from the control-channel hello when it changed,
// so device management reflects upgrades; the enroll-time value alone goes stale after a binary swap.
func (s *Service) refreshAgentAppVersion(ctx context.Context, deviceID, appVersion string) {
	if deviceID == "" || appVersion == "" || len(appVersion) > maxAppVersionLength {
		return
	}
	deviceAgent := query.Use(s.db).DeviceAgent
	if _, err := deviceAgent.WithContext(ctx).
		Where(deviceAgent.DeviceID.Eq(deviceID), deviceAgent.AppVersion.Neq(appVersion)).
		UpdateColumns(map[string]any{"app_version": appVersion}); err != nil {
		slog.Warn("failed to refresh device agent version", "device", deviceID, "error", err)
	}
}

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
	kind     string
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
	return &deviceGrant{deviceID: deviceID, userID: device.UserID, kind: device.Kind, revoked: device.RevokedAt != nil}, nil
}

// authorize is the single authorization point for fleet device actions: only the device owner is allowed,
// and revoked devices are always denied; every decision (allow or deny) is written to the audit log.
func (s *Service) authorize(ctx context.Context, identity *account.Identity, deviceID, action, kind string) (*deviceGrant, error) {
	grant, err := s.loadGrant(ctx, deviceID)
	outcome, reason := "allow", ""
	var decision error
	switch {
	case err != nil:
		outcome, reason, decision = "deny", "not_found", err
	case grant.revoked:
		outcome, reason, decision = "deny", "device_revoked", ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	case grant.userID != identity.UserID:
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

// canAccess follows the same owner rules as authorize, for read-only paths, and writes no audit.
func (s *Service) canAccess(ctx context.Context, identity *account.Identity, deviceID string) (*deviceGrant, error) {
	grant, err := s.loadGrant(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	if grant.revoked {
		return nil, ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	}
	if grant.userID != identity.UserID {
		return nil, ipc.NewError(ipc.CodeForbidden, "设备不属于该用户")
	}
	return grant, nil
}

// DeviceOwnerID returns the owning user ID of the device, for the event outlet to filter device://status
// pushes by owner; it returns an error when the device does not exist.
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
			return "", 0, ipc.NewError(ipc.CodeForbidden, "需要超级管理员权限")
		}
		ownerID = targetUserID
	}
	if _, err := s.accounts.GetUser(ctx, ownerID); err != nil {
		return "", 0, err
	}
	if ttl == 0 {
		ttl = defaultEnrollCodeTTL
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
	row := store.DeviceEnrollCode{ID: ids.New(), UserID: ownerID, CodeHash: hashSecret(code), CreatedAt: now, ExpiresAt: expiresAt}
	if err := query.Use(s.db).DeviceEnrollCode.WithContext(ctx).Create(&row); err != nil {
		return "", 0, dbError(err)
	}
	return code, expiresAt, nil
}

func (s *Service) ConsumeEnrollCode(ctx context.Context, code, name, platform, appVersion string) (*EnrollResult, error) {
	return s.ConsumeEnrollCodeKind(ctx, code, name, platform, appVersion, "")
}

// ConsumeEnrollCodeKind enrolls a device of the given enroll kind: empty for a
// managed host agent, or agent.DeviceKindLocalHelper for a desktop exposing its
// local helper. A local helper device starts without autostart, since it runs
// inside the desktop app, and is never shared with another account.
func (s *Service) ConsumeEnrollCodeKind(ctx context.Context, code, name, platform, appVersion, kind string) (*EnrollResult, error) {
	deviceKind := agentDeviceKind
	switch kind {
	case "":
	case agent.DeviceKindLocalHelper:
		deviceKind = store.DeviceKindLocalHelper
	default:
		return nil, ipc.BadParam(fmt.Errorf("未知的设备类型 %q", kind))
	}
	desiredAutostart := deviceKind == agentDeviceKind
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
	deviceID := ids.New()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := query.Use(tx)
		enrollCode := q.DeviceEnrollCode
		// Atomically claim the enroll code: only an unconsumed, unexpired row can be updated, and only one of concurrent consumes succeeds.
		claimed, err := enrollCode.WithContext(ctx).
			Where(enrollCode.CodeHash.Eq(hashSecret(code)), enrollCode.ConsumedAt.IsNull(), enrollCode.ExpiresAt.Gt(now)).
			Update(enrollCode.ConsumedAt, now)
		if err != nil {
			return dbError(err)
		}
		if claimed.RowsAffected == 0 {
			return ipc.NewError(ipc.CodeForbidden, "设备注册码无效或已过期，请重新生成")
		}
		consumed, err := enrollCode.WithContext(ctx).Where(enrollCode.CodeHash.Eq(hashSecret(code))).First()
		if err != nil {
			return dbError(err)
		}
		ownerID := consumed.UserID
		if err := q.UserDevice.WithContext(ctx).Create(&store.UserDevice{
			ID: deviceID, UserID: ownerID, Name: name, Kind: deviceKind, CreatedAt: now,
		}); err != nil {
			return dbError(err)
		}
		credentialID := ids.New()
		if err := q.SyncCredential.WithContext(ctx).Create(&store.SyncCredential{
			ID: credentialID, UserID: ownerID, DeviceID: deviceID, Purpose: PurposeDeviceAgent,
			SecretHash: hashSecret(secret), CreatedAt: now,
		}); err != nil {
			return dbError(err)
		}
		if err := q.DeviceAgent.WithContext(ctx).Create(&store.DeviceAgent{
			DeviceID: deviceID, CredentialID: credentialID, Platform: platform, AppVersion: appVersion,
			DesiredAutostart: desiredAutostart, TerminalEnabled: true, ServiceStateJSON: "{}", CreatedAt: now,
		}); err != nil {
			return dbError(err)
		}
		payload, err := json.Marshal(auditPayload{Action: "enroll", DeviceID: deviceID, Requester: ownerID, Outcome: "allow"})
		if err != nil {
			return ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
		}
		auditRow := store.AuditRow{
			TS: now, AssetID: &deviceID, Source: auditSourceFleet, Kind: auditKindDeviceEnroll,
			PayloadJSON: string(payload), OwnerID: ownerID,
		}
		if err := q.AuditRow.WithContext(ctx).Create(&auditRow); err != nil {
			return dbError(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &EnrollResult{
		DeviceID:          deviceID,
		Secret:            secret,
		BaseURLs:          baseURLs,
		MetricsIntervalMS: DefaultMetricsIntervalMS,
		DesiredAutostart:  desiredAutostart,
		TerminalEnabled:   true,
		Kind:              kind,
	}, nil
}

// AuthenticateAgent verifies the device credential triple binding (device_id + secret + purpose);
// a revoked or expired credential or device is denied; on success it writes back last_used_at/last_seen_at.
func (s *Service) AuthenticateAgent(ctx context.Context, deviceID, secret string) (string, error) {
	syncCredential := query.Use(s.db).SyncCredential
	credential, err := syncCredential.WithContext(ctx).
		Where(syncCredential.SecretHash.Eq(hashSecret(secret)), syncCredential.Purpose.Eq(PurposeDeviceAgent)).
		First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ipc.NewError(ipc.CodeForbidden, "设备凭证无效")
	}
	if err != nil {
		return "", dbError(err)
	}
	userDevice := query.Use(s.db).UserDevice
	device, err := userDevice.WithContext(ctx).Where(userDevice.ID.Eq(credential.DeviceID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ipc.NewError(ipc.CodeForbidden, "设备凭证无效")
	}
	if err != nil {
		return "", dbError(err)
	}
	switch {
	case credential.DeviceID != deviceID:
		return "", ipc.NewError(ipc.CodeForbidden, "设备凭证与设备不匹配")
	case credential.RevokedAt != nil:
		return "", ipc.NewError(ipc.CodeForbidden, "设备凭证已吊销")
	case credential.ExpiresAt > 0 && s.now() >= credential.ExpiresAt:
		return "", ipc.NewError(ipc.CodeForbidden, "设备凭证已过期")
	case device.RevokedAt != nil:
		return "", ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	}
	now := s.now()
	if _, err := syncCredential.WithContext(ctx).
		Where(syncCredential.DeviceID.Eq(deviceID), syncCredential.Purpose.Eq(PurposeDeviceAgent)).
		UpdateColumns(map[string]any{"last_used_at": now}); err != nil {
		return "", dbError(err)
	}
	if _, err := userDevice.WithContext(ctx).Where(userDevice.ID.Eq(deviceID)).
		UpdateColumns(map[string]any{"last_seen_at": now}); err != nil {
		return "", dbError(err)
	}
	deviceAgent := query.Use(s.db).DeviceAgent
	if _, err := deviceAgent.WithContext(ctx).Where(deviceAgent.DeviceID.Eq(deviceID)).
		UpdateColumns(map[string]any{"last_seen_at": now}); err != nil {
		return "", dbError(err)
	}
	return credential.UserID, nil
}

func (s *Service) ListDevices(ctx context.Context, identity *account.Identity) ([]*Device, error) {
	userDevice := query.Use(s.db).UserDevice
	rows, err := userDevice.WithContext(ctx).
		Where(userDevice.UserID.Eq(identity.UserID)).
		Order(userDevice.CreatedAt, userDevice.ID).
		Find()
	if err != nil {
		return nil, dbError(err)
	}
	deviceIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		deviceIDs = append(deviceIDs, row.ID)
	}
	agents := map[string]*store.DeviceAgent{}
	if len(deviceIDs) > 0 {
		deviceAgent := query.Use(s.db).DeviceAgent
		agentRows, err := deviceAgent.WithContext(ctx).Where(deviceAgent.DeviceID.In(deviceIDs...)).Find()
		if err != nil {
			return nil, dbError(err)
		}
		for _, row := range agentRows {
			agents[row.DeviceID] = row
		}
	}
	usernames := map[string]string{}
	if identity.Role == account.RoleSuperadmin && len(deviceIDs) > 0 {
		ownerIDs := make([]string, 0, len(rows))
		for _, row := range rows {
			ownerIDs = append(ownerIDs, row.UserID)
		}
		appUser := query.Use(s.db).AppUser
		userRows, err := appUser.WithContext(ctx).Where(appUser.ID.In(ownerIDs...)).Find()
		if err != nil {
			return nil, dbError(err)
		}
		for _, row := range userRows {
			usernames[row.ID] = row.Username
		}
	}
	var devices []*Device
	for _, row := range rows {
		device := &Device{ID: row.ID, Name: row.Name, Kind: row.Kind, CreatedAt: row.CreatedAt}
		if row.LastSeenAt != nil {
			device.LastSeenAt = *row.LastSeenAt
		}
		if row.RevokedAt != nil {
			device.RevokedAt = *row.RevokedAt
		}
		if identity.Role == account.RoleSuperadmin {
			device.Owner = &Owner{ID: row.UserID, Username: usernames[row.UserID]}
		}
		if agentRow, ok := agents[row.ID]; ok {
			agent := &AgentInfo{
				Platform:         agentRow.Platform,
				AppVersion:       agentRow.AppVersion,
				DesiredAutostart: agentRow.DesiredAutostart,
				TerminalEnabled:  agentRow.TerminalEnabled,
				CurrentURL:       agentRow.CurrentURL,
			}
			if agentRow.LastSeenAt != nil {
				agent.LastSeenAt = *agentRow.LastSeenAt
			}
			if agentRow.ServiceStateJSON != "" {
				if err := json.Unmarshal([]byte(agentRow.ServiceStateJSON), &agent.ServiceState); err != nil {
					return nil, dbError(err)
				}
			}
			device.Agent = agent
		}
		devices = append(devices, device)
	}
	return devices, nil
}

func (s *Service) RevokeDevice(ctx context.Context, identity *account.Identity, deviceID string) error {
	grant, err := s.authorize(ctx, identity, deviceID, "revoke", auditKindDeviceRevoke)
	if err != nil {
		return err
	}
	now := s.now()
	// The revocation and its epoch bump share the account async-boundary mutex,
	// so a device revocation invalidates pending logins, MFA tickets, DEK
	// uploads, and TOTP bindings exactly like an account-side revocation.
	s.accounts.RevocationMu().Lock()
	defer s.accounts.RevocationMu().Unlock()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := query.Use(tx)
		if _, err := q.UserDevice.WithContext(ctx).
			Where(q.UserDevice.ID.Eq(deviceID), q.UserDevice.RevokedAt.IsNull()).
			UpdateColumns(map[string]any{"revoked_at": now}); err != nil {
			return dbError(err)
		}
		if _, err := q.UserSession.WithContext(ctx).
			Where(q.UserSession.DeviceID.Eq(deviceID), q.UserSession.RevokedAt.IsNull()).
			UpdateColumns(map[string]any{"revoked_at": now}); err != nil {
			return dbError(err)
		}
		if _, err := q.SyncCredential.WithContext(ctx).
			Where(q.SyncCredential.DeviceID.Eq(deviceID), q.SyncCredential.RevokedAt.IsNull()).
			UpdateColumns(map[string]any{"revoked_at": now}); err != nil {
			return dbError(err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.accounts.BumpEpoch(grant.userID)
	s.registry.KickDevice(deviceID)
	// Revocation means offline: after the control channel is kicked there is no more unregistration callback (KickDevice already removed it from the table),
	// so one offline transition is made up here, keeping the audit timeline and the device://status push complete.
	s.deviceOffline(deviceID)
	return nil
}

// AgentDesiredConfig returns the agent config the server desires; the metrics interval is a product-fixed value
// (the schema has no per-device column), kept consistent with what enroll delivers.
func (s *Service) AgentDesiredConfig(ctx context.Context, deviceID string) (AgentConfig, error) {
	deviceAgent := query.Use(s.db).DeviceAgent
	agent, err := deviceAgent.WithContext(ctx).Where(deviceAgent.DeviceID.Eq(deviceID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return AgentConfig{}, ipc.NewError(ipc.CodeNotFound, "设备代理不存在")
	}
	if err != nil {
		return AgentConfig{}, dbError(err)
	}
	return AgentConfig{
		DesiredAutostart:  agent.DesiredAutostart,
		MetricsIntervalMS: DefaultMetricsIntervalMS,
		TerminalEnabled:   agent.TerminalEnabled,
	}, nil
}

// AgentConfig is the agent runtime config the server desires.
type AgentConfig struct {
	DesiredAutostart  bool
	MetricsIntervalMS int64
	TerminalEnabled   bool
}

func (s *Service) SetDesiredAutostart(ctx context.Context, identity *account.Identity, deviceID string, desired bool) error {
	grant, err := s.authorize(ctx, identity, deviceID, "autostart", auditKindDeviceAutostart)
	if err != nil {
		return err
	}
	if desired && grant.kind == store.DeviceKindLocalHelper {
		return ipc.NewError(ipc.CodeBadParam, "该设备随 NexTerm 桌面应用运行，不使用自启动服务")
	}
	deviceAgent := query.Use(s.db).DeviceAgent
	updated, err := deviceAgent.WithContext(ctx).Where(deviceAgent.DeviceID.Eq(deviceID)).
		UpdateColumns(map[string]any{"desired_autostart": desired})
	if err != nil {
		return dbError(err)
	}
	if updated.RowsAffected == 0 {
		return ipc.NewError(ipc.CodeNotFound, "设备代理不存在")
	}
	s.pushDesiredConfig(ctx, deviceID)
	return nil
}

// pushDesiredConfig sends the stored desired config to the connected agent; offline agents pick it up on their next sync.
func (s *Service) pushDesiredConfig(ctx context.Context, deviceID string) {
	config, err := s.AgentDesiredConfig(ctx, deviceID)
	if err != nil {
		return
	}
	s.registry.PushControl(deviceID, controlMessage{
		Type:              "config",
		DesiredAutostart:  &config.DesiredAutostart,
		MetricsIntervalMS: &config.MetricsIntervalMS,
		TerminalEnabled:   &config.TerminalEnabled,
	})
}

// SetTerminalEnabled switches remote terminal access for a device (same owner-only authorization and audit as autostart).
// It gates new terminal/file/forward bridges server-side (AuthorizeDeviceTerminal, bridge_http.go) and is pushed to a connected
// agent, which then refuses new bridge requests; bridges that are already open are not torn down.
func (s *Service) SetTerminalEnabled(ctx context.Context, identity *account.Identity, deviceID string, enabled bool) error {
	if _, err := s.authorize(ctx, identity, deviceID, "terminal_toggle", auditKindDeviceTerminalToggle); err != nil {
		return err
	}
	deviceAgent := query.Use(s.db).DeviceAgent
	updated, err := deviceAgent.WithContext(ctx).Where(deviceAgent.DeviceID.Eq(deviceID)).
		UpdateColumns(map[string]any{"terminal_enabled": enabled})
	if err != nil {
		return dbError(err)
	}
	if updated.RowsAffected == 0 {
		return ipc.NewError(ipc.CodeNotFound, "设备代理不存在")
	}
	s.pushDesiredConfig(ctx, deviceID)
	return nil
}

func (s *Service) BaseURLs(ctx context.Context) ([]BaseURLEntry, error) {
	setting := query.Use(s.db).Setting
	row, err := setting.WithContext(ctx).Where(setting.Key.Eq(baseURLsSettingKey)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return []BaseURLEntry{}, nil
	}
	if err != nil {
		return nil, dbError(err)
	}
	var entries []BaseURLEntry
	if err := json.Unmarshal([]byte(row.Value), &entries); err != nil {
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
	row := store.Setting{Key: baseURLsSettingKey, Value: string(encoded), UpdatedAt: s.now()}
	if err := query.Use(s.db).Setting.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
		}).
		Create(&row); err != nil {
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
	userDevice := query.Use(s.db).UserDevice
	device, err := userDevice.WithContext(ctx).Where(userDevice.ID.Eq(deviceID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ipc.NewError(ipc.CodeNotFound, "设备不存在")
	}
	if err != nil {
		return dbError(err)
	}
	if device.RevokedAt != nil {
		return ipc.NewError(ipc.CodeForbidden, "设备已吊销")
	}
	if sample.TS == 0 {
		sample.TS = s.now()
	}
	row := store.DeviceMetrics{
		ID: ids.New(), DeviceID: deviceID, TS: sample.TS,
		CPUPct: &sample.CPUPct, MemUsed: &sample.MemUsed, MemTotal: &sample.MemTotal,
		DiskUsed: &sample.DiskUsed, DiskTotal: &sample.DiskTotal, UptimeS: &sample.UptimeS,
	}
	if err := query.Use(s.db).DeviceMetrics.WithContext(ctx).Create(&row); err != nil {
		return dbError(err)
	}
	return nil
}

type DeviceMetricsResult struct {
	Samples          []*MetricsSample `json:"samples"`
	Latest           *MetricsSample   `json:"latest,omitempty"`
	WindowStart      int64            `json:"windowStart,omitempty"`
	WindowEnd        int64            `json:"windowEnd,omitempty"`
	SampleCount      int64            `json:"sampleCount,omitempty"`
	Downsampled      bool             `json:"downsampled,omitempty"`
	SeriesError      string           `json:"seriesError,omitempty"`
	SampleIntervalMS int64            `json:"sampleIntervalMs,omitempty"`
}

type metricsPoint struct {
	ID          string `gorm:"column:id"`
	CPUPresent  bool   `gorm:"column:cpu_present"`
	MemPresent  bool   `gorm:"column:mem_present"`
	DiskPresent bool   `gorm:"column:disk_present"`
	MetricsSample
}

type metricsExtrema struct {
	min, max           metricsPoint
	minValue, maxValue float64
}

type metricsBucket struct {
	first, last    metricsPoint
	cpu, mem, disk metricsExtrema
}

func (e *metricsExtrema) add(point metricsPoint, value float64) {
	if e.min.ID == "" || value < e.minValue {
		e.min, e.minValue = point, value
	}
	if e.max.ID == "" || value > e.maxValue {
		e.max, e.maxValue = point, value
	}
}

func metricPointFromRow(row *store.DeviceMetrics) metricsPoint {
	point := metricsPoint{ID: row.ID, MetricsSample: MetricsSample{TS: row.TS}}
	if row.CPUPct != nil {
		point.CPUPct = *row.CPUPct
	}
	if row.MemUsed != nil {
		point.MemUsed = *row.MemUsed
	}
	if row.MemTotal != nil {
		point.MemTotal = *row.MemTotal
	}
	if row.DiskUsed != nil {
		point.DiskUsed = *row.DiskUsed
	}
	if row.DiskTotal != nil {
		point.DiskTotal = *row.DiskTotal
	}
	if row.UptimeS != nil {
		point.UptimeS = *row.UptimeS
	}
	return point
}

func (s *Service) DeviceMetrics(ctx context.Context, identity *account.Identity, deviceID string, since int64) (*DeviceMetricsResult, error) {
	if _, err := s.canAccess(ctx, identity, deviceID); err != nil {
		return nil, err
	}
	windowEnd := s.now()
	oldest := windowEnd - rawMetricsRetention.Milliseconds()
	if since <= 0 {
		since = windowEnd - 24*time.Hour.Milliseconds()
	}
	if since < oldest {
		since = oldest
	}
	result := &DeviceMetricsResult{Samples: []*MetricsSample{}, WindowStart: since, WindowEnd: windowEnd}
	deviceMetrics := query.Use(s.db).DeviceMetrics
	latestRow, err := deviceMetrics.WithContext(ctx).
		Where(deviceMetrics.DeviceID.Eq(deviceID), deviceMetrics.TS.Gte(oldest), deviceMetrics.TS.Lte(windowEnd)).
		Order(deviceMetrics.TS.Desc(), deviceMetrics.ID.Desc()).
		First()
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, dbError(err)
	}
	var latest *metricsPoint
	if err == nil {
		point := metricPointFromRow(latestRow)
		latest = &point
		result.Latest = &point.MetricsSample
	}
	samples, sampleCount, sampleInterval, err := s.readMetricsWindow(ctx, deviceID, since, windowEnd, latest)
	if err != nil {
		result.SeriesError = dbError(err).Error()
		return result, nil
	}
	result.Samples = samples
	result.SampleCount = sampleCount
	result.SampleIntervalMS = sampleInterval
	result.Downsampled = sampleCount > int64(len(samples))
	return result, nil
}

func (s *Service) readMetricsWindow(ctx context.Context, deviceID string, windowStart, windowEnd int64, latest *metricsPoint) ([]*MetricsSample, int64, int64, error) {
	const (
		batchSize         = maxMetricsSamples
		bucketCount       = 500
		recentIntervalCap = 20
	)
	if windowStart > windowEnd {
		return []*MetricsSample{}, 0, 0, nil
	}
	deviceMetrics := query.Use(s.db).DeviceMetrics
	buckets := make([]metricsBucket, bucketCount)
	all := make([]metricsPoint, 0, maxMetricsSamples)
	var recentIntervals [recentIntervalCap]int64
	var sampleCount int64
	var intervalCount, intervalPos int
	var previousTS int64
	var hasPrevious bool
	span := windowEnd - windowStart
	cursorTS := windowStart
	cursorID := ""
	hasCursor := false
	for {
		query := deviceMetrics.WithContext(ctx).
			Where(deviceMetrics.DeviceID.Eq(deviceID), deviceMetrics.TS.Gte(windowStart), deviceMetrics.TS.Lte(windowEnd))
		if hasCursor {
			query = query.Where(deviceMetrics.TS.Gte(cursorTS), field.Or(
				deviceMetrics.TS.Gt(cursorTS),
				field.And(deviceMetrics.TS.Eq(cursorTS), deviceMetrics.ID.Gt(cursorID)),
			))
		}
		rows, err := query.
			Select(
				deviceMetrics.ID,
				deviceMetrics.TS,
				field.NewUnsafeFieldRaw("COALESCE(cpu_pct,0) AS cpu_pct"),
				field.NewUnsafeFieldRaw("COALESCE(mem_used,0) AS mem_used"),
				field.NewUnsafeFieldRaw("COALESCE(mem_total,0) AS mem_total"),
				field.NewUnsafeFieldRaw("COALESCE(disk_used,0) AS disk_used"),
				field.NewUnsafeFieldRaw("COALESCE(disk_total,0) AS disk_total"),
				field.NewUnsafeFieldRaw("COALESCE(uptime_s,0) AS uptime_s"),
				field.NewUnsafeFieldRaw("cpu_pct IS NOT NULL AS cpu_present"),
				field.NewUnsafeFieldRaw("(mem_used IS NOT NULL AND mem_total IS NOT NULL) AS mem_present"),
				field.NewUnsafeFieldRaw("(disk_used IS NOT NULL AND disk_total IS NOT NULL) AS disk_present"),
			).
			Order(deviceMetrics.TS, deviceMetrics.ID).
			Limit(batchSize).
			Rows()
		if err != nil {
			return nil, 0, 0, err
		}
		batchRows := 0
		lastRow := metricsPoint{}
		row := metricsPoint{}
		for rows.Next() {
			row = metricsPoint{}
			if err := rows.Scan(&row.ID, &row.TS, &row.CPUPct, &row.MemUsed, &row.MemTotal, &row.DiskUsed, &row.DiskTotal, &row.UptimeS, &row.CPUPresent, &row.MemPresent, &row.DiskPresent); err != nil {
				_ = rows.Close()
				return nil, 0, 0, err
			}
			lastRow = row
			batchRows++
			sampleCount++
			if sampleCount <= maxMetricsSamples {
				all = append(all, row)
			} else if sampleCount == maxMetricsSamples+1 {
				all = nil
			}
			bucketIndex := 0
			if span > 0 {
				bucketIndex = int((row.TS - windowStart) * bucketCount / (span + 1))
			}
			bucket := &buckets[bucketIndex]
			if bucket.first.ID == "" {
				bucket.first = row
			}
			bucket.last = row
			if row.CPUPresent {
				bucket.cpu.add(row, row.CPUPct)
			}
			if row.MemPresent && row.MemTotal > 0 {
				bucket.mem.add(row, float64(row.MemUsed)/float64(row.MemTotal))
			}
			if row.DiskPresent && row.DiskTotal > 0 {
				bucket.disk.add(row, float64(row.DiskUsed)/float64(row.DiskTotal))
			}
			if hasPrevious && row.TS > previousTS {
				recentIntervals[intervalPos] = row.TS - previousTS
				intervalPos = (intervalPos + 1) % recentIntervalCap
				if intervalCount < recentIntervalCap {
					intervalCount++
				}
			}
			previousTS = row.TS
			hasPrevious = true
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, 0, 0, err
		}
		if err := rows.Close(); err != nil {
			return nil, 0, 0, err
		}
		if batchRows < batchSize {
			break
		}
		cursorTS, cursorID, hasCursor = lastRow.TS, lastRow.ID, true
	}

	points := all
	if sampleCount > maxMetricsSamples {
		type pointKey struct {
			ts int64
			id string
		}
		selected := map[pointKey]metricsPoint{}
		add := func(point metricsPoint) {
			if point.ID != "" {
				selected[pointKey{ts: point.TS, id: point.ID}] = point
			}
		}
		for i := range buckets {
			bucket := &buckets[i]
			add(bucket.first)
			add(bucket.last)
			add(bucket.cpu.min)
			add(bucket.cpu.max)
			add(bucket.mem.min)
			add(bucket.mem.max)
			add(bucket.disk.min)
			add(bucket.disk.max)
		}
		if latest != nil && latest.TS >= windowStart && latest.TS <= windowEnd {
			add(*latest)
		}
		points = make([]metricsPoint, 0, len(selected))
		for _, point := range selected {
			points = append(points, point)
		}
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].TS == points[j].TS {
			return points[i].ID < points[j].ID
		}
		return points[i].TS < points[j].TS
	})
	samples := make([]*MetricsSample, 0, len(points))
	for i := range points {
		samples = append(samples, &points[i].MetricsSample)
	}
	if intervalCount == 0 {
		return samples, sampleCount, 0, nil
	}
	intervals := make([]int64, intervalCount)
	copy(intervals, recentIntervals[:intervalCount])
	sort.Slice(intervals, func(i, j int) bool { return intervals[i] < intervals[j] })
	sampleInterval := intervals[intervalCount/2]
	if intervalCount%2 == 0 {
		sampleInterval = (intervals[intervalCount/2-1] + intervals[intervalCount/2]) / 2
	}
	return samples, sampleCount, sampleInterval, nil
}

// RollupMetrics aggregates metrics past the raw retention period (48h) into hourly buckets (kept 30d),
// then cleans up expired raw rows and hourly buckets; repeated runs are idempotent.
// Only complete hour buckets are aggregated: the cutoff is aligned down to the hour boundary, and aggregation and raw-row deletion share the same cutoff,
// so boundary buckets are kept whole until the next round, and samples are never lost to whole-bucket overwrites across batches.
func (s *Service) RollupMetrics(ctx context.Context) error {
	now := s.now()
	rawCutoff := now - rawMetricsRetention.Milliseconds()
	rollupCutoff := (rawCutoff / metricsHourMS) * metricsHourMS
	hourlyCutoff := now - hourlyMetricsRetention.Milliseconds()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		upsert := &gorm.Statement{DB: tx, Clauses: make(map[string]clause.Clause)}
		upsert.AddClause(clause.OnConflict{
			Columns: []clause.Column{{Name: "device_id"}, {Name: "bucket_ts"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"cpu_pct", "mem_used", "mem_total", "disk_used", "disk_total", "uptime_s", "sample_count",
			}),
		})
		upsert.Build("ON CONFLICT")
		if err := tx.Exec(`INSERT INTO device_metrics_hourly(device_id, bucket_ts, cpu_pct, mem_used, mem_total, disk_used, disk_total, uptime_s, sample_count)
SELECT device_id, (ts - ts % 3600000), avg(cpu_pct), avg(mem_used), max(mem_total), avg(disk_used), max(disk_total), max(uptime_s), count(*)
FROM device_metrics WHERE ts < ?
GROUP BY device_id, (ts - ts % 3600000)
`+upsert.SQL.String(),
			rollupCutoff).Error; err != nil {
			return dbError(err)
		}
		if err := tx.Exec("DELETE FROM device_metrics WHERE ts < ?", rollupCutoff).Error; err != nil {
			return dbError(err)
		}
		if err := tx.Exec("DELETE FROM device_metrics_hourly WHERE bucket_ts < ?", hourlyCutoff).Error; err != nil {
			return dbError(err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// UpdateServiceState writes back the service state actually reported by the agent (installed/enabled/active).
func (s *Service) UpdateServiceState(ctx context.Context, deviceID string, state ServiceState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
	}
	deviceAgent := query.Use(s.db).DeviceAgent
	updated, err := deviceAgent.WithContext(ctx).Where(deviceAgent.DeviceID.Eq(deviceID)).
		UpdateColumns(map[string]any{"service_state_json": string(encoded)})
	if err != nil {
		return dbError(err)
	}
	if updated.RowsAffected == 0 {
		return ipc.NewError(ipc.CodeNotFound, "设备代理不存在")
	}
	return nil
}

// SetCurrentURL records the agent's current access address after a switch and writes a failover audit.
func (s *Service) SetCurrentURL(ctx context.Context, deviceID, url, reason string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := query.Use(tx)
		deviceAgent := q.DeviceAgent
		updated, err := deviceAgent.WithContext(ctx).Where(deviceAgent.DeviceID.Eq(deviceID)).
			UpdateColumns(map[string]any{"current_url": url})
		if err != nil {
			return dbError(err)
		}
		if updated.RowsAffected == 0 {
			return ipc.NewError(ipc.CodeNotFound, "设备代理不存在")
		}
		payload, err := json.Marshal(auditPayload{Action: "failover", DeviceID: deviceID, Requester: deviceID, Outcome: "allow", Reason: reason, URL: url})
		if err != nil {
			return ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
		}
		auditRow := store.AuditRow{
			TS: s.now(), AssetID: &deviceID, Source: auditSourceFleet, Kind: auditKindDeviceFailover,
			PayloadJSON: string(payload), OwnerID: ipc.OwnerID(ctx),
		}
		if err := q.AuditRow.WithContext(ctx).Create(&auditRow); err != nil {
			return dbError(err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// Close disconnects all device control channels and bridges (service shutdown).
func (s *Service) Close() error {
	s.registry.Close()
	return nil
}
