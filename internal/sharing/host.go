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

// HostShare 把一台 daemon 主机分享给一个注册用户: recipient 可在有效期内
// 随时通过 host agent 新建终端, 全程不接触 owner 的密码或密钥。owner_id 是
// 授予者 (设备 owner), 同一 (owner, device, recipient)
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

// CreateHostShare 把设备分享给注册用户。仅设备 owner  可授予;
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

// ListHostShares 返回与 identity 相关的分享 (授予的或接收的); 仅 owner 可见。
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

// RevokeHostShare 吊销主机分享: 授予者、设备 owner  可执行,
// 重复吊销不报错。吊销只影响本行, 同设备的其他分享不受影响。
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
