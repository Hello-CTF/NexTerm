package sharing

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
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
// 目标设备必须是 daemon 主机且代理在线; recipient 按用户名精确解析 (大小写
// 不敏感), 不能是设备 owner 本人。重复授予同一三元组会刷新权限与有效期并解除吊销。
// 设备授权与审计 (authorize) 先于用户名解析: 越权请求一律 403 并写 deny 审计,
// 不泄露接收者是否存在。
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
	err = s.db.QueryRowContext(ctx, `INSERT INTO host_share(id, owner_id, device_id, recipient_id, permission, created_at, expires_at)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(owner_id, device_id, recipient_id) DO UPDATE SET
permission = excluded.permission, created_at = excluded.created_at, expires_at = excluded.expires_at, revoked_at = NULL
RETURNING id`, share.ID, share.OwnerID, deviceID, recipient.ID, string(permission), now, expiresAt).Scan(&share.ID)
	if err != nil {
		return nil, dbError(err)
	}
	if err := s.audit(ctx, auditKindHostCreate, auditPayload{
		Action: "create", ShareID: share.ID, DeviceID: deviceID, OwnerID: share.OwnerID, Recipient: recipient.ID,
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
		return ipc.NewError(ipc.CodeNotFound, "主机分享不存在")
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

type hostShareRow struct {
	id         string
	permission Permission
	expiresAt  int64
}

// unionHostShares 在有效分享行上按权限级别计算并集: read_write 只由仍提供
// read_write 的行支撑, 有效期取这些行的最大 expires_at; 无有效 read_write
// 行时回退 read (同样取 read 行的最大有效期)。短权限行不会提前终止更长的高
// 权限授权, 不同授予者的分享互不影响。
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

// loadValidHostShares 读取 (device, recipient) 下全部未吊销未过期的分享行。
func (s *Service) loadValidHostShares(ctx context.Context, deviceID, recipientID string) ([]hostShareRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, permission, expires_at FROM host_share
WHERE device_id = ? AND recipient_id = ? AND revoked_at IS NULL AND expires_at > ? ORDER BY id`, deviceID, recipientID, s.now())
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	var shares []hostShareRow
	for rows.Next() {
		var share hostShareRow
		var permission string
		if err := rows.Scan(&share.id, &permission, &share.expiresAt); err != nil {
			return nil, dbError(err)
		}
		share.permission = Permission(permission)
		shares = append(shares, share)
	}
	return shares, rows.Err()
}

// AuthorizeTerminalOpen 校验注册用户能否通过 host agent 在设备上新建终端:
// 设备未吊销、存在至少一个未过期未吊销的分享、daemon 在线, 全部满足才
// 返回授权快照并写审计; 多个有效分享叠加时按 unionHostShares 的级别并集
// 计算权限与有效期。
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
	return &Grant{
		ShareIDs:   shareIDs,
		DeviceID:   deviceID,
		OwnerID:    grant.userID,
		Recipient:  identity.UserID,
		Permission: permission,
		ExpiresAt:  expiresAt,
	}, nil
}

// RevalidateHostAccess 按 ShareIDs 重查 host 分享行并校验设备/接收者绑定:
// 先用 checkDeviceBinding 校验当前设备归属与吊销状态, 再剔除吊销或过期的
// 分享行并对剩余行重算并集 (权限可能收缩为 read, 有效期以仍支撑该权限级别
// 的行为准); 全部失效则拒绝。返回刷新后的 Grant, 供服务端输入检查与 agent
// recheck 共用, 不信任签发时快照。
func (s *Service) RevalidateHostAccess(ctx context.Context, grant *Grant) (*Grant, error) {
	if grant == nil || len(grant.ShareIDs) == 0 || grant.Recipient == "" {
		return nil, ipc.NewError(ipc.CodeForbidden, "没有该设备的有效分享")
	}
	if _, err := s.checkDeviceBinding(ctx, grant); err != nil {
		return nil, err
	}
	now := s.now()
	var valid []hostShareRow
	reason := "no_share"
	for _, id := range grant.ShareIDs {
		var deviceID, recipientID, permission string
		var expiresAt int64
		var revokedAt sql.NullInt64
		err := s.db.QueryRowContext(ctx, `SELECT device_id, recipient_id, permission, expires_at, revoked_at FROM host_share WHERE id = ?`, id).
			Scan(&deviceID, &recipientID, &permission, &expiresAt, &revokedAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, dbError(err)
		}
		if deviceID != grant.DeviceID || recipientID != grant.Recipient {
			continue
		}
		if revokedAt.Valid {
			reason = "revoked"
			continue
		}
		if now >= expiresAt {
			reason = "expired"
			continue
		}
		valid = append(valid, hostShareRow{id: id, permission: Permission(permission), expiresAt: expiresAt})
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

// NewHostGate 为注册分享终端建立执行 Gate: 逐块以 RevalidateHostAccess 按
// 当前行刷新授权; 权限收缩 (read_write -> read) 即时生效 — 输入停止, 输出
// 在有效 read 行下继续; 分享或设备吊销即双向停止。后续连接切片应通过本
// 构造建立数据通路, 而不是直接持有 Grant 快照。
func (s *Service) NewHostGate(grant *Grant, options ...GateOption) *Gate {
	wired := []GateOption{
		WithRecheck(func(ctx context.Context, current *Grant) (*Grant, error) {
			return s.RevalidateHostAccess(ctx, current)
		}),
	}
	return NewGate(grant, append(wired, options...)...)
}
