package sharing

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestLinkCreateDefaultsReadOnly(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	sessionID := ids.New()

	link, token, err := fixture.service.CreateLink(context.Background(), fixture.identity(owner), deviceID, sessionID, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if link.Permission != PermissionRead {
		t.Fatalf("default permission = %q, want %q", link.Permission, PermissionRead)
	}
	if link.OwnerID != owner.ID || link.DeviceID != deviceID || link.SessionID != sessionID {
		t.Fatalf("link not bound to owner/device/session: %+v", link)
	}
	if link.ExpiresAt != fixture.now+defaultLinkTTL.Milliseconds() {
		t.Fatalf("expiresAt = %d, want default TTL", link.ExpiresAt)
	}
	if token == "" {
		t.Fatal("token must be returned once")
	}
	var tokenHash string
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT token_hash FROM share_link WHERE id = ?", link.ID).Scan(&tokenHash); err != nil {
		t.Fatal(err)
	}
	if tokenHash == token {
		t.Fatal("raw token must not be stored")
	}
}

func TestLinkCreateExplicitWrite(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")

	link, _, err := fixture.service.CreateLink(context.Background(), fixture.identity(owner), deviceID, ids.New(), true, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if link.Permission != PermissionReadWrite {
		t.Fatalf("permission = %q, want %q", link.Permission, PermissionReadWrite)
	}
}

func TestLinkCreateAuthorization(t *testing.T) {
	fixture := newServiceFixture(t)
	admin := fixture.createSuperadmin(t, "root")
	owner := fixture.createUser(t, "alice")
	other := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")

	_, _, err := fixture.service.CreateLink(context.Background(), fixture.identity(other), deviceID, ids.New(), false, 0)
	requireIPCCode(t, err, ipc.CodeForbidden)

	if _, _, err := fixture.service.CreateLink(context.Background(), fixture.identity(admin), deviceID, ids.New(), false, 0); err != nil {
		t.Fatalf("superadmin create: %v", err)
	}

	rows := fixture.auditRows(t, auditKindLinkCreate)
	if len(rows) < 2 {
		t.Fatalf("create audits = %d, want deny + allow", len(rows))
	}
	if rows[0]["outcome"] != "deny" || rows[0]["reason"] != "not_owner" {
		t.Fatalf("deny audit = %+v", rows[0])
	}
}

func TestLinkCreateRequiresDaemonHost(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	plainDevice := fixture.createDevice(t, owner, "desktop-only")

	_, _, err := fixture.service.CreateLink(context.Background(), fixture.identity(owner), plainDevice, ids.New(), false, 0)
	requireIPCCode(t, err, ipc.CodeForbidden)

	agentDevice := fixture.createAgentDevice(t, owner, "agent-host")
	fixture.setAgentOnline(t, agentDevice, false)
	_, _, err = fixture.service.CreateLink(context.Background(), fixture.identity(owner), agentDevice, ids.New(), false, 0)
	requireIPCCode(t, err, ipc.CodeDisconnected)
}

func TestLinkResolve(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	sessionID := ids.New()
	link, token, err := fixture.service.CreateLink(context.Background(), fixture.identity(owner), deviceID, sessionID, true, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	grant, err := fixture.service.ResolveLink(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if len(grant.ShareIDs) != 1 || grant.ShareIDs[0] != link.ID {
		t.Fatalf("grant share ids = %v", grant.ShareIDs)
	}
	if grant.DeviceID != deviceID || grant.SessionID != sessionID || grant.OwnerID != owner.ID {
		t.Fatalf("grant binding = %+v", grant)
	}
	if grant.Recipient != "" {
		t.Fatalf("public link grant must be anonymous, got recipient %q", grant.Recipient)
	}
	if grant.Permission != PermissionReadWrite || grant.ExpiresAt != link.ExpiresAt {
		t.Fatalf("grant permission/expiry = %+v", grant)
	}

	var lastAccessedAt int64
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT last_accessed_at FROM share_link WHERE id = ?", link.ID).Scan(&lastAccessedAt); err != nil {
		t.Fatal(err)
	}
	if lastAccessedAt != fixture.now {
		t.Fatalf("last_accessed_at = %d, want %d", lastAccessedAt, fixture.now)
	}

	rows := fixture.auditRows(t, auditKindLinkAccess)
	if len(rows) != 1 || rows[0]["outcome"] != "allow" || rows[0]["share_id"] != link.ID {
		t.Fatalf("access audits = %+v", rows)
	}
}

func TestLinkResolveInvalidToken(t *testing.T) {
	fixture := newServiceFixture(t)

	_, err := fixture.service.ResolveLink(context.Background(), "not-a-real-token")
	requireIPCCode(t, err, ipc.CodeForbidden)

	rows := fixture.auditRows(t, auditKindLinkAccess)
	if len(rows) != 1 || rows[0]["outcome"] != "deny" || rows[0]["reason"] != "not_found" {
		t.Fatalf("access audits = %+v", rows)
	}
}

func TestLinkExpiryStopsAccess(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	_, token, err := fixture.service.CreateLink(context.Background(), fixture.identity(owner), deviceID, ids.New(), false, minShareTTL)
	if err != nil {
		t.Fatal(err)
	}

	fixture.now += minShareTTL.Milliseconds() + 1
	_, err = fixture.service.ResolveLink(context.Background(), token)
	requireIPCCode(t, err, ipc.CodeForbidden)

	rows := fixture.auditRows(t, auditKindLinkAccess)
	if len(rows) != 1 || rows[0]["outcome"] != "deny" || rows[0]["reason"] != "expired" {
		t.Fatalf("access audits = %+v", rows)
	}
}

func TestLinkRevocationStopsAccess(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	other := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	link, token, err := fixture.service.CreateLink(context.Background(), fixture.identity(owner), deviceID, ids.New(), false, 0)
	if err != nil {
		t.Fatal(err)
	}

	if err := fixture.service.RevokeLink(context.Background(), fixture.identity(other), link.ID); err == nil {
		t.Fatal("non-owner revoke must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	if _, err := fixture.service.ResolveLink(context.Background(), token); err != nil {
		t.Fatalf("link must stay active after failed revoke: %v", err)
	}

	if err := fixture.service.RevokeLink(context.Background(), fixture.identity(owner), link.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RevokeLink(context.Background(), fixture.identity(owner), link.ID); err != nil {
		t.Fatalf("repeated revoke must be idempotent: %v", err)
	}
	if _, err := fixture.service.ResolveLink(context.Background(), token); err == nil {
		t.Fatal("revoked link must not resolve")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	rows := fixture.auditRows(t, auditKindLinkAccess)
	last := rows[len(rows)-1]
	if last["outcome"] != "deny" || last["reason"] != "revoked" {
		t.Fatalf("access audits = %+v", rows)
	}
}

func TestLinkInputRevalidation(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")

	readLink, readToken, err := fixture.service.CreateLink(context.Background(), fixture.identity(owner), deviceID, ids.New(), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	readGrant, err := fixture.service.ResolveLink(context.Background(), readToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidateLinkInput(context.Background(), readGrant); err == nil {
		t.Fatal("read-only grant must reject input")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	writeLink, writeToken, err := fixture.service.CreateLink(context.Background(), fixture.identity(owner), deviceID, ids.New(), true, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeGrant, err := fixture.service.ResolveLink(context.Background(), writeToken)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := fixture.service.RevalidateLinkInput(context.Background(), writeGrant)
	if err != nil {
		t.Fatalf("read-write grant must allow input: %v", err)
	}
	if refreshed.Permission != PermissionReadWrite || refreshed.ExpiresAt != writeGrant.ExpiresAt {
		t.Fatalf("refreshed grant = %+v", refreshed)
	}

	fixture.now = writeGrant.ExpiresAt
	if _, err := fixture.service.RevalidateLinkInput(context.Background(), writeGrant); err == nil {
		t.Fatal("expired grant must reject input")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	rows := fixture.auditRows(t, auditKindLinkInput)
	if len(rows) != 3 {
		t.Fatalf("input audits = %+v", rows)
	}
	if rows[0]["outcome"] != "deny" || rows[0]["reason"] != "read_only" || rows[0]["share_id"] != readLink.ID {
		t.Fatalf("read-only input audit = %+v", rows[0])
	}
	if rows[1]["outcome"] != "allow" || rows[1]["share_id"] != writeLink.ID {
		t.Fatalf("read-write input audit = %+v", rows[1])
	}
	if rows[2]["outcome"] != "deny" || rows[2]["reason"] != "expired" {
		t.Fatalf("expired input audit = %+v", rows[2])
	}
}

func TestLinkInputStopsAfterRevocation(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	link, token, err := fixture.service.CreateLink(context.Background(), fixture.identity(owner), deviceID, ids.New(), true, 0)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.ResolveLink(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidateLinkInput(context.Background(), grant); err != nil {
		t.Fatalf("input must flow before revocation: %v", err)
	}

	if err := fixture.service.RevokeLink(context.Background(), fixture.identity(owner), link.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidateLinkInput(context.Background(), grant); err == nil {
		t.Fatal("revoked link must reject input even with a pre-revocation grant")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	rows := fixture.auditRows(t, auditKindLinkInput)
	last := rows[len(rows)-1]
	if last["outcome"] != "deny" || last["reason"] != "revoked" || last["share_id"] != link.ID {
		t.Fatalf("input audits = %+v", rows)
	}
}

func TestLinkRevokeAuditContainsShareID(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	other := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	link, _, err := fixture.service.CreateLink(context.Background(), fixture.identity(owner), deviceID, ids.New(), false, 0)
	if err != nil {
		t.Fatal(err)
	}

	if err := fixture.service.RevokeLink(context.Background(), fixture.identity(other), link.ID); err == nil {
		t.Fatal("non-owner revoke must fail")
	}
	if err := fixture.service.RevokeLink(context.Background(), fixture.identity(owner), link.ID); err != nil {
		t.Fatal(err)
	}

	rows := fixture.auditRows(t, auditKindLinkRevoke)
	if len(rows) != 2 {
		t.Fatalf("revoke audits = %+v", rows)
	}
	if rows[0]["outcome"] != "deny" || rows[0]["reason"] != "not_owner" || rows[0]["share_id"] != link.ID || rows[0]["session_id"] == "" {
		t.Fatalf("deny revoke audit = %+v", rows[0])
	}
	if rows[1]["outcome"] != "allow" || rows[1]["share_id"] != link.ID || rows[1]["owner_id"] != owner.ID {
		t.Fatalf("allow revoke audit = %+v", rows[1])
	}
}

func TestLinkNeverDisclosesCredentials(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	_, token, err := fixture.service.CreateLink(context.Background(), fixture.identity(owner), deviceID, ids.New(), true, 0)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.ResolveLink(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}

	var row string
	if err := fixture.db.QueryRowContext(context.Background(), `SELECT id || '|' || owner_id || '|' || device_id || '|' || session_id || '|' || token_hash || '|' || permission FROM share_link`).Scan(&row); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row, token) {
		t.Fatal("raw token leaked into share_link row")
	}
	encoded, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), token) {
		t.Fatal("raw token leaked into grant")
	}
	for _, kind := range []string{auditKindLinkCreate, auditKindLinkAccess} {
		for _, payload := range fixture.auditRows(t, kind) {
			if strings.Contains(payload["requester"].(string), token) {
				t.Fatalf("token leaked into %s audit", kind)
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), token) {
				t.Fatalf("token leaked into %s audit payload", kind)
			}
		}
	}
}

func TestLinkListScope(t *testing.T) {
	fixture := newServiceFixture(t)
	admin := fixture.createSuperadmin(t, "root")
	owner := fixture.createUser(t, "alice")
	other := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	if _, _, err := fixture.service.CreateLink(context.Background(), fixture.identity(owner), deviceID, ids.New(), false, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.service.CreateLink(context.Background(), fixture.identity(admin), deviceID, ids.New(), true, 0); err != nil {
		t.Fatal(err)
	}

	links, err := fixture.service.ListLinks(context.Background(), fixture.identity(owner))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Permission != PermissionRead {
		t.Fatalf("owner links = %+v", links)
	}
	links, err = fixture.service.ListLinks(context.Background(), fixture.identity(other))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("other user links = %+v", links)
	}
	links, err = fixture.service.ListLinks(context.Background(), fixture.identity(admin))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("superadmin links = %+v", links)
	}
}

func TestLinkRevokedDeviceStopsAccess(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	_, token, err := fixture.service.CreateLink(context.Background(), fixture.identity(owner), deviceID, ids.New(), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.ExecContext(context.Background(), "UPDATE user_device SET revoked_at = ? WHERE id = ?", fixture.now, deviceID); err != nil {
		t.Fatal(err)
	}

	_, err = fixture.service.ResolveLink(context.Background(), token)
	requireIPCCode(t, err, ipc.CodeForbidden)
	rows := fixture.auditRows(t, auditKindLinkAccess)
	last := rows[len(rows)-1]
	if last["outcome"] != "deny" || last["reason"] != "device_revoked" {
		t.Fatalf("access audits = %+v", rows)
	}
}
