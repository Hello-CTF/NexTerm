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
	"gorm.io/gorm/clause"
)

// HostShare shares a daemon host with one registered user: the recipient can open new terminals
// through the host agent at any time while the share is valid, never touching the owner's password or keys. owner_id is the
// grantor (the device owner); each (owner, device, recipient)
// triple has exactly one row, and shares from different grantors are independent.
type HostShare struct {
	ID                string
	OwnerID           string
	OwnerUsername     string
	DeviceID          string
	RecipientID       string
	RecipientUsername string
	Permission        Permission
	CreatedAt         int64
	ExpiresAt         int64
	RevokedAt         int64
}

// CreateHostShare shares a device with a registered user. Only the device owner may grant;
// the target device must be a daemon host with its agent online; the recipient is resolved by exact username (case-
// insensitive) and cannot be the device owner themself. Re-granting the same triple refreshes permission and expiry and lifts revocation.
// Device authorization and auditing (authorize) happen before username resolution: unauthorized requests always get 403 with a deny audit,
// never revealing whether the recipient exists.
func (s *Service) CreateHostShare(ctx context.Context, identity *account.Identity, deviceID, recipientUsername string, write bool, ttl time.Duration) (*HostShare, error) {
	grant, err := s.authorize(ctx, identity, deviceID, "create", auditKindHostCreate)
	if err != nil {
		return nil, err
	}
	recipient, err := s.accounts.GetUserByUsername(ctx, recipientUsername)
	if err != nil {
		return nil, err
	}
	if recipient.ID == grant.userID {
		return nil, ipc.BadParam(errors.New("不能分享给设备所有者"))
	}
	owner, err := s.accounts.GetUser(ctx, identity.UserID)
	if err != nil {
		return nil, err
	}
	if recipient.State != account.StateActive {
		return nil, ipc.NewError(ipc.CodeForbidden, "接收者账号不可用")
	}
	ttl, err = normalizeTTL(ttl, defaultHostShareTTL)
	if err != nil {
		return nil, err
	}
	if err := s.requireDaemon(ctx, deviceID); err != nil {
		return nil, err
	}
	permission := permissionFor(write)
	now := s.now()
	expiresAt := now + ttl.Milliseconds()
	share := &HostShare{
		ID:                ids.New(),
		OwnerID:           identity.UserID,
		OwnerUsername:     owner.Username,
		DeviceID:          deviceID,
		RecipientID:       recipient.ID,
		RecipientUsername: recipient.Username,
		Permission:        permission,
		CreatedAt:         now,
		ExpiresAt:         expiresAt,
	}
	hostShare := query.Use(s.db).HostShare
	row := store.HostShare{
		ID: share.ID, OwnerID: share.OwnerID, DeviceID: deviceID, RecipientID: recipient.ID,
		Permission: string(permission), CreatedAt: now, ExpiresAt: expiresAt,
	}
	err = hostShare.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "owner_id"}, {Name: "device_id"}, {Name: "recipient_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"permission", "created_at", "expires_at", "revoked_at"}),
		}).
		Create(&row)
	if err != nil {
		return nil, dbError(err)
	}
	actual, err := hostShare.WithContext(ctx).
		Where(hostShare.OwnerID.Eq(share.OwnerID), hostShare.DeviceID.Eq(deviceID), hostShare.RecipientID.Eq(recipient.ID)).
		First()
	if err != nil {
		return nil, dbError(err)
	}
	share.ID = actual.ID
	if err := s.audit(ctx, auditKindHostCreate, auditPayload{
		Action: "create", ShareID: share.ID, DeviceID: deviceID, OwnerID: share.OwnerID, Recipient: recipient.ID,
		Requester: identity.UserID, Outcome: "allow", Permission: string(permission),
	}); err != nil {
		return nil, err
	}
	return share, nil
}

// ListHostShares returns the shares related to the identity (granted or received); only the owner can see them.
func (s *Service) ListHostShares(ctx context.Context, identity *account.Identity) ([]*HostShare, error) {
	hostShare := query.Use(s.db).HostShare
	rows, err := hostShare.WithContext(ctx).
		Where(hostShare.OwnerID.Eq(identity.UserID)).
		Or(hostShare.RecipientID.Eq(identity.UserID)).
		Order(hostShare.CreatedAt, hostShare.ID).
		Find()
	if err != nil {
		return nil, dbError(err)
	}
	userIDs := make([]string, 0, 2*len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		for _, id := range []string{row.OwnerID, row.RecipientID} {
			if !seen[id] {
				seen[id] = true
				userIDs = append(userIDs, id)
			}
		}
	}
	usernames := map[string]string{}
	if len(userIDs) > 0 {
		appUser := query.Use(s.db).AppUser
		users, err := appUser.WithContext(ctx).Where(appUser.ID.In(userIDs...)).Find()
		if err != nil {
			return nil, dbError(err)
		}
		for _, user := range users {
			usernames[user.ID] = user.Username
		}
	}
	shares := make([]*HostShare, 0, len(rows))
	for _, row := range rows {
		share := &HostShare{
			ID: row.ID, OwnerID: row.OwnerID, OwnerUsername: usernames[row.OwnerID],
			DeviceID: row.DeviceID, RecipientID: row.RecipientID, RecipientUsername: usernames[row.RecipientID],
			Permission: Permission(row.Permission), CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
		}
		if row.RevokedAt != nil {
			share.RevokedAt = *row.RevokedAt
		}
		shares = append(shares, share)
	}
	return shares, nil
}

// RevokeHostShare revokes a host share: the grantor or the device owner may do this,
// and repeated revocation does not error. Revocation affects only this row; other shares on the same device are unaffected.
func (s *Service) RevokeHostShare(ctx context.Context, identity *account.Identity, shareID string) error {
	hostShare := query.Use(s.db).HostShare
	row, err := hostShare.WithContext(ctx).Where(hostShare.ID.Eq(shareID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ipc.NewError(ipc.CodeNotFound, "主机分享不存在")
	}
	if err != nil {
		return dbError(err)
	}
	share := HostShare{ID: row.ID, OwnerID: row.OwnerID, DeviceID: row.DeviceID}
	userDevice := query.Use(s.db).UserDevice
	device, err := userDevice.WithContext(ctx).Where(userDevice.ID.Eq(share.DeviceID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ipc.NewError(ipc.CodeNotFound, "主机分享不存在")
	}
	if err != nil {
		return dbError(err)
	}
	outcome, reason := "allow", ""
	var decision error
	if identity.UserID != share.OwnerID && identity.UserID != device.UserID {
		outcome, reason, decision = "deny", "not_owner", ipc.NewError(ipc.CodeForbidden, "无权限吊销该分享")
	}
	auditErr := s.audit(ctx, auditKindHostRevoke, auditPayload{
		Action: "revoke", ShareID: share.ID, DeviceID: share.DeviceID, OwnerID: share.OwnerID,
		Requester: identity.UserID, Outcome: outcome, Reason: reason,
	})
	if decision != nil {
		return decision
	}
	if auditErr != nil {
		return auditErr
	}
	if _, err := hostShare.WithContext(ctx).Where(hostShare.ID.Eq(shareID), hostShare.RevokedAt.IsNull()).UpdateColumns(map[string]any{"revoked_at": s.now()}); err != nil {
		return dbError(err)
	}
	return nil
}

type hostShareRow struct {
	id         string
	permission Permission
	expiresAt  int64
}

// unionHostShares computes the union of valid share rows by permission level: read_write is only backed by rows still offering
// read_write, and the expiry is the max expires_at of those rows; with no valid read_write
// row it falls back to read (likewise taking the max expiry of read rows). A short-permission row never terminates a longer high-
// permission grant early, and shares from different grantors do not affect each other.
func unionHostShares(rows []hostShareRow) (Permission, int64, []string) {
	permission := PermissionRead
	for _, row := range rows {
		if row.permission.AllowsWrite() {
			permission = PermissionReadWrite
			break
		}
	}
	var expiresAt int64
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.id)
		if row.permission == permission && row.expiresAt > expiresAt {
			expiresAt = row.expiresAt
		}
	}
	return permission, expiresAt, ids
}

// loadValidHostShares reads all unrevoked, unexpired share rows under (device, recipient).
func (s *Service) loadValidHostShares(ctx context.Context, deviceID, recipientID string) ([]hostShareRow, error) {
	hostShare := query.Use(s.db).HostShare
	rows, err := hostShare.WithContext(ctx).
		Where(hostShare.DeviceID.Eq(deviceID), hostShare.RecipientID.Eq(recipientID),
			hostShare.RevokedAt.IsNull(), hostShare.ExpiresAt.Gt(s.now())).
		Order(hostShare.ID).
		Find()
	if err != nil {
		return nil, dbError(err)
	}
	shares := make([]hostShareRow, 0, len(rows))
	for _, row := range rows {
		shares = append(shares, hostShareRow{id: row.ID, permission: Permission(row.Permission), expiresAt: row.ExpiresAt})
	}
	return shares, nil
}

// HostGrant is the registered-recipient authorization snapshot. RecipientSessionID binds the authentication session,
// not the terminal session and never the raw session cookie.
type HostGrant struct {
	Grant
	RecipientSessionID string
}

// AuthorizeTerminalOpen verifies that a registered user may open a new terminal on the device through the host agent:
// the device is not revoked, at least one unexpired unrevoked share exists, and the daemon is online; only when all hold does it
// return the authorization snapshot and write an audit; with multiple valid shares stacked, permission and expiry are computed
// by the level union of unionHostShares.
func (s *Service) AuthorizeTerminalOpen(ctx context.Context, identity *account.Identity, deviceID string) (*HostGrant, error) {
	grant, err := s.loadGrant(ctx, deviceID)
	deny := func(reason string, decision error) (*HostGrant, error) {
		auditErr := s.audit(ctx, auditKindHostTerminal, auditPayload{
			Action: "open_terminal", DeviceID: deviceID, Recipient: identity.UserID,
			Requester: identity.UserID, Outcome: "deny", Reason: reason,
		})
		if auditErr != nil {
			return nil, auditErr
		}
		return nil, decision
	}
	if err != nil {
		var appErr *ipc.Error
		if errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound {
			return deny("not_found", err)
		}
		return nil, err
	}
	if grant.revoked {
		return deny("device_revoked", ipc.NewError(ipc.CodeForbidden, "设备已吊销"))
	}
	if err := s.validateHostRecipientSession(ctx, identity.UserID, identity.SessionID); err != nil {
		return deny("recipient_session", err)
	}
	shares, err := s.loadValidHostShares(ctx, deviceID, identity.UserID)
	if err != nil {
		return nil, err
	}
	if len(shares) == 0 {
		return deny("no_share", ipc.NewError(ipc.CodeForbidden, "没有该设备的有效分享"))
	}
	if err := s.requireDaemon(ctx, deviceID); err != nil {
		reason := "daemon_unavailable"
		var appErr *ipc.Error
		if errors.As(err, &appErr) && appErr.Code == ipc.CodeDisconnected {
			reason = "agent_offline"
		}
		return deny(reason, err)
	}
	permission, expiresAt, shareIDs := unionHostShares(shares)
	if err := s.audit(ctx, auditKindHostTerminal, auditPayload{
		Action: "open_terminal", DeviceID: deviceID, OwnerID: grant.userID, Recipient: identity.UserID,
		Requester: identity.UserID, Outcome: "allow", Permission: string(permission),
	}); err != nil {
		return nil, err
	}
	return &HostGrant{
		Grant: Grant{
			ShareIDs:   shareIDs,
			DeviceID:   deviceID,
			OwnerID:    grant.userID,
			Recipient:  identity.UserID,
			Permission: permission,
			ExpiresAt:  expiresAt,
		},
		RecipientSessionID: identity.SessionID,
	}, nil
}

func (s *Service) validateHostRecipientSession(ctx context.Context, userID, sessionID string) error {
	if userID == "" || sessionID == "" {
		return ipc.NewError(ipc.CodeForbidden, "接收者会话已失效，请重新登录")
	}
	user, err := s.accounts.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if user.State != account.StateActive {
		return ipc.NewError(ipc.CodeForbidden, "接收者账号不可用")
	}
	sessions, err := s.accounts.ListUserSessions(ctx, userID)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if session.ID == sessionID {
			return nil
		}
	}
	return ipc.NewError(ipc.CodeForbidden, "接收者会话已失效，请重新登录")
}

// RevalidateHostAccess re-reads host share rows by ShareIDs and verifies device/recipient/session binding:
// it first validates the authentication session and current device ownership, then drops revoked or expired share rows and
// recomputes the union over the remaining rows. Permission may shrink to read, and the expiry follows the rows still backing that level.
func (s *Service) RevalidateHostAccess(ctx context.Context, grant *HostGrant) (*HostGrant, error) {
	if grant == nil || len(grant.ShareIDs) == 0 || grant.Recipient == "" {
		return nil, ipc.NewError(ipc.CodeForbidden, "没有该设备的有效分享")
	}
	if err := s.validateHostRecipientSession(ctx, grant.Recipient, grant.RecipientSessionID); err != nil {
		return nil, err
	}
	if _, err := s.checkDeviceBinding(ctx, &grant.Grant); err != nil {
		return nil, err
	}
	now := s.now()
	var valid []hostShareRow
	reason := "no_share"
	hostShare := query.Use(s.db).HostShare
	for _, id := range grant.ShareIDs {
		row, err := hostShare.WithContext(ctx).Where(hostShare.ID.Eq(id)).First()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return nil, dbError(err)
		}
		if row.DeviceID != grant.DeviceID || row.RecipientID != grant.Recipient {
			continue
		}
		if row.RevokedAt != nil {
			reason = "revoked"
			continue
		}
		if now >= row.ExpiresAt {
			reason = "expired"
			continue
		}
		valid = append(valid, hostShareRow{id: id, permission: Permission(row.Permission), expiresAt: row.ExpiresAt})
	}
	if len(valid) == 0 {
		return nil, ipc.NewError(ipc.CodeForbidden, hostShareDenyMessage(reason))
	}
	permission, expiresAt, ids := unionHostShares(valid)
	refreshed := *grant
	refreshed.ShareIDs = ids
	refreshed.Permission = permission
	refreshed.ExpiresAt = expiresAt
	return &refreshed, nil
}

func hostShareDenyMessage(reason string) string {
	switch reason {
	case "revoked":
		return "主机分享已吊销"
	case "expired":
		return "主机分享已过期"
	case "device_revoked":
		return "设备已吊销"
	case "owner_mismatch":
		return "设备归属与授权不一致"
	case "device_not_found":
		return "设备不存在"
	default:
		return "没有该设备的有效分享"
	}
}

// NewHostGate builds the enforcement Gate for registered share terminals: per block it refreshes the authorization from current rows
// and the bound authentication session; permission shrinkage (read_write -> read) takes effect immediately - input stops, output
// continues under valid read rows; share/device/session revocation stops both directions.
func (s *Service) NewHostGate(grant *HostGrant, options ...GateOption) *Gate {
	recipientSessionID := grant.RecipientSessionID
	wired := []GateOption{
		WithRecheck(func(ctx context.Context, current *Grant) (*Grant, error) {
			refreshed, err := s.RevalidateHostAccess(ctx, &HostGrant{Grant: *current, RecipientSessionID: recipientSessionID})
			if err != nil {
				return nil, err
			}
			return &refreshed.Grant, nil
		}),
	}
	return NewGate(&grant.Grant, append(wired, options...)...)
}
