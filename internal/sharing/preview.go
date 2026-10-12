package sharing

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

const (
	auditKindPreviewCreate = "share_preview_create"
	auditKindPreviewRevoke = "share_preview_revoke"
	auditKindPreviewAccess = "share_preview_access"
	auditKindPreviewInput  = "share_preview_input"
)

// 预览链接的拒绝原因哨兵: 订阅通道据此把终止原因映射为可下发的错误码,
// 不必匹配消息文本。
var (
	ErrPreviewInvalid = ipc.NewError(ipc.CodeForbidden, "分享链接无效")
	ErrPreviewRevoked = ipc.NewError(ipc.CodeForbidden, "分享链接已吊销")
	ErrPreviewExpired = ipc.NewError(ipc.CodeForbidden, "分享链接已过期")
)

// PreviewLink 是一条远程预览链接: 绑定服务端会话 Manager 里的一个终端
// 标签页 (session_id 即 tab ID), 获得链接的人匿名打开视图, 看到当前屏幕与后续
// 实时输出; 默认只读, 创建时显式开写才允许输入。过期/吊销语义与 share_link 一致。
type PreviewLink struct {
	ID             string
	OwnerID        string
	SessionID      string
	Permission     Permission
	CreatedAt      int64
	ExpiresAt      int64
	RevokedAt      int64
	LastAccessedAt int64
}

// PreviewGrant 是预览授权的快照: 校验通过后交给订阅通道, 绑定预览 ID、
// 标签页、权限与过期时间。不含设备 (预览不经过设备代理)。
type PreviewGrant struct {
	ShareID    string     `json:"share_id"`
	SessionID  string     `json:"session_id"`
	OwnerID    string     `json:"owner_id"`
	Permission Permission `json:"permission"`
	ExpiresAt  int64      `json:"expires_at"`
}

// previewPermission 把库存权限档归一到协议两档: 只有严格等于 read_write 才
// 放行写, 空值或未知档位一律按只读处理。
func previewPermission(raw string) Permission {
	if Permission(raw) == PermissionReadWrite {
		return PermissionReadWrite
	}
	return PermissionRead
}

// CreatePreviewLink 为服务端托管的一个终端标签页创建预览链接。默认只读;
// write 显式为 true 时才授予读写。原始 token 只在本次返回, 库中只存哈希。
// 标签页归属与存在性由调用方 (HTTP 路由) 校验。链接行与 create 审计在同一
// 事务提交: 审计写入失败即整体回滚, 不留下无审计的匿名链接。
func (s *Service) CreatePreviewLink(ctx context.Context, identity *account.Identity, sessionID string, write bool, ttl time.Duration) (*PreviewLink, string, error) {
	if sessionID == "" {
		return nil, "", ipc.BadParam(errors.New("会话 ID 不能为空"))
	}
	ttl, err := normalizeTTL(ttl, defaultLinkTTL)
	if err != nil {
		return nil, "", err
	}
	token, err := randomSecret(linkTokenBytes)
	if err != nil {
		return nil, "", err
	}
	permission := permissionFor(write)
	now := s.now()
	link := &PreviewLink{
		ID:         ids.New(),
		OwnerID:    identity.UserID,
		SessionID:  sessionID,
		Permission: permission,
		CreatedAt:  now,
		ExpiresAt:  now + ttl.Milliseconds(),
	}
	row := store.SharePreviewLink{
		ID: link.ID, OwnerID: link.OwnerID, SessionID: link.SessionID,
		TokenHash: hashSecret(token), Permission: string(link.Permission), CreatedAt: link.CreatedAt, ExpiresAt: link.ExpiresAt,
	}
	payload := auditPayload{
		Action: "create", ShareID: link.ID, SessionID: sessionID,
		OwnerID: link.OwnerID, Requester: identity.UserID, Outcome: "allow", Permission: string(permission),
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := query.Use(tx).SharePreviewLink.WithContext(ctx).Create(&row); err != nil {
			return dbError(err)
		}
		return s.writeAuditTx(ctx, tx, auditKindPreviewCreate, payload)
	}); err != nil {
		return nil, "", err
	}
	return link, token, nil
}

// writeAuditTx 在事务内写审计行, 行格式与 writeAudit 一致; 失败时返回错误,
// 由所在事务回滚全部写入。
func (s *Service) writeAuditTx(ctx context.Context, tx *gorm.DB, kind string, payload auditPayload) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
	}
	row := store.AuditRow{TS: s.now(), Source: auditSourceSharing, Kind: kind, PayloadJSON: string(encoded), OwnerID: ipc.OwnerID(ctx)}
	if err := query.Use(tx).AuditRow.WithContext(ctx).Create(&row); err != nil {
		return dbError(err)
	}
	return nil
}

// ResolvePreview 用公开 token 换取授权快照; 无效、过期或已吊销一律拒绝
// 并写审计, 不区分无效与不存在。
func (s *Service) ResolvePreview(ctx context.Context, token string) (*PreviewGrant, error) {
	sharePreviewLink := query.Use(s.db).SharePreviewLink
	row, err := sharePreviewLink.WithContext(ctx).Where(sharePreviewLink.TokenHash.Eq(hashSecret(token))).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if auditErr := s.audit(ctx, auditKindPreviewAccess, auditPayload{Action: "access", Requester: "", Outcome: "deny", Reason: "not_found"}); auditErr != nil {
			return nil, auditErr
		}
		return nil, ErrTokenNotFound
	}
	if err != nil {
		return nil, dbError(err)
	}
	link := PreviewLink{
		ID: row.ID, OwnerID: row.OwnerID, SessionID: row.SessionID,
		Permission: previewPermission(row.Permission), ExpiresAt: row.ExpiresAt,
	}
	deny := func(reason string, err error) (*PreviewGrant, error) {
		if auditErr := s.audit(ctx, auditKindPreviewAccess, auditPayload{
			Action: "access", ShareID: link.ID, SessionID: link.SessionID,
			OwnerID: link.OwnerID, Requester: "", Outcome: "deny", Reason: reason, Permission: string(link.Permission),
		}); auditErr != nil {
			return nil, auditErr
		}
		return nil, err
	}
	switch {
	case row.RevokedAt != nil:
		return deny("revoked", ErrPreviewRevoked)
	case s.now() >= link.ExpiresAt:
		return deny("expired", ErrPreviewExpired)
	}
	if _, err := sharePreviewLink.WithContext(ctx).Where(sharePreviewLink.ID.Eq(link.ID)).UpdateColumns(map[string]any{"last_accessed_at": s.now()}); err != nil {
		return nil, dbError(err)
	}
	if err := s.audit(ctx, auditKindPreviewAccess, auditPayload{
		Action: "access", ShareID: link.ID, SessionID: link.SessionID,
		OwnerID: link.OwnerID, Requester: "", Outcome: "allow", Permission: string(link.Permission),
	}); err != nil {
		return nil, err
	}
	return &PreviewGrant{
		ShareID:    link.ID,
		SessionID:  link.SessionID,
		OwnerID:    link.OwnerID,
		Permission: link.Permission,
		ExpiresAt:  link.ExpiresAt,
	}, nil
}

// revalidatePreview 是 RevalidatePreview 的内部实现, 额外返回机器可读的拒绝
// 原因 ("not_found"/"revoked"/"expired"), 供输入路径写审计。
func (s *Service) revalidatePreview(ctx context.Context, grant *PreviewGrant) (*PreviewGrant, string, error) {
	if grant == nil || grant.ShareID == "" {
		return nil, "not_found", ErrPreviewInvalid
	}
	sharePreviewLink := query.Use(s.db).SharePreviewLink
	row, err := sharePreviewLink.WithContext(ctx).Where(sharePreviewLink.ID.Eq(grant.ShareID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "not_found", ErrPreviewInvalid
	}
	if err != nil {
		return nil, "", dbError(err)
	}
	switch {
	case row.SessionID != grant.SessionID:
		return nil, "not_found", ErrPreviewInvalid
	case row.RevokedAt != nil:
		return nil, "revoked", ErrPreviewRevoked
	case s.now() >= row.ExpiresAt:
		return nil, "expired", ErrPreviewExpired
	}
	refreshed := *grant
	refreshed.Permission = previewPermission(row.Permission)
	refreshed.ExpiresAt = row.ExpiresAt
	return &refreshed, "", nil
}

// RevalidatePreview 按当前行重查预览授权: 吊销、过期或行不存在即拒绝, 返回
// 刷新后的授权 (权限与过期时间以当前行为准, 权限收缩即时生效), 供订阅通道的
// 周期复查使用, 不信任签发时快照。
func (s *Service) RevalidatePreview(ctx context.Context, grant *PreviewGrant) (*PreviewGrant, error) {
	refreshed, _, err := s.revalidatePreview(ctx, grant)
	return refreshed, err
}

// RevalidatePreviewInput 是预览输入路径的服务端强制点: 每次输入都按当前行
// 重查 (吊销/过期/绑定) 并要求 read_write, 不信任签发时的授权快照, 权限收缩
// 即时生效; 每次判定 (放行或拒绝) 都写审计 (按会话/时间窗口聚合, 见
// auditInputAggregated)。
func (s *Service) RevalidatePreviewInput(ctx context.Context, grant *PreviewGrant) (*PreviewGrant, error) {
	refreshed, reason, err := s.revalidatePreview(ctx, grant)
	if err == nil && !refreshed.Permission.AllowsWrite() {
		reason = "read_only"
		err = ipc.NewError(ipc.CodeForbidden, "分享链接为只读")
	}
	payload := auditPayload{Action: "input", Requester: "", Outcome: "deny", Reason: reason}
	if grant != nil {
		payload.ShareID = grant.ShareID
		payload.SessionID = grant.SessionID
		payload.OwnerID = grant.OwnerID
		payload.Permission = string(grant.Permission)
	}
	if err == nil {
		payload.Outcome = "allow"
		payload.Reason = ""
		payload.Permission = string(refreshed.Permission)
	}
	if auditErr := s.auditInputAggregated(ctx, auditKindPreviewInput, payload); auditErr != nil {
		return nil, auditErr
	}
	if err != nil {
		return nil, err
	}
	return refreshed, nil
}

// RevokePreviewLink 吊销预览链接; owner 可执行, 重复吊销
// 不报错。每次判定都写含 share_id 的审计。
func (s *Service) RevokePreviewLink(ctx context.Context, identity *account.Identity, linkID string) error {
	sharePreviewLink := query.Use(s.db).SharePreviewLink
	row, err := sharePreviewLink.WithContext(ctx).Where(sharePreviewLink.ID.Eq(linkID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ipc.NewError(ipc.CodeNotFound, "分享链接不存在")
	}
	if err != nil {
		return dbError(err)
	}
	outcome, reason := "allow", ""
	var decision error
	if row.OwnerID != identity.UserID {
		outcome, reason = "deny", "not_owner"
		decision = ipc.NewError(ipc.CodeForbidden, "分享链接不属于该用户")
	}
	if auditErr := s.audit(ctx, auditKindPreviewRevoke, auditPayload{
		Action: "revoke", ShareID: linkID, SessionID: row.SessionID,
		OwnerID: row.OwnerID, Requester: identity.UserID, Outcome: outcome, Reason: reason,
	}); auditErr != nil {
		return auditErr
	}
	if decision != nil {
		return decision
	}
	if _, err := sharePreviewLink.WithContext(ctx).Where(sharePreviewLink.ID.Eq(linkID), sharePreviewLink.RevokedAt.IsNull()).UpdateColumns(map[string]any{"revoked_at": s.now()}); err != nil {
		return dbError(err)
	}
	return nil
}

// ListPreviewLinks 返回 owner 创建的预览链接; 仅 owner 可见。
// 不含 token 哈希。
func (s *Service) ListPreviewLinks(ctx context.Context, identity *account.Identity) ([]*PreviewLink, error) {
	sharePreviewLink := query.Use(s.db).SharePreviewLink
	rows, err := sharePreviewLink.WithContext(ctx).
		Where(sharePreviewLink.OwnerID.Eq(identity.UserID)).
		Order(sharePreviewLink.CreatedAt, sharePreviewLink.ID).
		Find()
	if err != nil {
		return nil, dbError(err)
	}
	links := make([]*PreviewLink, 0, len(rows))
	for _, row := range rows {
		link := &PreviewLink{
			ID: row.ID, OwnerID: row.OwnerID, SessionID: row.SessionID,
			Permission: previewPermission(row.Permission),
			CreatedAt:  row.CreatedAt, ExpiresAt: row.ExpiresAt,
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
