package sharing

import (
	"context"
	"errors"
	stdsync "sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

const (
	// linkRevalidateTTL is the short TTL for public-link Gate revalidation: block-level revalidation converges to at most
	// once per second per connection, and revocation/shrinkage takes effect at the latest on the next revalidation after the TTL.
	linkRevalidateTTL = time.Second
	// linkInputAuditWindow is the aggregation window for input audit: same-kind events in one window are only counted,
	// so anonymous read_write connections cannot flood audit_log block by block.
	linkInputAuditWindow = time.Minute
)

// Grant is a snapshot of one share authorization: after server-side verification it is handed to the connection path and the agent-side
// Gate, binding the share ID, device, session, permission, and expiry. OwnerID is the device owner
// (distinct from the grantor owner_id of the share row); public links have an empty Recipient.
type Grant struct {
	ShareIDs   []string   `json:"share_ids"`
	DeviceID   string     `json:"device_id"`
	SessionID  string     `json:"session_id,omitempty"`
	OwnerID    string     `json:"owner_id"`
	Recipient  string     `json:"recipient,omitempty"`
	Permission Permission `json:"permission"`
	ExpiresAt  int64      `json:"expires_at"`
}

// Link is a public share link; OwnerID is the grantor (the device owner or the managed
// owner).
type Link struct {
	ID             string
	OwnerID        string
	DeviceID       string
	SessionID      string
	Permission     Permission
	CreatedAt      int64
	ExpiresAt      int64
	RevokedAt      int64
	LastAccessedAt int64
}

// CreateLink creates a public share link for one live terminal session on a daemon device. Read-only by default;
// read_write is granted only when write is explicitly true. The raw token is returned only in this response; only its hash is stored.
func (s *Service) CreateLink(ctx context.Context, identity *account.Identity, deviceID, sessionID string, write bool, ttl time.Duration) (*Link, string, error) {
	if _, err := s.authorize(ctx, identity, deviceID, "create", auditKindLinkCreate); err != nil {
		return nil, "", err
	}
	if !ids.Valid(sessionID) {
		return nil, "", ipc.BadParam(errors.New("会话 ID 不合法"))
	}
	ttl, err := normalizeTTL(ttl, defaultLinkTTL)
	if err != nil {
		return nil, "", err
	}
	if err := s.requireDaemon(ctx, deviceID); err != nil {
		return nil, "", err
	}
	token, err := randomSecret(linkTokenBytes)
	if err != nil {
		return nil, "", err
	}
	permission := permissionFor(write)
	now := s.now()
	link := &Link{
		ID:         ids.New(),
		OwnerID:    identity.UserID,
		DeviceID:   deviceID,
		SessionID:  sessionID,
		Permission: permission,
		CreatedAt:  now,
		ExpiresAt:  now + ttl.Milliseconds(),
	}
	row := store.ShareLink{
		ID: link.ID, OwnerID: link.OwnerID, DeviceID: link.DeviceID, SessionID: link.SessionID,
		TokenHash: hashSecret(token), Permission: string(link.Permission), CreatedAt: link.CreatedAt, ExpiresAt: link.ExpiresAt,
	}
	if err := query.Use(s.db).ShareLink.WithContext(ctx).Create(&row); err != nil {
		return nil, "", dbError(err)
	}
	if err := s.audit(ctx, auditKindLinkCreate, auditPayload{
		Action: "create", ShareID: link.ID, DeviceID: deviceID, SessionID: sessionID,
		OwnerID: link.OwnerID, Requester: identity.UserID, Outcome: "allow", Permission: string(permission),
	}); err != nil {
		return nil, "", err
	}
	return link, token, nil
}

// ResolveLink exchanges a public token for an authorization snapshot; invalid, expired, revoked, or unreachable-device cases
// are all denied with an audit record, without distinguishing invalid from nonexistent.
func (s *Service) ResolveLink(ctx context.Context, token string) (*Grant, error) {
	notFound := func() (*Grant, error) {
		auditErr := s.audit(ctx, auditKindLinkAccess, auditPayload{Action: "access", Requester: "", Outcome: "deny", Reason: "not_found"})
		if auditErr != nil {
			return nil, auditErr
		}
		return nil, ErrTokenNotFound
	}
	shareLink := query.Use(s.db).ShareLink
	row, err := shareLink.WithContext(ctx).Where(shareLink.TokenHash.Eq(hashSecret(token))).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFound()
	}
	if err != nil {
		return nil, dbError(err)
	}
	link := Link{
		ID: row.ID, OwnerID: row.OwnerID, DeviceID: row.DeviceID, SessionID: row.SessionID,
		Permission: Permission(row.Permission), ExpiresAt: row.ExpiresAt,
	}
	userDevice := query.Use(s.db).UserDevice
	device, err := userDevice.WithContext(ctx).Where(userDevice.ID.Eq(link.DeviceID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFound()
	}
	if err != nil {
		return nil, dbError(err)
	}
	deny := func(reason, message string) (*Grant, error) {
		auditErr := s.audit(ctx, auditKindLinkAccess, auditPayload{
			Action: "access", ShareID: link.ID, DeviceID: link.DeviceID, SessionID: link.SessionID,
			OwnerID: link.OwnerID, Requester: "", Outcome: "deny", Reason: reason, Permission: string(link.Permission),
		})
		if auditErr != nil {
			return nil, auditErr
		}
		return nil, ipc.NewError(ipc.CodeForbidden, message)
	}
	switch {
	case row.RevokedAt != nil:
		return deny("revoked", "分享链接已吊销")
	case s.now() >= link.ExpiresAt:
		return deny("expired", "分享链接已过期")
	case device.RevokedAt != nil:
		return deny("device_revoked", "设备已吊销")
	}
	if err := s.requireDaemon(ctx, link.DeviceID); err != nil {
		reason := "daemon_unavailable"
		var appErr *ipc.Error
		if errors.As(err, &appErr) && appErr.Code == ipc.CodeDisconnected {
			reason = "agent_offline"
		}
		auditErr := s.audit(ctx, auditKindLinkAccess, auditPayload{
			Action: "access", ShareID: link.ID, DeviceID: link.DeviceID, SessionID: link.SessionID,
			OwnerID: link.OwnerID, Requester: "", Outcome: "deny", Reason: reason, Permission: string(link.Permission),
		})
		if auditErr != nil {
			return nil, auditErr
		}
		return nil, err
	}
	now := s.now()
	if _, err := shareLink.WithContext(ctx).Where(shareLink.ID.Eq(link.ID)).UpdateColumns(map[string]any{"last_accessed_at": now}); err != nil {
		return nil, dbError(err)
	}
	if err := s.audit(ctx, auditKindLinkAccess, auditPayload{
		Action: "access", ShareID: link.ID, DeviceID: link.DeviceID, SessionID: link.SessionID,
		OwnerID: link.OwnerID, Requester: "", Outcome: "allow", Permission: string(link.Permission),
	}); err != nil {
		return nil, err
	}
	return &Grant{
		ShareIDs:   []string{link.ID},
		DeviceID:   link.DeviceID,
		SessionID:  link.SessionID,
		OwnerID:    device.UserID,
		Permission: link.Permission,
		ExpiresAt:  link.ExpiresAt,
	}, nil
}

// RevokeLink revokes a public link; the owner may do this, and repeated revocation does not error.
// Every decision is recorded in an audit entry carrying the share_id.
func (s *Service) RevokeLink(ctx context.Context, identity *account.Identity, linkID string) error {
	shareLink := query.Use(s.db).ShareLink
	row, err := shareLink.WithContext(ctx).Where(shareLink.ID.Eq(linkID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ipc.NewError(ipc.CodeNotFound, "分享链接不存在")
	}
	if err != nil {
		return dbError(err)
	}
	link := Link{ID: row.ID, OwnerID: row.OwnerID, DeviceID: row.DeviceID, SessionID: row.SessionID}
	outcome, reason := "allow", ""
	var decision error
	if link.OwnerID != identity.UserID {
		_, outcome, reason, decision = s.authorizeDevice(ctx, identity, link.DeviceID)
	}
	auditErr := s.audit(ctx, auditKindLinkRevoke, auditPayload{
		Action: "revoke", ShareID: link.ID, DeviceID: link.DeviceID, SessionID: link.SessionID,
		OwnerID: link.OwnerID, Requester: identity.UserID, Outcome: outcome, Reason: reason,
	})
	if decision != nil {
		return decision
	}
	if auditErr != nil {
		return auditErr
	}
	if _, err := shareLink.WithContext(ctx).Where(shareLink.ID.Eq(linkID), shareLink.RevokedAt.IsNull()).UpdateColumns(map[string]any{"revoked_at": s.now()}); err != nil {
		return dbError(err)
	}
	return nil
}

// ListLinks returns the public links created by the owner; only the owner can see them. Token hashes are not included.
func (s *Service) ListLinks(ctx context.Context, identity *account.Identity) ([]*Link, error) {
	shareLink := query.Use(s.db).ShareLink
	rows, err := shareLink.WithContext(ctx).
		Where(shareLink.OwnerID.Eq(identity.UserID)).
		Order(shareLink.CreatedAt, shareLink.ID).
		Find()
	if err != nil {
		return nil, dbError(err)
	}
	links := make([]*Link, 0, len(rows))
	for _, row := range rows {
		link := &Link{
			ID: row.ID, OwnerID: row.OwnerID, DeviceID: row.DeviceID, SessionID: row.SessionID,
			Permission: Permission(row.Permission), CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
		}
		if row.RevokedAt != nil {
			link.RevokedAt = *row.RevokedAt
		}
		if row.LastAccessedAt != nil {
			link.LastAccessedAt = *row.LastAccessedAt
		}
		links = append(links, link)
	}
	return links, nil
}

// revalidateLink is the internal implementation of RevalidateLink; it additionally returns a machine-readable denial reason
// ("revoked"/"expired"/"device_revoked"/"owner_mismatch"/"not_found") for the input
// path to write audits. Before revalidating it uses checkDeviceBinding to verify current device ownership and revocation state.
func (s *Service) revalidateLink(ctx context.Context, grant *Grant) (*Grant, string, error) {
	if grant == nil || len(grant.ShareIDs) == 0 {
		return nil, "not_found", ipc.NewError(ipc.CodeForbidden, "分享链接无效")
	}
	if reason, err := s.checkDeviceBinding(ctx, grant); err != nil {
		return nil, reason, err
	}
	now := s.now()
	reason := "not_found"
	permission := PermissionRead
	var expiresAt int64
	var validIDs []string
	shareLink := query.Use(s.db).ShareLink
	for _, id := range grant.ShareIDs {
		row, err := shareLink.WithContext(ctx).Where(shareLink.ID.Eq(id)).First()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return nil, "", dbError(err)
		}
		if row.DeviceID != grant.DeviceID || row.SessionID != grant.SessionID {
			continue
		}
		switch {
		case row.RevokedAt != nil:
			reason = "revoked"
		case now >= row.ExpiresAt:
			reason = "expired"
		default:
			validIDs = append(validIDs, id)
			if Permission(row.Permission).AllowsWrite() {
				permission = PermissionReadWrite
			}
			if row.ExpiresAt > expiresAt {
				expiresAt = row.ExpiresAt
			}
		}
	}
	if len(validIDs) == 0 {
		return nil, reason, ipc.NewError(ipc.CodeForbidden, linkDenyMessage(reason))
	}
	refreshed := *grant
	refreshed.ShareIDs = validIDs
	refreshed.Permission = permission
	refreshed.ExpiresAt = expiresAt
	return &refreshed, "", nil
}

// RevalidateLink re-reads link rows by ShareIDs and verifies device/session binding: revoked, expired, or
// binding-mismatched rows are all dropped; if everything is invalid it denies. Returns the refreshed Grant (permission and expiry
// follow the current rows), for use by the connection path and agent recheck, never trusting the snapshot from issuance.
func (s *Service) RevalidateLink(ctx context.Context, grant *Grant) (*Grant, error) {
	refreshed, _, err := s.revalidateLink(ctx, grant)
	return refreshed, err
}

func linkDenyMessage(reason string) string {
	switch reason {
	case "revoked":
		return "分享链接已吊销"
	case "expired":
		return "分享链接已过期"
	case "device_revoked":
		return "设备已吊销"
	case "owner_mismatch":
		return "设备归属与授权不一致"
	case "device_not_found":
		return "设备不存在"
	default:
		return "分享链接无效"
	}
}

// NewLinkGate builds the enforcement Gate for public link connections: per block it refreshes the authorization from current rows
// via RevalidateLink (revocation/expiry/device revocation stops both directions immediately), and the input direction additionally
// goes through RevalidateLinkInput for auditing and read_write enforcement before Write. Revalidation carries a short TTL cache:
// per-block input of anonymous read_write connections no longer hits the database and audit every time, and revocation/shrinkage takes effect at the latest on the next
// revalidation after the TTL. Future connection slices should build their data path through this constructor instead of holding a
// Grant snapshot directly.
func (s *Service) NewLinkGate(grant *Grant, options ...GateOption) *Gate {
	wired := []GateOption{
		WithRevalidateTTL(linkRevalidateTTL),
		WithRecheck(func(ctx context.Context, current *Grant) (*Grant, error) {
			return s.RevalidateLink(ctx, current)
		}),
		WithInputCheck(func(ctx context.Context, current *Grant) (*Grant, error) {
			return s.RevalidateLinkInput(ctx, current)
		}),
	}
	return NewGate(grant, append(wired, options...)...)
}

// RevalidateLinkInput is the server-side enforcement point of the public link input path: every input is re-read from the current
// row (revocation/expiry/binding) and required to be read_write, never trusting the Grant snapshot from issuance;
// every decision is written to the audit log (aggregated by session/time window, see auditLinkInput).
func (s *Service) RevalidateLinkInput(ctx context.Context, grant *Grant) (*Grant, error) {
	refreshed, reason, err := s.revalidateLink(ctx, grant)
	if err == nil && !refreshed.Permission.AllowsWrite() {
		reason = "read_only"
		err = ipc.NewError(ipc.CodeForbidden, "分享链接为只读")
	}
	payload := auditPayload{Action: "input", Requester: "", Outcome: "deny", Reason: reason}
	if grant != nil {
		if len(grant.ShareIDs) > 0 {
			payload.ShareID = grant.ShareIDs[0]
		}
		payload.DeviceID = grant.DeviceID
		payload.SessionID = grant.SessionID
		payload.OwnerID = grant.OwnerID
		payload.Permission = string(grant.Permission)
	}
	if err == nil {
		payload.Outcome = "allow"
		payload.Reason = ""
		payload.Permission = string(refreshed.Permission)
	}
	if auditErr := s.auditInputAggregated(ctx, auditKindLinkInput, payload); auditErr != nil {
		return nil, auditErr
	}
	if err != nil {
		return nil, err
	}
	return refreshed, nil
}

type linkInputAuditPayload struct {
	auditPayload
	Suppressed int64 `json:"suppressed,omitempty"`
}

type linkInputAuditState struct {
	startedAt  int64
	payload    auditPayload
	suppressed int64
}

var linkInputAudits = struct {
	mu      stdsync.Mutex
	windows map[string]*linkInputAuditState
}{windows: map[string]*linkInputAuditState{}}

// auditInputAggregated aggregates input audits by share kind + session/device + time window: within a window,
// same-kind events are only counted without writing rows; after the window expires, same-kind events fold the count into one row; a kind change (e.g.
// allow -> deny) first folds the old window count into a row, then immediately writes one row for the new event, so denials are never delayed.
// Device links have no session ID and aggregate by device (anonymous read_write connections cannot flood audit_log block by block).
func (s *Service) auditInputAggregated(ctx context.Context, kind string, payload auditPayload) error {
	key := payload.SessionID
	if key == "" {
		key = payload.DeviceID
	}
	if key == "" {
		return s.auditInput(ctx, kind, linkInputAuditPayload{auditPayload: payload})
	}
	key = kind + "|" + key
	now := s.now()
	linkInputAudits.mu.Lock()
	state := linkInputAudits.windows[key]
	switch {
	case state == nil:
		if len(linkInputAudits.windows) > 1024 {
			for existing, entry := range linkInputAudits.windows {
				if now-entry.startedAt >= linkInputAuditWindow.Milliseconds() {
					delete(linkInputAudits.windows, existing)
				}
			}
		}
		linkInputAudits.windows[key] = &linkInputAuditState{startedAt: now, payload: payload}
		linkInputAudits.mu.Unlock()
		return s.auditInput(ctx, kind, linkInputAuditPayload{auditPayload: payload})
	case now-state.startedAt < linkInputAuditWindow.Milliseconds() && state.payload.Outcome == payload.Outcome && state.payload.Reason == payload.Reason:
		state.suppressed++
		linkInputAudits.mu.Unlock()
		return nil
	case state.payload.Outcome == payload.Outcome && state.payload.Reason == payload.Reason:
		count := state.suppressed + 1
		state.startedAt, state.suppressed = now, 0
		linkInputAudits.mu.Unlock()
		return s.auditInput(ctx, kind, linkInputAuditPayload{auditPayload: payload, Suppressed: count})
	default:
		flushed := state.payload
		count := state.suppressed
		state.startedAt, state.payload, state.suppressed = now, payload, 0
		linkInputAudits.mu.Unlock()
		if count > 0 {
			if err := s.auditInput(ctx, kind, linkInputAuditPayload{auditPayload: flushed, Suppressed: count}); err != nil {
				return err
			}
		}
		return s.auditInput(ctx, kind, linkInputAuditPayload{auditPayload: payload})
	}
}

func (s *Service) auditInput(ctx context.Context, kind string, payload linkInputAuditPayload) error {
	return s.writeAudit(ctx, kind, payload)
}
