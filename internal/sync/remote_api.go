package sync

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// 本文件的视图结构与服务端路由合同一一对应(JSON 标签保持线上 snake_case):
// fleet 对齐 internal/fleet/server/http.go 与 sharing_http.go,
// 账号对齐 internal/server/account_http.go、preview_http.go 与 remote_http.go。

type RemoteFleetOwner struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type RemoteFleetServiceState struct {
	Installed       bool   `json:"installed"`
	Enabled         bool   `json:"enabled"`
	Active          bool   `json:"active"`
	LastReconcileAt int64  `json:"last_reconcile_at,omitempty"`
	LastError       string `json:"last_error,omitempty"`
}

type RemoteFleetAgent struct {
	Platform         string                  `json:"platform"`
	AppVersion       string                  `json:"app_version"`
	DesiredAutostart bool                    `json:"desired_autostart"`
	TerminalEnabled  bool                    `json:"terminal_enabled"`
	CurrentURL       string                  `json:"current_url"`
	ServiceState     RemoteFleetServiceState `json:"service_state"`
	LastSeenAt       int64                   `json:"last_seen_at"`
	StateDigest      string                  `json:"state_digest,omitempty"`
}

type RemoteFleetDevice struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	State      string            `json:"state,omitempty"`
	CreatedAt  int64             `json:"created_at"`
	LastSeenAt int64             `json:"last_seen_at"`
	RevokedAt  int64             `json:"revoked_at"`
	Owner      *RemoteFleetOwner `json:"owner,omitempty"`
	Agent      *RemoteFleetAgent `json:"agent,omitempty"`
}

type RemoteFleetDevices struct {
	Devices []RemoteFleetDevice `json:"devices"`
}

type RemoteMetricsSample struct {
	TS        int64   `json:"ts"`
	CPUPct    float64 `json:"cpu_pct"`
	MemUsed   int64   `json:"mem_used"`
	MemTotal  int64   `json:"mem_total"`
	DiskUsed  int64   `json:"disk_used"`
	DiskTotal int64   `json:"disk_total"`
	UptimeS   int64   `json:"uptime_s"`
}

type RemoteMetricsSamples struct {
	Samples []RemoteMetricsSample `json:"samples"`
}

type RemoteBaseURLEntry struct {
	URL      string `json:"url"`
	Insecure bool   `json:"insecure,omitempty"`
}

type RemoteBaseURLs struct {
	BaseURLs []RemoteBaseURLEntry `json:"base_urls"`
}

type RemoteEnrollCode struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expires_at"`
}

type RemoteHostShare struct {
	ID                string `json:"id"`
	OwnerID           string `json:"owner_id"`
	OwnerUsername     string `json:"owner_username"`
	DeviceID          string `json:"device_id"`
	RecipientID       string `json:"recipient_id"`
	RecipientUsername string `json:"recipient_username"`
	Permission        string `json:"permission"`
	CreatedAt         int64  `json:"created_at"`
	ExpiresAt         int64  `json:"expires_at"`
	RevokedAt         int64  `json:"revoked_at,omitempty"`
}

type RemoteHostShares struct {
	Shares []RemoteHostShare `json:"shares"`
}

type RemoteHostShareInput struct {
	DeviceID          string `json:"deviceId"`
	RecipientUsername string `json:"recipientUsername"`
	Write             bool   `json:"write"`
	TTLMS             int64  `json:"ttlMs"`
}

// RemoteShareLink.Token 仅创建响应携带, 列表/吊销响应不含 token。
type RemoteShareLink struct {
	ID             string `json:"id"`
	OwnerID        string `json:"owner_id"`
	DeviceID       string `json:"device_id"`
	SessionID      string `json:"session_id"`
	Permission     string `json:"permission"`
	CreatedAt      int64  `json:"created_at"`
	ExpiresAt      int64  `json:"expires_at"`
	RevokedAt      int64  `json:"revoked_at,omitempty"`
	LastAccessedAt int64  `json:"last_accessed_at,omitempty"`
	Token          string `json:"token,omitempty"`
}

type RemoteShareLinks struct {
	Links []RemoteShareLink `json:"links"`
}

type RemoteShareLinkInput struct {
	DeviceID  string `json:"deviceId"`
	SessionID string `json:"sessionId"`
	Write     bool   `json:"write"`
	TTLMS     int64  `json:"ttlMs"`
}

type RemotePreviewLink struct {
	ID             string `json:"id"`
	OwnerID        string `json:"owner_id"`
	SessionID      string `json:"session_id"`
	CreatedAt      int64  `json:"created_at"`
	ExpiresAt      int64  `json:"expires_at"`
	RevokedAt      int64  `json:"revoked_at,omitempty"`
	LastAccessedAt int64  `json:"last_accessed_at,omitempty"`
}

type RemotePreviewLinks struct {
	Links []RemotePreviewLink `json:"links"`
}

// RemotePreviewLinkCreate 是 POST /share/previews 的创建响应: 唯一携带一次性
// token 的入口, token 只用于当场拼公开 URL, 不持久化。
type RemotePreviewLinkCreate struct {
	RemotePreviewLink
	Token string `json:"token"`
}

// RemoteSession 是 POST /share/remote-sessions 的建壳响应: session_id 既是
// 推流 (feed)/删除路径里的 {id}, 也直接用于 POST /share/previews 建只读预览。
type RemoteSession struct {
	SessionID string `json:"session_id"`
	Cols      uint32 `json:"cols"`
	Rows      uint32 `json:"rows"`
}

type RemoteAccountDevice struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	RevokedAt  int64  `json:"revoked_at"`
}

type RemoteAccountDevices struct {
	Devices []RemoteAccountDevice `json:"devices"`
}

type RemoteAccountUser struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	DisplayName        string `json:"display_name"`
	Role               string `json:"role"`
	State              string `json:"state"`
	MustChangePassword bool   `json:"must_change_password"`
	MFAEnabled         bool   `json:"mfa_enabled"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
	LastLoginAt        int64  `json:"last_login_at"`
}

type RemoteAccountUsers struct {
	Users []RemoteAccountUser `json:"users"`
}

type RemoteAccountMe struct {
	User        RemoteAccountUser `json:"user"`
	MFARequired bool              `json:"mfa_required"`
}

type RemoteAccountUserResult struct {
	User RemoteAccountUser `json:"user"`
}

// fleet: 设备与注册码。

func (c *remoteClient) ListDevices(ctx context.Context) (*RemoteFleetDevices, error) {
	var view RemoteFleetDevices
	if err := c.api(ctx, http.MethodGet, "/fleet/devices", nil, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

func (c *remoteClient) DeviceMetrics(ctx context.Context, deviceID string, sinceMS int64) (*RemoteMetricsSamples, error) {
	path := "/fleet/devices/" + url.PathEscape(deviceID) + "/metrics"
	if sinceMS > 0 {
		path += "?since_ms=" + strconv.FormatInt(sinceMS, 10)
	}
	var view RemoteMetricsSamples
	if err := c.api(ctx, http.MethodGet, path, nil, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

func (c *remoteClient) RevokeDevice(ctx context.Context, deviceID string) error {
	return c.api(ctx, http.MethodPost, "/fleet/devices/"+url.PathEscape(deviceID)+"/revoke", nil, nil)
}

func (c *remoteClient) ApproveDevice(ctx context.Context, deviceID string) error {
	return c.api(ctx, http.MethodPost, "/fleet/devices/"+url.PathEscape(deviceID)+"/approve", nil, nil)
}

func (c *remoteClient) RejectDevice(ctx context.Context, deviceID string) error {
	return c.api(ctx, http.MethodPost, "/fleet/devices/"+url.PathEscape(deviceID)+"/reject", nil, nil)
}

func (c *remoteClient) SetDeviceAutostart(ctx context.Context, deviceID string, desired bool) error {
	return c.api(ctx, http.MethodPost, "/fleet/devices/"+url.PathEscape(deviceID)+"/autostart", map[string]bool{"desired": desired}, nil)
}

func (c *remoteClient) CreateEnrollCode(ctx context.Context, ttlMS int64, userID string) (*RemoteEnrollCode, error) {
	payload := map[string]any{"ttl_ms": ttlMS}
	if userID != "" {
		payload["user_id"] = userID
	}
	var view RemoteEnrollCode
	if err := c.api(ctx, http.MethodPost, "/device/enroll-codes", payload, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

func (c *remoteClient) GetBaseURLs(ctx context.Context) (*RemoteBaseURLs, error) {
	var view RemoteBaseURLs
	if err := c.api(ctx, http.MethodGet, "/fleet/base-urls", nil, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

func (c *remoteClient) PutBaseURLs(ctx context.Context, entries []RemoteBaseURLEntry) (*RemoteBaseURLs, error) {
	var view RemoteBaseURLs
	if err := c.api(ctx, http.MethodPut, "/fleet/base-urls", map[string]any{"base_urls": entries}, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

// sharing: 主机分享、会话公开链接与只读预览。

func (c *remoteClient) ListHostShares(ctx context.Context) (*RemoteHostShares, error) {
	var view RemoteHostShares
	if err := c.api(ctx, http.MethodGet, "/share/host-shares", nil, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

func (c *remoteClient) CreateHostShare(ctx context.Context, input RemoteHostShareInput) (*RemoteHostShare, error) {
	payload := map[string]any{
		"device_id":          input.DeviceID,
		"recipient_username": input.RecipientUsername,
		"write":              input.Write,
		"ttl_ms":             input.TTLMS,
	}
	var view RemoteHostShare
	if err := c.api(ctx, http.MethodPost, "/share/host-shares", payload, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

func (c *remoteClient) RevokeHostShare(ctx context.Context, id string) error {
	return c.api(ctx, http.MethodPost, "/share/host-shares/"+url.PathEscape(id)+"/revoke", nil, nil)
}

func (c *remoteClient) ListShareLinks(ctx context.Context) (*RemoteShareLinks, error) {
	var view RemoteShareLinks
	if err := c.api(ctx, http.MethodGet, "/share/links", nil, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

func (c *remoteClient) CreateShareLink(ctx context.Context, input RemoteShareLinkInput) (*RemoteShareLink, error) {
	payload := map[string]any{
		"device_id":  input.DeviceID,
		"session_id": input.SessionID,
		"write":      input.Write,
		"ttl_ms":     input.TTLMS,
	}
	var view RemoteShareLink
	if err := c.api(ctx, http.MethodPost, "/share/links", payload, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

func (c *remoteClient) RevokeShareLink(ctx context.Context, id string) error {
	return c.api(ctx, http.MethodPost, "/share/links/"+url.PathEscape(id)+"/revoke", nil, nil)
}

func (c *remoteClient) ListPreviewLinks(ctx context.Context) (*RemotePreviewLinks, error) {
	var view RemotePreviewLinks
	if err := c.api(ctx, http.MethodGet, "/share/previews", nil, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

func (c *remoteClient) RevokePreviewLink(ctx context.Context, id string) error {
	return c.api(ctx, http.MethodPost, "/share/previews/"+url.PathEscape(id)+"/revoke", nil, nil)
}

// 远程会话壳: 桌面端只读预览的数据面 — 壳是无本地进程的服务端终端标签页,
// 输出由 RemoteSessionFeedDial 的推流 WS 喂入。

func (c *remoteClient) RemoteSessionCreate(ctx context.Context, title, assetID string, cols, rows uint32) (*RemoteSession, error) {
	payload := map[string]any{"title": title, "asset_id": assetID, "cols": cols, "rows": rows}
	var view RemoteSession
	if err := c.api(ctx, http.MethodPost, "/share/remote-sessions", payload, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

func (c *remoteClient) RemoteSessionDelete(ctx context.Context, id string) error {
	return c.api(ctx, http.MethodDelete, "/share/remote-sessions/"+url.PathEscape(id), nil, nil)
}

func (c *remoteClient) CreatePreviewLink(ctx context.Context, sessionID string, ttlMS int64) (*RemotePreviewLinkCreate, error) {
	payload := map[string]any{"session_id": sessionID, "ttl_ms": ttlMS}
	var view RemotePreviewLinkCreate
	if err := c.api(ctx, http.MethodPost, "/share/previews", payload, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

// account: 自己的设备、管理员用户管理与改密。

func (c *remoteClient) Me(ctx context.Context) (*RemoteAccountMe, error) {
	var view RemoteAccountMe
	if err := c.api(ctx, http.MethodGet, "/auth/me", nil, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

func (c *remoteClient) ListMyDevices(ctx context.Context) (*RemoteAccountDevices, error) {
	var view RemoteAccountDevices
	if err := c.api(ctx, http.MethodGet, "/auth/devices", nil, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

func (c *remoteClient) RevokeMyDevice(ctx context.Context, deviceID string) error {
	return c.api(ctx, http.MethodDelete, "/auth/devices/"+url.PathEscape(deviceID), nil, nil)
}

func (c *remoteClient) AdminListUsers(ctx context.Context) (*RemoteAccountUsers, error) {
	var view RemoteAccountUsers
	if err := c.api(ctx, http.MethodGet, "/admin/users", nil, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

func (c *remoteClient) AdminCreateUser(ctx context.Context, username, password, displayName string) (*RemoteAccountUserResult, error) {
	payload := map[string]string{"username": username, "password": password, "display_name": displayName}
	var view RemoteAccountUserResult
	if err := c.api(ctx, http.MethodPost, "/admin/users", payload, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

// AdminSetUserState 映射服务端仅有的两个状态动作: disabled → /disable, reset_required → /reset。
func (c *remoteClient) AdminSetUserState(ctx context.Context, userID, state string) error {
	var action string
	switch state {
	case "disabled":
		action = "disable"
	case accountStateResetRequired:
		action = "reset"
	default:
		return ipc.NewError(ipc.CodeBadParam, "不支持的目标账号状态(仅支持 disabled / reset_required)")
	}
	return c.api(ctx, http.MethodPost, "/admin/users/"+url.PathEscape(userID)+"/"+action, nil, nil)
}

// changeMyPassword 提交改密与新 DEK 信封; 信封由调用方用新旧口令重包裹生成。
func (c *remoteClient) changeMyPassword(ctx context.Context, oldPassword, newPassword string, envelopes *vault.UserDEKEnvelopes) error {
	payload := map[string]any{
		"old_password":      oldPassword,
		"new_password":      newPassword,
		"dek_envelope":      envelopes.DEKEnvelope,
		"kdf_salt":          envelopes.KDFSalt,
		"kdf_params":        envelopes.KDFParams,
		"recovery_envelope": envelopes.RecoveryEnvelope,
		"recovery_hash":     envelopes.RecoveryHash,
	}
	return c.api(ctx, http.MethodPost, "/auth/password", payload, nil)
}

// Healthz 读 /healthz 的服务端版本; 该路由公开, 无需会话。
func (c *remoteClient) Healthz(ctx context.Context) (string, error) {
	var view struct {
		Version string `json:"version"`
	}
	if err := c.api(ctx, http.MethodGet, "/healthz", nil, &view); err != nil {
		return "", err
	}
	return view.Version, nil
}
