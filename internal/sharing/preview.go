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

const (
	auditKindPreviewCreate = "share_preview_create"
	auditKindPreviewRevoke = "share_preview_revoke"
	auditKindPreviewAccess = "share_preview_access"
)

// 预览链接的拒绝原因哨兵: 订阅通道据此把终止原因映射为可下发的错误码,
// 不必匹配消息文本。
var (
	ErrPreviewInvalid = ipc.NewError(ipc.CodeForbidden, "分享链接无效")
	ErrPreviewRevoked = ipc.NewError(ipc.CodeForbidden, "分享链接已吊销")
	ErrPreviewExpired = ipc.NewError(ipc.CodeForbidden, "分享链接已过期")
)

// PreviewLink 是一条只读远程预览链接: 绑定服务端会话 Manager 里的一个终端
// 标签页 (session_id 即 tab ID), 获得链接的人匿名打开只读视图, 看到当前屏幕与后续
// 实时输出, 不能输入。恒只读, 无权限档; 过期/吊销语义与 share_link 一致。
type PreviewLink struct {
	ID             string
	OwnerID        string
	SessionID      string
	CreatedAt      int64
	ExpiresAt      int64
	RevokedAt      int64
	LastAccessedAt int64
}

// PreviewGrant 是预览授权的快照: 校验通过后交给只读订阅通道, 绑定预览 ID、
// 标签页与过期时间。不含设备与权限 (预览不经过设备代理, 恒只读)。
type PreviewGrant struct {
	ShareID   string `json:"share_id"`
	SessionID string `json:"session_id"`
	OwnerID   string `json:"owner_id"`
	ExpiresAt int64  `json:"expires_at"`
}

// CreatePreviewLink 为服务端托管的一个终端标签页创建只读预览链接。原始 token
// 只在本次返回, 库中只存哈希。标签页归属与存在性由调用方 (HTTP 路由) 校验。
func (s *Service) CreatePreviewLink(ctx context.Context, identity *account.Identity, sessionID string, ttl time.Duration) (*PreviewLink, string, error) {
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
	now := s.now()
	link := &PreviewLink{
		ID:        ids.New(),
		OwnerID:   identity.UserID,
		SessionID: sessionID,
		CreatedAt: now,
		ExpiresAt: now + ttl.Milliseconds(),
	}
	row := store.SharePreviewLink{
		ID: link.ID, OwnerID: link.OwnerID, SessionID: link.SessionID,
		TokenHash: hashSecret(token), CreatedAt: link.CreatedAt, ExpiresAt: link.ExpiresAt,
	}
	if err := query.Use(s.db).SharePreviewLink.WithContext(ctx).Create(&row); err != nil {
		return nil, "", dbError(err)
	}
	if err := s.audit(ctx, auditKindPreviewCreate, auditPayload{
		Action: "create", ShareID: link.ID, SessionID: sessionID,
		OwnerID: link.OwnerID, Requester: identity.UserID, Outcome: "allow", Permission: string(PermissionRead),
	}); err != nil {
		return nil, "", err
	}
	return link, token, nil
}

// ResolvePreview 用公开 token 换取只读授权快照; 无效、过期或已吊销一律拒绝
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
	link := PreviewLink{ID: row.ID, OwnerID: row.OwnerID, SessionID: row.SessionID, ExpiresAt: row.ExpiresAt}
	deny := func(reason string, err error) (*PreviewGrant, error) {
		if auditErr := s.audit(ctx, auditKindPreviewAccess, auditPayload{
			Action: "access", ShareID: link.ID, SessionID: link.SessionID,
			OwnerID: link.OwnerID, Requester: "", Outcome: "deny", Reason: reason, Permission: string(PermissionRead),
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
		OwnerID: link.OwnerID, Requester: "", Outcome: "allow", Permission: string(PermissionRead),
	}); err != nil {
		return nil, err
	}
	return &PreviewGrant{
		ShareID:   link.ID,
		SessionID: link.SessionID,
		OwnerID:   link.OwnerID,
		ExpiresAt: link.ExpiresAt,
	}, nil
}

// RevalidatePreview 按当前行重查预览授权: 吊销、过期或行不存在即拒绝, 供
// 只读订阅通道的周期复查使用, 不信任签发时快照。
func (s *Service) RevalidatePreview(ctx context.Context, grant *PreviewGrant) (*PreviewGrant, error) {
	if grant == nil || grant.ShareID == "" {
		return nil, ErrPreviewInvalid
	}
	sharePreviewLink := query.Use(s.db).SharePreviewLink
	row, err := sharePreviewLink.WithContext(ctx).Where(sharePreviewLink.ID.Eq(grant.ShareID)).First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPreviewInvalid
	}
	if err != nil {
		return nil, dbError(err)
	}
	switch {
	case row.SessionID != grant.SessionID:
		return nil, ErrPreviewInvalid
	case row.RevokedAt != nil:
		return nil, ErrPreviewRevoked
	case s.now() >= row.ExpiresAt:
		return nil, ErrPreviewExpired
	}
	refreshed := *grant
	refreshed.ExpiresAt = row.ExpiresAt
	return &refreshed, nil
}

// RevokePreviewLink 吊销只读预览链接; owner 可执行, 重复吊销
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

// ListPreviewLinks 返回 owner 创建的只读预览链接; 仅 owner 可见。
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
			CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
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
