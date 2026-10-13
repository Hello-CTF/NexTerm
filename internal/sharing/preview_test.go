package sharing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

func TestPreviewCreateDefaultsAndTokenHashed(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	sessionID := ids.New()

	link, token, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), sessionID, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if link.OwnerID != owner.ID || link.SessionID != sessionID {
		t.Fatalf("link not bound to owner/session: %+v", link)
	}
	if link.Permission != PermissionRead {
		t.Fatalf("default permission = %q, want %q", link.Permission, PermissionRead)
	}
	if link.ExpiresAt != fixture.now+defaultLinkTTL.Milliseconds() {
		t.Fatalf("expiresAt = %d, want default TTL", link.ExpiresAt)
	}
	if token == "" {
		t.Fatal("token must be returned once")
	}
	sharePreviewLink := query.Use(fixture.db).SharePreviewLink
	row, err := sharePreviewLink.WithContext(context.Background()).Where(sharePreviewLink.ID.Eq(link.ID)).First()
	if err != nil {
		t.Fatal(err)
	}
	if row.TokenHash == token || row.TokenHash == "" {
		t.Fatal("raw token must not be stored")
	}
	if row.Permission != string(PermissionRead) {
		t.Fatalf("stored permission = %q, want %q", row.Permission, PermissionRead)
	}

	writeLink, _, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), ids.New(), true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if writeLink.Permission != PermissionReadWrite {
		t.Fatalf("write permission = %q, want %q", writeLink.Permission, PermissionReadWrite)
	}
	row, err = sharePreviewLink.WithContext(context.Background()).Where(sharePreviewLink.ID.Eq(writeLink.ID)).First()
	if err != nil {
		t.Fatal(err)
	}
	if row.Permission != string(PermissionReadWrite) {
		t.Fatalf("stored permission = %q, want %q", row.Permission, PermissionReadWrite)
	}

	rows := fixture.auditRows(t, auditKindPreviewCreate)
	if len(rows) != 2 || rows[0]["permission"] != string(PermissionRead) || rows[1]["permission"] != string(PermissionReadWrite) {
		t.Fatalf("create audits = %+v", rows)
	}

	if _, _, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), "", false, 0); err == nil {
		t.Fatal("empty session id accepted")
	}
	if _, _, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), ids.New(), false, 30*time.Second); err == nil {
		t.Fatal("ttl below minimum accepted")
	}
}

func TestPreviewCreateAuditFailureRollsBackLink(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")

	// Make the create audit write fail: with the audit table dropped the insert must fail, and the link row must roll back
	// with the same transaction, leaving no anonymous link without an audit.
	if err := fixture.db.Exec("DROP TABLE audit_log").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), ids.New(), true, 0); err == nil {
		t.Fatal("create must fail when the audit write fails")
	}
	sharePreviewLink := query.Use(fixture.db).SharePreviewLink
	count, err := sharePreviewLink.WithContext(context.Background()).Count()
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("link row survived audit failure, want transactional rollback (%d rows)", count)
	}
}

func TestPreviewResolveAccessDenyAndAudit(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")

	if _, err := fixture.service.ResolvePreview(context.Background(), "no-such-token"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("unknown token err = %v, want ErrTokenNotFound", err)
	}

	link, token, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), ids.New(), false, time.Hour)
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
	if grant.Permission != PermissionRead {
		t.Fatalf("grant permission = %q, want %q", grant.Permission, PermissionRead)
	}

	_, writeToken, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), ids.New(), true, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	writeGrant, err := fixture.service.ResolvePreview(context.Background(), writeToken)
	if err != nil {
		t.Fatal(err)
	}
	if writeGrant.Permission != PermissionReadWrite {
		t.Fatalf("write grant permission = %q, want %q", writeGrant.Permission, PermissionReadWrite)
	}

	sharePreviewLink := query.Use(fixture.db).SharePreviewLink
	row, err := sharePreviewLink.WithContext(context.Background()).Where(sharePreviewLink.ID.Eq(link.ID)).First()
	if err != nil {
		t.Fatal(err)
	}
	if row.LastAccessedAt == nil {
		t.Fatal("last_accessed_at not updated on allow")
	}

	// Expired: wind expires_at directly into the past; resolve denies with audit reason=expired.
	if _, err := sharePreviewLink.WithContext(context.Background()).Where(sharePreviewLink.ID.Eq(link.ID)).UpdateColumns(map[string]any{"expires_at": fixture.now - 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ResolvePreview(context.Background(), token); err == nil {
		t.Fatal("expired token resolved")
	}

	// Revoked: resolve denies with audit reason=revoked.
	if _, err := sharePreviewLink.WithContext(context.Background()).Where(sharePreviewLink.ID.Eq(link.ID)).UpdateColumns(map[string]any{"expires_at": fixture.now + time.Hour.Milliseconds(), "revoked_at": fixture.now}); err != nil {
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
	want := []string{"deny:not_found", "allow:", "allow:", "deny:expired", "deny:revoked"}
	if strings.Join(outcomes, ",") != strings.Join(want, ",") {
		t.Fatalf("access audits = %v, want %v", outcomes, want)
	}
	if rows[1]["permission"] != string(PermissionRead) || rows[2]["permission"] != string(PermissionReadWrite) {
		t.Fatalf("access audit permissions = %q, %q", rows[1]["permission"], rows[2]["permission"])
	}
}

func TestPreviewRevokeAuthorization(t *testing.T) {
	fixture := newServiceFixture(t)
	admin := fixture.createSuperadmin(t, "root")
	owner := fixture.createUser(t, "alice")
	other := fixture.createUser(t, "bob")
	sessionID := ids.New()

	link, token, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), sessionID, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RevokePreviewLink(context.Background(), fixture.identity(other), link.ID); err == nil {
		t.Fatal("non-owner revoke accepted")
	}
	if _, err := fixture.service.ResolvePreview(context.Background(), token); err != nil {
		t.Fatalf("denied revoke must not revoke: %v", err)
	}
	if err := fixture.service.RevokePreviewLink(context.Background(), fixture.identity(admin), link.ID); err == nil {
		t.Fatal("superadmin revoke on another user's link must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	if err := fixture.service.RevokePreviewLink(context.Background(), fixture.identity(owner), link.ID); err != nil {
		t.Fatalf("owner revoke: %v", err)
	}
	// Repeated revocation does not error.
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

	if _, _, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), ids.New(), true, 0); err != nil {
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
	if links[0].Permission != PermissionReadWrite {
		t.Fatalf("listed permission = %q, want %q", links[0].Permission, PermissionReadWrite)
	}
	links, err = fixture.service.ListPreviewLinks(context.Background(), fixture.identity(admin))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("superadmin list = %v", links)
	}
}

func TestPreviewRevalidate(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	sessionID := ids.New()

	link, _, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), sessionID, true, time.Hour)
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
	if refreshed.Permission != PermissionReadWrite {
		t.Fatalf("refreshed permission = %q, want %q", refreshed.Permission, PermissionReadWrite)
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

func TestPreviewInputRevalidation(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")

	readLink, readToken, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), ids.New(), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	readGrant, err := fixture.service.ResolvePreview(context.Background(), readToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidatePreviewInput(context.Background(), readGrant); err == nil {
		t.Fatal("read-only grant must reject input")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	writeLink, writeToken, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), ids.New(), true, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeGrant, err := fixture.service.ResolvePreview(context.Background(), writeToken)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := fixture.service.RevalidatePreviewInput(context.Background(), writeGrant)
	if err != nil {
		t.Fatalf("read-write grant must allow input: %v", err)
	}
	if refreshed.Permission != PermissionReadWrite || refreshed.ExpiresAt != writeGrant.ExpiresAt {
		t.Fatalf("refreshed grant = %+v", refreshed)
	}

	fixture.now = writeGrant.ExpiresAt
	if _, err := fixture.service.RevalidatePreviewInput(context.Background(), writeGrant); err == nil {
		t.Fatal("expired grant must reject input")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	rows := fixture.auditRows(t, auditKindPreviewInput)
	if len(rows) != 3 {
		t.Fatalf("input audits = %+v", rows)
	}
	if rows[0]["outcome"] != "deny" || rows[0]["reason"] != "read_only" || rows[0]["share_id"] != readLink.ID {
		t.Fatalf("read-only input audit = %+v", rows[0])
	}
	if rows[1]["outcome"] != "allow" || rows[1]["share_id"] != writeLink.ID || rows[1]["permission"] != string(PermissionReadWrite) {
		t.Fatalf("read-write input audit = %+v", rows[1])
	}
	if rows[2]["outcome"] != "deny" || rows[2]["reason"] != "expired" {
		t.Fatalf("expired input audit = %+v", rows[2])
	}
}

func TestPreviewPermissionShrinkStopsInput(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	sessionID := ids.New()

	_, token, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), sessionID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.ResolvePreview(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidatePreviewInput(context.Background(), grant); err != nil {
		t.Fatalf("input must flow before shrink: %v", err)
	}

	// Permission shrinkage (read_write -> read) takes effect immediately from the current row: input stops, output authorization remains.
	sharePreviewLink := query.Use(fixture.db).SharePreviewLink
	if _, err := sharePreviewLink.WithContext(context.Background()).Where(sharePreviewLink.ID.Eq(grant.ShareID)).UpdateColumns(map[string]any{"permission": string(PermissionRead)}); err != nil {
		t.Fatal(err)
	}
	refreshed, err := fixture.service.RevalidatePreview(context.Background(), grant)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Permission != PermissionRead {
		t.Fatalf("refreshed permission = %q, want read", refreshed.Permission)
	}
	if _, err := fixture.service.RevalidatePreviewInput(context.Background(), grant); err == nil {
		t.Fatal("shrunk grant must reject input")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	rows := fixture.auditRows(t, auditKindPreviewInput)
	if last := rows[len(rows)-1]; last["outcome"] != "deny" || last["reason"] != "read_only" || last["share_id"] != grant.ShareID {
		t.Fatalf("input audits = %+v", rows)
	}
}

func TestPreviewInputStopsAfterRevocation(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")

	link, token, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), ids.New(), true, 0)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.ResolvePreview(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidatePreviewInput(context.Background(), grant); err != nil {
		t.Fatalf("input must flow before revocation: %v", err)
	}

	if err := fixture.service.RevokePreviewLink(context.Background(), fixture.identity(owner), link.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidatePreviewInput(context.Background(), grant); err == nil {
		t.Fatal("revoked link must reject input even with a pre-revocation grant")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	rows := fixture.auditRows(t, auditKindPreviewInput)
	if last := rows[len(rows)-1]; last["outcome"] != "deny" || last["reason"] != "revoked" || last["share_id"] != link.ID {
		t.Fatalf("input audits = %+v", rows)
	}
}

func TestPreviewInputAuditAggregatedBySessionWindow(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	sessionID := ids.New()

	link, token, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), sessionID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.ResolvePreview(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}

	for range 5 {
		if _, err := fixture.service.RevalidatePreviewInput(context.Background(), grant); err != nil {
			t.Fatal(err)
		}
	}
	rows := fixture.auditRows(t, auditKindPreviewInput)
	if len(rows) != 1 || rows[0]["outcome"] != "allow" {
		t.Fatalf("input audits within one window = %+v, want a single allow row", rows)
	}

	fixture.now += linkInputAuditWindow.Milliseconds() + 1
	if _, err := fixture.service.RevalidatePreviewInput(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	rows = fixture.auditRows(t, auditKindPreviewInput)
	if len(rows) != 2 {
		t.Fatalf("input audits after window = %+v", rows)
	}
	if suppressed, _ := rows[1]["suppressed"].(float64); suppressed != 5 {
		t.Fatalf("aggregated row suppressed = %v, want 5 (%+v)", rows[1]["suppressed"], rows[1])
	}

	if err := fixture.service.RevokePreviewLink(context.Background(), fixture.identity(owner), link.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidatePreviewInput(context.Background(), grant); err == nil {
		t.Fatal("revoked link must reject input")
	}
	rows = fixture.auditRows(t, auditKindPreviewInput)
	if len(rows) != 3 || rows[2]["outcome"] != "deny" || rows[2]["reason"] != "revoked" {
		t.Fatalf("deny audit must not be delayed by aggregation: %+v", rows)
	}
}

// An out-of-protocol permission level in the database (dirty data/manual edit) is always treated as read-only: parsing and revalidation normalize to
// read, the input enforcement point denies, and an unknown level is never delivered or allowed to write.
func TestPreviewUnknownDBPermissionFailsSafe(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	sessionID := ids.New()

	link, token, err := fixture.service.CreatePreviewLink(context.Background(), fixture.identity(owner), sessionID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	sharePreviewLink := query.Use(fixture.db).SharePreviewLink
	if _, err := sharePreviewLink.WithContext(context.Background()).Where(sharePreviewLink.ID.Eq(link.ID)).UpdateColumns(map[string]any{"permission": "admin"}); err != nil {
		t.Fatal(err)
	}

	grant, err := fixture.service.ResolvePreview(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if grant.Permission != PermissionRead {
		t.Fatalf("unknown permission must normalize to read, got %q", grant.Permission)
	}
	if _, err := fixture.service.RevalidatePreviewInput(context.Background(), grant); err == nil {
		t.Fatal("unknown DB permission must not allow input")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	links, err := fixture.service.ListPreviewLinks(context.Background(), fixture.identity(owner))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Permission != PermissionRead {
		t.Fatalf("listed links = %+v", links)
	}

	rows := fixture.auditRows(t, auditKindPreviewInput)
	if last := rows[len(rows)-1]; last["outcome"] != "deny" || last["reason"] != "read_only" {
		t.Fatalf("input audits = %+v", rows)
	}
}
