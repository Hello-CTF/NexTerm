package sharing

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

// HostShare 把一台 daemon 主机分享给一个注册用户: recipient 可在有效期内
// 随时通过 host agent 新建终端, 全程不接触 owner 的密码或密钥。owner_id 是
// 授予者 (设备 owner 或代管的 superadmin), 同一 (owner, device, recipient)
// 三元组只有一行, 不同授予者的分享相互独立。
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

// CreateHostShare 把设备分享给注册用户。仅设备 owner 或 superadmin 可授予;
// 目标设备必须是 daemon 主机且代理在线; recipient 不能是设备 owner 本人。
// 重复授予同一三元组会刷新权限与有效期并解除吊销。
func (s *Service) CreateHostShare(ctx context.Context, identity *account.Identity, deviceID, recipientID string, write bool, ttl time.Duration) (*HostShare, error) {
	grant, err := s.authorize(ctx, identity, deviceID, "create", auditKindHostCreate)
	if err != nil {
		return nil, err
	}
	if recipientID == grant.userID {
		return nil, ipc.BadParam(errors.New("不能向设备 owner 本人分享"))
	}
	recipient, err := s.accounts.GetUser(ctx, recipientID)
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
		DeviceID:          deviceID,
		RecipientID:       recipientID,
		RecipientUsername: recipient.Username,
		Permission:        permission,
		CreatedAt:         now,
		ExpiresAt:         expiresAt,
	}
	err = s.db.QueryRowContext(ctx, `INSERT INTO host_share(id, owner_id, device_id, recipient_id, permission, created_at, expires_at)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(owner_id, device_id, recipient_id) DO UPDATE SET
permission = excluded.permission, created_at = excluded.created_at, expires_at = excluded.expires_at, revoked_at = NULL
RETURNING id`, share.ID, share.OwnerID, deviceID, recipientID, string(permission), now, expiresAt).Scan(&share.ID)
	if err != nil {
		return nil, dbError(err)
	}
	if err := s.audit(ctx, auditKindHostCreate, auditPayload{
		Action: "create", ShareID: share.ID, DeviceID: deviceID, OwnerID: share.OwnerID, Recipient: recipientID,
		Requester: identity.UserID, Outcome: "allow", Permission: string(permission),
	}); err != nil {
		return nil, err
	}
	return share, nil
}

// ListHostShares 返回与 identity 相关的分享 (授予的或接收的); superadmin
// 返回全部。
func (s *Service) ListHostShares(ctx context.Context, identity *account.Identity) ([]*HostShare, error) {
	query := `SELECT h.id, h.owner_id, h.device_id, h.recipient_id, h.permission, h.created_at, h.expires_at, h.revoked_at,
ou.username, ru.username
FROM host_share h
JOIN app_user ou ON ou.id = h.owner_id
JOIN app_user ru ON ru.id = h.recipient_id`
	args := []any{}
	if identity.Role != account.RoleSuperadmin {
		query += " WHERE h.owner_id = ? OR h.recipient_id = ?"
		args = append(args, identity.UserID, identity.UserID)
	}
	query += " ORDER BY h.created_at, h.id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	var shares []*HostShare
	for rows.Next() {
		var share HostShare
		var revokedAt sql.NullInt64
		if err := rows.Scan(&share.ID, &share.OwnerID, &share.DeviceID, &share.RecipientID, &share.Permission,
			&share.CreatedAt, &share.ExpiresAt, &revokedAt, &share.OwnerUsername, &share.RecipientUsername); err != nil {
			return nil, dbError(err)
		}
		if revokedAt.Valid {
			share.RevokedAt = revokedAt.Int64
		}
		shares = append(shares, &share)
	}
	return shares, rows.Err()
}

// RevokeHostShare 吊销主机分享: 授予者、设备 owner 或 superadmin 可执行,
// 重复吊销不报错。吊销只影响本行, 同设备的其他分享不受影响。
func (s *Service) RevokeHostShare(ctx context.Context, identity *account.Identity, shareID string) error {
	var share HostShare
	var deviceOwnerID string
	var revokedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT h.id, h.owner_id, h.device_id, h.revoked_at, d.user_id
FROM host_share h JOIN user_device d ON d.id = h.device_id WHERE h.id = ?`, shareID).
		Scan(&share.ID, &share.OwnerID, &share.DeviceID, &revokedAt, &deviceOwnerID)
	if errors.Is(err, sql.ErrNoRows) {
		return ipc.NewError(ipc.CodeNotFound, "未找到: 主机分享")
	}
	if err != nil {
		return dbError(err)
	}
	outcome, reason := "allow", ""
	var decision error
	if identity.Role != account.RoleSuperadmin && identity.UserID != share.OwnerID && identity.UserID != deviceOwnerID {
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
	if _, err := s.db.ExecContext(ctx, "UPDATE host_share SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", s.now(), shareID); err != nil {
		return dbError(err)
	}
	return nil
}

// AuthorizeTerminalOpen 校验注册用户能否通过 host agent 在设备上新建终端:
// 设备未吊销、存在至少一个未过期未吊销的分享、daemon 在线, 全部满足才
// 返回授权快照并写审计; 多个有效分享叠加时取最宽权限与最早过期时间。
func (s *Service) AuthorizeTerminalOpen(ctx context.Context, identity *account.Identity, deviceID string) (*Grant, error) {
	grant, err := s.loadGrant(ctx, deviceID)
	deny := func(reason string, decision error) (*Grant, error) {
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
	now := s.now()
	rows, err := s.db.QueryContext(ctx, `SELECT id, permission, expires_at FROM host_share
WHERE device_id = ? AND recipient_id = ? AND revoked_at IS NULL AND expires_at > ? ORDER BY id`, deviceID, identity.UserID, now)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	var shareIDs []string
	permission := PermissionRead
	var expiresAt int64
	for rows.Next() {
		var id string
		var rowPermission string
		var rowExpiresAt int64
		if err := rows.Scan(&id, &rowPermission, &rowExpiresAt); err != nil {
			return nil, dbError(err)
		}
		shareIDs = append(shareIDs, id)
		if Permission(rowPermission).AllowsWrite() {
			permission = PermissionReadWrite
		}
		if expiresAt == 0 || rowExpiresAt < expiresAt {
			expiresAt = rowExpiresAt
		}
	}
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	if len(shareIDs) == 0 {
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
	if err := s.audit(ctx, auditKindHostTerminal, auditPayload{
		Action: "open_terminal", DeviceID: deviceID, OwnerID: grant.userID, Recipient: identity.UserID,
		Requester: identity.UserID, Outcome: "allow", Permission: string(permission),
	}); err != nil {
		return nil, err
	}
	return &Grant{
		ShareIDs:   shareIDs,
		DeviceID:   deviceID,
		OwnerID:    grant.userID,
		Recipient:  identity.UserID,
		Permission: permission,
		ExpiresAt:  expiresAt,
	}, nil
}
