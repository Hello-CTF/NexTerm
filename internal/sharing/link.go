package sharing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	stdsync "sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

const (
	// linkRevalidateTTL 是公开链接 Gate 重查的短 TTL: 块级重查收敛为每秒
	// 每连接至多一次, 吊销/收缩最迟在 TTL 后的下一次重查生效。
	linkRevalidateTTL = time.Second
	// linkInputAuditWindow 是输入审计的聚合窗口: 同窗同类事件只计数,
	// 匿名 read_write 连接无法逐块刷爆 audit_log。
	linkInputAuditWindow = time.Minute
)

// Grant 是一次分享授权的快照: 服务端校验通过后交给连接路径与 agent 端
// Gate, 绑定分享 ID、设备、会话、权限与过期时间。OwnerID 是设备 owner
// (区别于 share 行的授予者 owner_id); 公开链接 Recipient 为空。
type Grant struct {
	ShareIDs   []string   `json:"share_ids"`
	DeviceID   string     `json:"device_id"`
	SessionID  string     `json:"session_id,omitempty"`
	OwnerID    string     `json:"owner_id"`
	Recipient  string     `json:"recipient,omitempty"`
	Permission Permission `json:"permission"`
	ExpiresAt  int64      `json:"expires_at"`
}

// Link 是一条公开分享链接; OwnerID 是授予者 (设备 owner 或代管的
// superadmin), 设备 owner 与 superadmin 都可吊销。
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

// CreateLink 为 daemon 设备上的一个实时终端会话创建公开分享链接。默认只读;
// write 显式为 true 时才授予读写。原始 token 只在本次返回, 库中只存哈希。
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
	if _, err := s.db.ExecContext(ctx, `INSERT INTO share_link(id, owner_id, device_id, session_id, token_hash, permission, created_at, expires_at)
VALUES(?,?,?,?,?,?,?,?)`, link.ID, link.OwnerID, link.DeviceID, link.SessionID, hashSecret(token), string(link.Permission), link.CreatedAt, link.ExpiresAt); err != nil {
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

// ResolveLink 用公开 token 换取授权快照; 无效、过期、已吊销或设备不可达
// 一律拒绝并写审计, 不区分无效与不存在。
func (s *Service) ResolveLink(ctx context.Context, token string) (*Grant, error) {
	var link Link
	var deviceOwnerID string
	var revokedAt, deviceRevokedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT l.id, l.owner_id, l.device_id, l.session_id, l.permission, l.expires_at, l.revoked_at, d.user_id, d.revoked_at
FROM share_link l JOIN user_device d ON d.id = l.device_id
WHERE l.token_hash = ?`, hashSecret(token)).
		Scan(&link.ID, &link.OwnerID, &link.DeviceID, &link.SessionID, &link.Permission, &link.ExpiresAt, &revokedAt, &deviceOwnerID, &deviceRevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		auditErr := s.audit(ctx, auditKindLinkAccess, auditPayload{Action: "access", Requester: "", Outcome: "deny", Reason: "not_found"})
		if auditErr != nil {
			return nil, auditErr
		}
		return nil, ErrTokenNotFound
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
	case revokedAt.Valid:
		return deny("revoked", "分享链接已吊销")
	case s.now() >= link.ExpiresAt:
		return deny("expired", "分享链接已过期")
	case deviceRevokedAt.Valid:
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
	if _, err := s.db.ExecContext(ctx, "UPDATE share_link SET last_accessed_at = ? WHERE id = ?", now, link.ID); err != nil {
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
		OwnerID:    deviceOwnerID,
		Permission: link.Permission,
		ExpiresAt:  link.ExpiresAt,
	}, nil
}

// RevokeLink 吊销公开链接; owner 或 superadmin 可执行, 重复吊销不报错。
// 每次判定都写含 share_id 的审计。
func (s *Service) RevokeLink(ctx context.Context, identity *account.Identity, linkID string) error {
	var link Link
	var revokedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT id, owner_id, device_id, session_id, revoked_at FROM share_link WHERE id = ?", linkID).
		Scan(&link.ID, &link.OwnerID, &link.DeviceID, &link.SessionID, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ipc.NewError(ipc.CodeNotFound, "分享链接不存在")
	}
	if err != nil {
		return dbError(err)
	}
	_, outcome, reason, decision := s.authorizeDevice(ctx, identity, link.DeviceID)
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
	if _, err := s.db.ExecContext(ctx, "UPDATE share_link SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", s.now(), linkID); err != nil {
		return dbError(err)
	}
	return nil
}

// ListLinks 返回 owner 创建的公开链接; superadmin 返回全部。不含 token 哈希。
func (s *Service) ListLinks(ctx context.Context, identity *account.Identity) ([]*Link, error) {
	query := `SELECT id, owner_id, device_id, session_id, permission, created_at, expires_at, revoked_at, last_accessed_at FROM share_link`
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
	var links []*Link
	for rows.Next() {
		var link Link
		var revokedAt, lastAccessedAt sql.NullInt64
		if err := rows.Scan(&link.ID, &link.OwnerID, &link.DeviceID, &link.SessionID, &link.Permission, &link.CreatedAt, &link.ExpiresAt, &revokedAt, &lastAccessedAt); err != nil {
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

// revalidateLink 是 RevalidateLink 的内部实现, 额外返回机器可读的拒绝原因
// ("revoked"/"expired"/"device_revoked"/"owner_mismatch"/"not_found"), 供输入
// 路径写审计。重查前先用 checkDeviceBinding 校验当前设备归属与吊销状态。
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
	for _, id := range grant.ShareIDs {
		var deviceID, sessionID, rowPermission string
		var rowExpiresAt int64
		var revokedAt sql.NullInt64
		err := s.db.QueryRowContext(ctx, `SELECT device_id, session_id, permission, expires_at, revoked_at FROM share_link WHERE id = ?`, id).
			Scan(&deviceID, &sessionID, &rowPermission, &rowExpiresAt, &revokedAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, "", dbError(err)
		}
		if deviceID != grant.DeviceID || sessionID != grant.SessionID {
			continue
		}
		switch {
		case revokedAt.Valid:
			reason = "revoked"
		case now >= rowExpiresAt:
			reason = "expired"
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
		return nil, reason, ipc.NewError(ipc.CodeForbidden, linkDenyMessage(reason))
	}
	refreshed := *grant
	refreshed.ShareIDs = validIDs
	refreshed.Permission = permission
	refreshed.ExpiresAt = expiresAt
	return &refreshed, "", nil
}

// RevalidateLink 按 ShareIDs 重查链接行并校验设备/会话绑定: 吊销、过期或
// 绑定不一致的行一律剔除; 全部失效则拒绝。返回刷新后的 Grant (权限与过期
// 时间以当前行为准), 供连接路径与 agent recheck 使用, 不信任签发时快照。
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

// NewLinkGate 为公开链接连接建立执行 Gate: 逐块以 RevalidateLink 按当前行
// 刷新授权 (吊销/过期/设备吊销即双向停止), 输入方向在 Write 前再经
// RevalidateLinkInput 审计并强制 read_write。重查带短 TTL 缓存: 匿名
// read_write 连接的每块输入不再都打到数据库与审计, 吊销/收缩最迟在 TTL 后的
// 下一次重查生效。后续连接切片应通过本构造建立数据通路, 而不是直接持有
// Grant 快照。
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

// RevalidateLinkInput 是公开链接输入路径的服务端强制点: 每次输入都按当前
// 行重查 (吊销/过期/绑定) 并要求 read_write, 不信任签发时的 Grant 快照;
// 每次判定都写审计 (按会话/时间窗口聚合, 见 auditLinkInput)。
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

// auditInputAggregated 按分享类别 + 会话/设备 + 时间窗口聚合输入审计: 窗口内
// 同类事件只计数不落行; 窗口过期后的同类事件把计数折算成一行; 类别变化 (如
// allow -> deny) 先把旧窗口计数折算落行, 再立即落新事件的一行, 拒绝不被延迟。
// 设备链接没有会话 ID, 按设备聚合 (匿名 read_write 连接无法逐块刷爆 audit_log)。
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
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO audit_log(ts, session_id, asset_id, source, kind, payload_json, exit_code, duration_ms)
VALUES(?,NULL,NULL,?,?,?,NULL,NULL)`, s.now(), auditSourceSharing, kind, string(encoded)); err != nil {
		return dbError(err)
	}
	return nil
}
