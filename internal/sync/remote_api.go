package sync

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// View structs in this file correspond one-to-one with the server route contracts (JSON tags keep the wire snake_case):
// fleet aligns with internal/fleet/server/http.go and sharing_http.go,
// account aligns with internal/server/account_http.go, preview_http.go, and remote_http.go.

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
	CreatedAt  int64             `json:"created_at"`
	LastSeenAt int64             `json:"last_seen_at"`
	RevokedAt  int64             `json:"revoked_at"`
	Owner      *RemoteFleetOwner `json:"owner,omitempty"`
	Agent      *RemoteFleetAgent `json:"agent,omitempty"`
}

type RemoteFleetDevices struct {
	Devices []RemoteFleetDevice `json:"devices"`
	// ServerVersion is empty when talking to an older server that does not report it.
	ServerVersion string `json:"server_version,omitempty"`
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
	Samples          []RemoteMetricsSample `json:"samples"`
	Latest           *RemoteMetricsSample  `json:"latest,omitempty"`
	WindowStart      int64                 `json:"windowStart,omitempty"`
	WindowEnd        int64                 `json:"windowEnd,omitempty"`
	SampleCount      int64                 `json:"sampleCount,omitempty"`
	Downsampled      bool                  `json:"downsampled,omitempty"`
	SeriesError      string                `json:"seriesError,omitempty"`
	SampleIntervalMS int64                 `json:"sampleIntervalMs,omitempty"`
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

// RemotePreviewLinkCreate is the POST /share/previews create response: the only entry
// carrying the one-time token, which is used on the spot to build the public URL and is never persisted.
type RemotePreviewLinkCreate struct {
	RemotePreviewLink
	Token string `json:"token"`
}

// RemoteSession is the POST /share/remote-sessions shell-creation response: session_id serves as
// the {id} in the feed/delete paths and is also used directly for POST /share/previews to create a read-only preview.
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

// fleet: devices and enrollment codes.

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

func (c *remoteClient) SetDeviceAutostart(ctx context.Context, deviceID string, desired bool) error {
	return c.api(ctx, http.MethodPost, "/fleet/devices/"+url.PathEscape(deviceID)+"/autostart", map[string]bool{"desired": desired}, nil)
}

func (c *remoteClient) SetDeviceTerminalAccess(ctx context.Context, deviceID string, enabled bool) error {
	return c.api(ctx, http.MethodPost, "/fleet/devices/"+url.PathEscape(deviceID)+"/terminal-access", map[string]bool{"enabled": enabled}, nil)
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

// sharing: host shares, legacy session links, and read-only previews.

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

// Remote session shells: the data plane of the desktop read-only preview - a shell is a server-side terminal tab with no local process,
// fed output from the RemoteSessionFeedDial streaming WS.

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

// account: own devices, admin user management, and password change.

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

// AdminSetUserState maps the server's only two state actions: disabled -> /disable, reset_required -> /reset.
func (c *remoteClient) AdminSetUserState(ctx context.Context, userID, state string) error {
	var action string
	switch state {
	case "disabled":
		action = "disable"
	case accountStateResetRequired:
		action = "reset"
	default:
		return ipc.NewError(ipc.CodeBadParam, "不支持的目标账号状态（仅支持 disabled / reset_required）")
	}
	return c.api(ctx, http.MethodPost, "/admin/users/"+url.PathEscape(userID)+"/"+action, nil, nil)
}

// changeMyPassword submits the password change with the new DEK envelope; the caller generates the envelope by re-wrapping with the old and new passwords.
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

// Healthz reads the server version from /healthz; the route is public and needs no session.
func (c *remoteClient) Healthz(ctx context.Context) (string, error) {
	var view struct {
		Version string `json:"version"`
	}
	if err := c.api(ctx, http.MethodGet, "/healthz", nil, &view); err != nil {
		return "", err
	}
	return view.Version, nil
}
