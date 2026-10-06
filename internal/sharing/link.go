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
		return nil, ipc.NewError(ipc.CodeForbidden, "分享链接无效")
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
func (s *Service) RevokeLink(ctx context.Context, identity *account.Identity, linkID string) error {
	var link Link
	var revokedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT id, owner_id, device_id, session_id, revoked_at FROM share_link WHERE id = ?", linkID).
		Scan(&link.ID, &link.OwnerID, &link.DeviceID, &link.SessionID, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ipc.NewError(ipc.CodeNotFound, "未找到: 分享链接")
	}
	if err != nil {
		return dbError(err)
	}
	if _, err := s.authorize(ctx, identity, link.DeviceID, "revoke", auditKindLinkRevoke); err != nil {
		return err
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

// CheckLinkInput 在服务端强制链接的读写范围: 只读链接的输入一律拒绝并写
// 审计; 读写链接放行并记录。过期授权在此同样被拒。
func (s *Service) CheckLinkInput(ctx context.Context, grant *Grant) error {
	allowed := grant != nil && grant.Permission.AllowsWrite() && s.now() < grant.ExpiresAt
	reason := ""
	if !allowed {
		reason = "read_only"
		if grant != nil && s.now() >= grant.ExpiresAt {
			reason = "expired"
		}
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
		if allowed {
			payload.Outcome = "allow"
			payload.Reason = ""
		}
	}
	if err := s.audit(ctx, auditKindLinkInput, payload); err != nil {
		return err
	}
	if !allowed {
		if reason == "expired" {
			return ipc.NewError(ipc.CodeForbidden, "分享链接已过期")
		}
		return ipc.NewError(ipc.CodeForbidden, "分享链接为只读")
	}
	return nil
}
