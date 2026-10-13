package sharing

import (
	"context"
	"errors"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

// DeviceLink shares a daemon device with account-less visitors for a limited time: unlike a
// Link bound to a single live session, the visitor opens a new terminal on the device via the host agent on every open, and the link
// itself is not tied to any existing session. The link is unavailable before not_before (delayed effect); owner_id is the
// grantor (the device owner or the owner they manage).
type DeviceLink struct {
	ID             string
	OwnerID        string
	DeviceID       string
	Permission     Permission
	CreatedAt      int64
	NotBefore      int64
	ExpiresAt      int64
	RevokedAt      int64
	LastAccessedAt int64
}

// CreateDeviceLink creates a public share link for a daemon device. Read-only by default; read_write is granted
// only when write is explicitly true. notBeforeMS is the effect time (unix milliseconds; <=0 means effective immediately,
// anything earlier than now converges to now); the effect time must be earlier than the expiry time. The raw token
// is returned only in this response; only its hash is stored.
func (s *Service) CreateDeviceLink(ctx context.Context, identity *account.Identity, deviceID string, write bool, notBeforeMS int64, ttl time.Duration) (*DeviceLink, string, error) {
	if _, err := s.authorize(ctx, identity, deviceID, "create", auditKindDeviceLinkCreate); err != nil {
		return nil, "", err
	}
	ttl, err := normalizeTTL(ttl, defaultDeviceShareTTL)
	if err != nil {
		return nil, "", err
	}
	now := s.now()
	expiresAt := now + ttl.Milliseconds()
	if notBeforeMS <= now {
		notBeforeMS = now
	}
	if notBeforeMS >= expiresAt {
		return nil, "", ipc.BadParam(errors.New("生效时间必须早于过期时间"))
	}
	if err := s.requireDaemon(ctx, deviceID); err != nil {
		return nil, "", err
	}
	token, err := randomSecret(linkTokenBytes)
	if err != nil {
		return nil, "", err
	}
	permission := permissionFor(write)
	link := &DeviceLink{
		ID:         ids.New(),
		OwnerID:    identity.UserID,
		DeviceID:   deviceID,
		Permission: permission,
		CreatedAt:  now,
		NotBefore:  notBeforeMS,
		ExpiresAt:  expiresAt,
	}
	row := store.DeviceShareLink{
		ID: link.ID, OwnerID: link.OwnerID, DeviceID: link.DeviceID, TokenHash: hashSecret(token),
		Permission: string(link.Permission), CreatedAt: link.CreatedAt, NotBefore: link.NotBefore, ExpiresAt: link.ExpiresAt,
	}
	if err := query.Use(s.db).DeviceShareLink.WithContext(ctx).Create(&row); err != nil {
		return nil, "", dbError(err)
	}
	if err := s.audit(ctx, auditKindDeviceLinkCreate, auditPayload{
		Action: "create", ShareID: link.ID, DeviceID: deviceID,
		OwnerID: link.OwnerID, Requester: identity.UserID, Outcome: "allow", Permission: string(permission),
	}); err != nil {
		return nil, "", err
	}
	return link, token, nil
}

// ResolveDeviceLink exchanges a public token for an authorization snapshot; invalid, not-yet-valid, expired, revoked, or
// unreachable-device cases are all denied with an audit record, without distinguishing invalid from nonexistent. The returned Grant carries no SessionID
// (the data plane creates a new terminal from it instead of attaching to an existing session).
func (s *Service) ResolveDeviceLink(ctx context.Context, token string) (*Grant, error) {
	notFound := func() (*Grant, error) {
		auditErr := s.audit(ctx, auditKindDeviceLinkAccess, auditPayload{Action: "access", Requester: "", Outcome: "deny", Reason: "not_found"})
		if auditErr != nil {
			return nil, auditErr
		}
		return nil, ErrTokenNotFound
	}
	deviceShareLink := query.Use(s.db).DeviceShareLink
	row, err := deviceShareLink.WithContext(ctx).Where(deviceShareLink.TokenHash.Eq(hashSecret(token))).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFound()
	}
	if err != nil {
		return nil, dbError(err)
	}
	link := DeviceLink{
		ID: row.ID, OwnerID: row.OwnerID, DeviceID: row.DeviceID, Permission: Permission(row.Permission),
		CreatedAt: row.CreatedAt, NotBefore: row.NotBefore, ExpiresAt: row.ExpiresAt,
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
		auditErr := s.audit(ctx, auditKindDeviceLinkAccess, auditPayload{
			Action: "access", ShareID: link.ID, DeviceID: link.DeviceID,
			OwnerID: link.OwnerID, Requester: "", Outcome: "deny", Reason: reason, Permission: string(link.Permission),
		})
		if auditErr != nil {
			return nil, auditErr
		}
		return nil, ipc.NewError(ipc.CodeForbidden, message)
	}
	now := s.now()
	switch {
	case row.RevokedAt != nil:
		return deny("revoked", "分享链接已吊销")
	case now >= link.ExpiresAt:
		return deny("expired", "分享链接已过期")
	case now < link.NotBefore:
		return deny("not_yet_valid", "分享链接尚未生效")
	case device.RevokedAt != nil:
		return deny("device_revoked", "设备已吊销")
	}
	if err := s.requireDaemon(ctx, link.DeviceID); err != nil {
		reason := "daemon_unavailable"
		var appErr *ipc.Error
		if errors.As(err, &appErr) && appErr.Code == ipc.CodeDisconnected {
			reason = "agent_offline"
		}
		auditErr := s.audit(ctx, auditKindDeviceLinkAccess, auditPayload{
			Action: "access", ShareID: link.ID, DeviceID: link.DeviceID,
			OwnerID: link.OwnerID, Requester: "", Outcome: "deny", Reason: reason, Permission: string(link.Permission),
		})
		if auditErr != nil {
			return nil, auditErr
		}
		return nil, err
	}
	if _, err := deviceShareLink.WithContext(ctx).Where(deviceShareLink.ID.Eq(link.ID)).UpdateColumns(map[string]any{"last_accessed_at": now}); err != nil {
		return nil, dbError(err)
	}
	if err := s.audit(ctx, auditKindDeviceLinkAccess, auditPayload{
		Action: "access", ShareID: link.ID, DeviceID: link.DeviceID,
		OwnerID: link.OwnerID, Requester: "", Outcome: "allow", Permission: string(link.Permission),
	}); err != nil {
		return nil, err
	}
	return &Grant{
		ShareIDs:   []string{link.ID},
		DeviceID:   link.DeviceID,
		OwnerID:    device.UserID,
		Permission: link.Permission,
		ExpiresAt:  link.ExpiresAt,
	}, nil
}

// RevokeDeviceLink revokes a device public link; the owner may do this, and repeated revocation does not
// error. Revocation affects only this row; session links on the same device and other device links are unaffected.
func (s *Service) RevokeDeviceLink(ctx context.Context, identity *account.Identity, linkID string) error {
	deviceShareLink := query.Use(s.db).DeviceShareLink
	row, err := deviceShareLink.WithContext(ctx).Where(deviceShareLink.ID.Eq(linkID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ipc.NewError(ipc.CodeNotFound, "分享链接不存在")
	}
	if err != nil {
		return dbError(err)
	}
	link := DeviceLink{ID: row.ID, OwnerID: row.OwnerID, DeviceID: row.DeviceID}
	_, outcome, reason, decision := s.authorizeDevice(ctx, identity, link.DeviceID)
	auditErr := s.audit(ctx, auditKindDeviceLinkRevoke, auditPayload{
		Action: "revoke", ShareID: link.ID, DeviceID: link.DeviceID,
		OwnerID: link.OwnerID, Requester: identity.UserID, Outcome: outcome, Reason: reason,
	})
	if decision != nil {
		return decision
	}
	if auditErr != nil {
		return auditErr
	}
	if _, err := deviceShareLink.WithContext(ctx).Where(deviceShareLink.ID.Eq(linkID), deviceShareLink.RevokedAt.IsNull()).UpdateColumns(map[string]any{"revoked_at": s.now()}); err != nil {
		return dbError(err)
	}
	return nil
}

// ListDeviceLinks returns the device public links created by the owner; only the owner can see them. Token hashes are
// not included.
func (s *Service) ListDeviceLinks(ctx context.Context, identity *account.Identity) ([]*DeviceLink, error) {
	deviceShareLink := query.Use(s.db).DeviceShareLink
	rows, err := deviceShareLink.WithContext(ctx).
		Where(deviceShareLink.OwnerID.Eq(identity.UserID)).
		Order(deviceShareLink.CreatedAt, deviceShareLink.ID).
		Find()
	if err != nil {
		return nil, dbError(err)
	}
	links := make([]*DeviceLink, 0, len(rows))
	for _, row := range rows {
		link := &DeviceLink{
			ID: row.ID, OwnerID: row.OwnerID, DeviceID: row.DeviceID, Permission: Permission(row.Permission),
			CreatedAt: row.CreatedAt, NotBefore: row.NotBefore, ExpiresAt: row.ExpiresAt,
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

// revalidateDeviceLink is the internal implementation of RevalidateDeviceLink; it additionally returns a machine-readable
// denial reason ("revoked"/"expired"/"not_yet_valid"/"device_revoked"/
// "owner_mismatch"/"not_found") for the input path to write audits. Before revalidating it uses
// checkDeviceBinding to verify current device ownership and revocation state, then refreshes permission and expiry from the current row
// (permission shrinkage takes effect immediately), never trusting the snapshot from issuance.
func (s *Service) revalidateDeviceLink(ctx context.Context, grant *Grant) (*Grant, string, error) {
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
	deviceShareLink := query.Use(s.db).DeviceShareLink
	for _, id := range grant.ShareIDs {
		row, err := deviceShareLink.WithContext(ctx).Where(deviceShareLink.ID.Eq(id)).First()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return nil, "", dbError(err)
		}
		if row.DeviceID != grant.DeviceID {
			continue
		}
		switch {
		case row.RevokedAt != nil:
			reason = "revoked"
		case now >= row.ExpiresAt:
			reason = "expired"
		case now < row.NotBefore:
			reason = "not_yet_valid"
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
		return nil, reason, ipc.NewError(ipc.CodeForbidden, deviceLinkDenyMessage(reason))
	}
	refreshed := *grant
	refreshed.ShareIDs = validIDs
	refreshed.Permission = permission
	refreshed.ExpiresAt = expiresAt
	return &refreshed, "", nil
}

// RevalidateDeviceLink re-reads device link rows by ShareIDs and verifies device binding: revoked, expired,
// not-yet-valid, or binding-mismatched rows are all dropped; if everything is invalid it denies. Returns the refreshed Grant (permission
// and expiry follow the current rows), for use by the connection path and agent recheck.
func (s *Service) RevalidateDeviceLink(ctx context.Context, grant *Grant) (*Grant, error) {
	refreshed, _, err := s.revalidateDeviceLink(ctx, grant)
	return refreshed, err
}

func deviceLinkDenyMessage(reason string) string {
	switch reason {
	case "revoked":
		return "分享链接已吊销"
	case "expired":
		return "分享链接已过期"
	case "not_yet_valid":
		return "分享链接尚未生效"
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

// NewDeviceLinkGate builds the enforcement Gate for device public link connections: per block it refreshes the authorization
// from current rows via RevalidateDeviceLink (revocation/expiry/not-yet-valid/device revocation stops both directions
// immediately, and permission shrinkage applies to the input direction at once), and the input direction additionally goes through
// RevalidateDeviceLinkInput for auditing and read_write enforcement before Write. Revalidation carries a short TTL cache; idle
// connections are covered by the integration layer's periodic Check.
func (s *Service) NewDeviceLinkGate(grant *Grant, options ...GateOption) *Gate {
	wired := []GateOption{
		WithRevalidateTTL(linkRevalidateTTL),
		WithRecheck(func(ctx context.Context, current *Grant) (*Grant, error) {
			return s.RevalidateDeviceLink(ctx, current)
		}),
		WithInputCheck(func(ctx context.Context, current *Grant) (*Grant, error) {
			return s.RevalidateDeviceLinkInput(ctx, current)
		}),
	}
	return NewGate(grant, append(wired, options...)...)
}

// RevalidateDeviceLinkInput is the server-side enforcement point of the device public link input path: every input
// is re-read from the current row (revocation/expiry/binding) and required to be read_write, never trusting the Grant
// snapshot from issuance; every decision is written to the audit log (aggregated by device/time window, see auditInputAggregated).
func (s *Service) RevalidateDeviceLinkInput(ctx context.Context, grant *Grant) (*Grant, error) {
	refreshed, reason, err := s.revalidateDeviceLink(ctx, grant)
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
		payload.OwnerID = grant.OwnerID
		payload.Permission = string(grant.Permission)
	}
	if err == nil {
		payload.Outcome = "allow"
		payload.Reason = ""
		payload.Permission = string(refreshed.Permission)
	}
	if auditErr := s.auditInputAggregated(ctx, auditKindDeviceLinkInput, payload); auditErr != nil {
		return nil, auditErr
	}
	if err != nil {
		return nil, err
	}
	return refreshed, nil
}
