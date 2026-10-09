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

// DeviceLink 把一台 daemon 设备在限定时间内分享给无账号访客: 与绑定单个
// 实时会话的 Link 不同, 访客每次打开都在设备上经 host agent 新建终端, 链接
// 本身不依附任何既有会话。not_before 之前链接不可用 (延迟生效); owner_id 是
// 授予者 (设备 owner 或代管的 superadmin), 设备 owner 与 superadmin 都可吊销。
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

// CreateDeviceLink 为 daemon 设备创建公开分享链接。默认只读; write 显式为
// true 时才授予读写。notBeforeMS 是生效时刻 (unix 毫秒; <=0 表示立即生效,
// 早于当前时刻的一律按当前时刻收敛); 生效时刻必须早于过期时刻。原始 token
// 只在本次返回, 库中只存哈希。
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
	if _, err := s.db.ExecContext(ctx, `INSERT INTO device_share_link(id, owner_id, device_id, token_hash, permission, created_at, not_before, expires_at)
VALUES(?,?,?,?,?,?,?,?)`, link.ID, link.OwnerID, link.DeviceID, hashSecret(token), string(link.Permission), link.CreatedAt, link.NotBefore, link.ExpiresAt); err != nil {
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

// ResolveDeviceLink 用公开 token 换取授权快照; 无效、未生效、过期、已吊销或
// 设备不可达一律拒绝并写审计, 不区分无效与不存在。返回的 Grant 不带 SessionID
// (数据面据此新建终端而不是 attach 既有会话)。
func (s *Service) ResolveDeviceLink(ctx context.Context, token string) (*Grant, error) {
	var link DeviceLink
	var deviceOwnerID string
	var revokedAt, deviceRevokedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT l.id, l.owner_id, l.device_id, l.permission, l.created_at, l.not_before, l.expires_at, l.revoked_at, d.user_id, d.revoked_at
FROM device_share_link l JOIN user_device d ON d.id = l.device_id
WHERE l.token_hash = ?`, hashSecret(token)).
		Scan(&link.ID, &link.OwnerID, &link.DeviceID, &link.Permission, &link.CreatedAt, &link.NotBefore, &link.ExpiresAt, &revokedAt, &deviceOwnerID, &deviceRevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		auditErr := s.audit(ctx, auditKindDeviceLinkAccess, auditPayload{Action: "access", Requester: "", Outcome: "deny", Reason: "not_found"})
		if auditErr != nil {
			return nil, auditErr
		}
		return nil, ErrTokenNotFound
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
	case revokedAt.Valid:
		return deny("revoked", "分享链接已吊销")
	case now >= link.ExpiresAt:
		return deny("expired", "分享链接已过期")
	case now < link.NotBefore:
		return deny("not_yet_valid", "分享链接尚未生效")
	case deviceRevokedAt.Valid:
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
	if _, err := s.db.ExecContext(ctx, "UPDATE device_share_link SET last_accessed_at = ? WHERE id = ?", now, link.ID); err != nil {
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
		OwnerID:    deviceOwnerID,
		Permission: link.Permission,
		ExpiresAt:  link.ExpiresAt,
	}, nil
}

// RevokeDeviceLink 吊销设备公开链接; owner 或 superadmin 可执行, 重复吊销不
// 报错。吊销只影响本行, 同设备的会话链接与其他设备链接不受影响。
func (s *Service) RevokeDeviceLink(ctx context.Context, identity *account.Identity, linkID string) error {
	var link DeviceLink
	var revokedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT id, owner_id, device_id, revoked_at FROM device_share_link WHERE id = ?", linkID).
		Scan(&link.ID, &link.OwnerID, &link.DeviceID, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ipc.NewError(ipc.CodeNotFound, "分享链接不存在")
	}
	if err != nil {
		return dbError(err)
	}
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
	if _, err := s.db.ExecContext(ctx, "UPDATE device_share_link SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", s.now(), linkID); err != nil {
		return dbError(err)
	}
	return nil
}

// ListDeviceLinks 返回 owner 创建的设备公开链接; superadmin 返回全部。不含
// token 哈希。
func (s *Service) ListDeviceLinks(ctx context.Context, identity *account.Identity) ([]*DeviceLink, error) {
	query := `SELECT id, owner_id, device_id, permission, created_at, not_before, expires_at, revoked_at, last_accessed_at FROM device_share_link`
	args := []any{}
	if identity.Role != account.RoleSuperadmin {
		query += " WHERE owner_id = ?"
		args = append(args, identity.UserID)
	}
	query += " ORDER BY created_at, id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	var links []*DeviceLink
	for rows.Next() {
		var link DeviceLink
		var revokedAt, lastAccessedAt sql.NullInt64
		if err := rows.Scan(&link.ID, &link.OwnerID, &link.DeviceID, &link.Permission, &link.CreatedAt, &link.NotBefore, &link.ExpiresAt, &revokedAt, &lastAccessedAt); err != nil {
			return nil, dbError(err)
		}
		if revokedAt.Valid {
			link.RevokedAt = revokedAt.Int64
		}
		if lastAccessedAt.Valid {
			link.LastAccessedAt = lastAccessedAt.Int64
		}
		links = append(links, &link)
	}
	return links, rows.Err()
}

// revalidateDeviceLink 是 RevalidateDeviceLink 的内部实现, 额外返回机器可读
// 的拒绝原因 ("revoked"/"expired"/"not_yet_valid"/"device_revoked"/
// "owner_mismatch"/"not_found"), 供输入路径写审计。重查前先用
// checkDeviceBinding 校验当前设备归属与吊销状态, 再按当前行刷新权限与有效期
// (权限收缩即时生效), 不信任签发时的快照。
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
	for _, id := range grant.ShareIDs {
		var deviceID, rowPermission string
		var notBefore, rowExpiresAt int64
		var revokedAt sql.NullInt64
		err := s.db.QueryRowContext(ctx, `SELECT device_id, permission, not_before, expires_at, revoked_at FROM device_share_link WHERE id = ?`, id).
			Scan(&deviceID, &rowPermission, &notBefore, &rowExpiresAt, &revokedAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, "", dbError(err)
		}
		if deviceID != grant.DeviceID {
			continue
		}
		switch {
		case revokedAt.Valid:
			reason = "revoked"
		case now >= rowExpiresAt:
			reason = "expired"
		case now < notBefore:
			reason = "not_yet_valid"
		default:
			validIDs = append(validIDs, id)
			if Permission(rowPermission).AllowsWrite() {
				permission = PermissionReadWrite
			}
			if rowExpiresAt > expiresAt {
				expiresAt = rowExpiresAt
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

// RevalidateDeviceLink 按 ShareIDs 重查设备链接行并校验设备绑定: 吊销、过期、
// 未生效或绑定不一致的行一律剔除; 全部失效则拒绝。返回刷新后的 Grant (权限
// 与过期时间以当前行为准), 供连接路径与 agent recheck 使用。
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

// NewDeviceLinkGate 为设备公开链接连接建立执行 Gate: 逐块以
// RevalidateDeviceLink 按当前行刷新授权 (吊销/过期/未生效/设备吊销即双向
// 停止, 权限收缩即时作用于输入方向), 输入方向在 Write 前再经
// RevalidateDeviceLinkInput 审计并强制 read_write。重查带短 TTL 缓存, 空闲
// 连接由集成层的周期 Check 兜住。
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

// RevalidateDeviceLinkInput 是设备公开链接输入路径的服务端强制点: 每次输入
// 都按当前行重查 (吊销/过期/绑定) 并要求 read_write, 不信任签发时的 Grant
// 快照; 每次判定都写审计 (按设备/时间窗口聚合, 见 auditInputAggregated)。
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
