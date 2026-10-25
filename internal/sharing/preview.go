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

// Denial-reason sentinels for preview links: the subscribe channel maps termination reasons to deliverable error codes from these,
// without matching message text.
var (
	ErrPreviewInvalid = ipc.NewError(ipc.CodeForbidden, "分享链接无效")
	ErrPreviewRevoked = ipc.NewError(ipc.CodeForbidden, "分享链接已吊销")
	ErrPreviewExpired = ipc.NewError(ipc.CodeForbidden, "分享链接已过期")
)

// PreviewLink is a remote preview link: it binds one terminal
// tab in the server-side session Manager (session_id is the tab ID); whoever holds the link anonymously opens a view and sees the current screen and subsequent
// live output; read-only by default, input is allowed only when write is explicitly enabled at creation.
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

// PreviewGrant is the snapshot of a preview authorization: after verification it is handed to the subscribe channel, binding the preview ID,
// tab, permission, and expiry. It carries no device (previews do not go through the device agent).
type PreviewGrant struct {
	ShareID    string     `json:"share_id"`
	SessionID  string     `json:"session_id"`
	OwnerID    string     `json:"owner_id"`
	Permission Permission `json:"permission"`
	ExpiresAt  int64      `json:"expires_at"`
}

// previewPermission normalizes the stored permission level to the protocol's two levels: only a strict read_write
// match allows writes; empty or unknown levels are always treated as read-only.
func previewPermission(raw string) Permission {
	if Permission(raw) == PermissionReadWrite {
		return PermissionReadWrite
	}
	return PermissionRead
}

// CreatePreviewLink creates a preview link for one server-hosted terminal tab. Read-only by default;
// read_write is granted only when write is explicitly true. The raw token is returned only in this response; only its hash is stored.
// Tab ownership and existence are verified by the caller (the HTTP route). The link row and the create audit commit in the same
// transaction: an audit write failure rolls back everything, leaving no anonymous link without an audit.
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

// writeAuditTx writes an audit row inside a transaction, in the same row format as writeAudit; on failure it returns an error
// and the surrounding transaction rolls back all writes.
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

// ResolvePreview exchanges a public token for an authorization snapshot; invalid, expired, or revoked cases are all denied
// with an audit record, without distinguishing invalid from nonexistent.
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

// revalidatePreview is the internal implementation of RevalidatePreview; it additionally returns a machine-readable denial
// reason ("not_found"/"revoked"/"expired") for the input path to write audits.
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

// RevalidatePreview re-reads the preview authorization from the current row: revoked, expired, or missing rows are denied; it returns
// the refreshed authorization (permission and expiry follow the current row, and permission shrinkage takes effect immediately), for the subscribe channel's
// periodic recheck, never trusting the snapshot from issuance.
func (s *Service) RevalidatePreview(ctx context.Context, grant *PreviewGrant) (*PreviewGrant, error) {
	refreshed, _, err := s.revalidatePreview(ctx, grant)
	return refreshed, err
}

// RevalidatePreviewInput is the server-side enforcement point of the preview input path: every input is re-read from the current row
// (revocation/expiry/binding) and required to be read_write, never trusting the authorization snapshot from issuance; permission shrinkage
// takes effect immediately; every decision (allow or deny) is written to the audit log (aggregated by session/time window, see
// auditInputAggregated).
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

// RevokePreviewLink revokes a preview link; the owner may do this, and repeated revocation
// does not error. Every decision is recorded in an audit entry carrying the share_id.
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

// ListPreviewLinks returns the preview links created by the owner; only the owner can see them.
// Token hashes are not included.
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
