package sharing

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestPreviewCreateDefaultsAndTokenHashed(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	sessionID := ids.New()

	link, token, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if link.OwnerID != owner.ID || link.SessionID != sessionID {
		t.Fatalf("link not bound to owner/session: %+v", link)
	}
	if link.ExpiresAt != fixture.now+defaultLinkTTL.Milliseconds() {
		t.Fatalf("expiresAt = %d, want default TTL", link.ExpiresAt)
	}
	if token == "" {
		t.Fatal("token must be returned once")
	}
	var tokenHash string
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT token_hash FROM share_preview_link WHERE id = ?", link.ID).Scan(&tokenHash); err != nil {
		t.Fatal(err)
	}
	if tokenHash == token || tokenHash == "" {
		t.Fatal("raw token must not be stored")
	}

	if _, _, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), "", 0); err == nil {
		t.Fatal("empty session id accepted")
	}
	if _, _, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), ids.New(), 30*time.Second); err == nil {
		t.Fatal("ttl below minimum accepted")
	}
}

func TestPreviewResolveAccessDenyAndAudit(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")

	if _, err := fixture.service.ResolvePreview(context.Background(), "no-such-token"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("unknown token err = %v, want ErrTokenNotFound", err)
	}

	link, token, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), ids.New(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.ResolvePreview(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ShareID != link.ID || grant.SessionID != link.SessionID || grant.ExpiresAt != link.ExpiresAt {
		t.Fatalf("grant = %+v, link = %+v", grant, link)
	}
	var lastAccessed sql.NullInt64
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT last_accessed_at FROM share_preview_link WHERE id = ?", link.ID).Scan(&lastAccessed); err != nil {
		t.Fatal(err)
	}
	if !lastAccessed.Valid {
		t.Fatal("last_accessed_at not updated on allow")
	}

	// 过期: 直接把 expires_at 拨到过去, resolve 拒绝且审计 reason=expired。
	if _, err := fixture.db.ExecContext(context.Background(), "UPDATE share_preview_link SET expires_at = ? WHERE id = ?", fixture.now-1, link.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ResolvePreview(context.Background(), token); err == nil {
		t.Fatal("expired token resolved")
	}

	// 吊销: resolve 拒绝且审计 reason=revoked。
	if _, err := fixture.db.ExecContext(context.Background(), "UPDATE share_preview_link SET expires_at = ?, revoked_at = ? WHERE id = ?", fixture.now+time.Hour.Milliseconds(), fixture.now, link.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ResolvePreview(context.Background(), token); err == nil {
		t.Fatal("revoked token resolved")
	}

	rows := fixture.auditRows(t, auditKindPreviewAccess)
	var outcomes []string
	for _, row := range rows {
		outcome, _ := row["outcome"].(string)
		reason, _ := row["reason"].(string)
		outcomes = append(outcomes, outcome+":"+reason)
	}
	want := []string{"deny:not_found", "allow:", "deny:expired", "deny:revoked"}
	if strings.Join(outcomes, ",") != strings.Join(want, ",") {
		t.Fatalf("access audits = %v, want %v", outcomes, want)
	}
}

func TestPreviewRevokeAuthorization(t *testing.T) {
	fixture := newServiceFixture(t)
	admin := fixture.createSuperadmin(t, "root")
	owner := fixture.createUser(t, "alice")
	other := fixture.createUser(t, "bob")
	sessionID := ids.New()

	link, token, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RevokePreviewLink(context.Background(), fixture.identity(other), link.ID); err == nil {
		t.Fatal("non-owner revoke accepted")
	}
	if _, err := fixture.service.ResolvePreview(context.Background(), token); err != nil {
		t.Fatalf("denied revoke must not revoke: %v", err)
	}
	if err := fixture.service.RevokePreviewLink(context.Background(), fixture.identity(admin), link.ID); err != nil {
		t.Fatalf("superadmin revoke: %v", err)
	}
	// 重复吊销不报错。
	if err := fixture.service.RevokePreviewLink(context.Background(), fixture.identity(owner), link.ID); err != nil {
		t.Fatalf("repeat revoke: %v", err)
	}
	if _, err := fixture.service.ResolvePreview(context.Background(), token); err == nil {
		t.Fatal("revoked token resolved")
	}
	if err := fixture.service.RevokePreviewLink(context.Background(), fixture.identity(owner), ids.New()); err == nil {
		t.Fatal("revoking missing link accepted")
	}

	rows := fixture.auditRows(t, auditKindPreviewRevoke)
	if len(rows) < 3 || rows[0]["outcome"] != "deny" || rows[0]["reason"] != "not_owner" {
		t.Fatalf("revoke audits = %v", rows)
	}
}

func TestPreviewListScoping(t *testing.T) {
	fixture := newServiceFixture(t)
	admin := fixture.createSuperadmin(t, "root")
	owner := fixture.createUser(t, "alice")
	other := fixture.createUser(t, "bob")

	if _, _, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), ids.New(), 0); err != nil {
		t.Fatal(err)
	}
	links, err := fixture.service.ListPreviewLinks(context.Background(), fixture.identity(other))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("other list = %v, want empty", links)
	}
	links, err = fixture.service.ListPreviewLinks(context.Background(), fixture.identity(owner))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].OwnerID != owner.ID {
		t.Fatalf("owner list = %v", links)
	}
	links, err = fixture.service.ListPreviewLinks(context.Background(), fixture.identity(admin))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("superadmin list = %v", links)
	}
}

func TestPreviewRevalidate(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	sessionID := ids.New()

	link, _, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), sessionID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	grant := &PreviewGrant{ShareID: link.ID, SessionID: sessionID, OwnerID: owner.ID, ExpiresAt: link.ExpiresAt}
	refreshed, err := fixture.service.RevalidatePreview(context.Background(), grant)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ExpiresAt != link.ExpiresAt {
		t.Fatalf("refreshed grant = %+v", refreshed)
	}

	if _, err := fixture.service.RevalidatePreview(context.Background(), &PreviewGrant{ShareID: link.ID, SessionID: ids.New()}); err == nil {
		t.Fatal("session binding mismatch accepted")
	}
	if _, err := fixture.service.RevalidatePreview(context.Background(), &PreviewGrant{ShareID: ids.New(), SessionID: sessionID}); err == nil {
		t.Fatal("missing row accepted")
	}
	if err := fixture.service.RevokePreviewLink(context.Background(), fixture.identity(owner), link.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidatePreview(context.Background(), grant); err == nil {
		t.Fatal("revoked grant revalidated")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	if _, err := fixture.service.RevalidatePreview(context.Background(), nil); err == nil {
		t.Fatal("nil grant revalidated")
	}
}
