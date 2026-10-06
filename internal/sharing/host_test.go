package sharing

import (
	"context"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

func TestHostShareCreateAndOpenTerminal(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	recipient := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")

	share, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, recipient.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if share.OwnerID != owner.ID || share.DeviceID != deviceID || share.RecipientID != recipient.ID {
		t.Fatalf("share binding = %+v", share)
	}
	if share.Permission != PermissionReadWrite {
		t.Fatalf("permission = %q, want %q", share.Permission, PermissionReadWrite)
	}
	if share.RecipientUsername != "bob" {
		t.Fatalf("recipient username = %q", share.RecipientUsername)
	}

	grant, err := fixture.service.AuthorizeTerminalOpen(context.Background(), fixture.identity(recipient), deviceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(grant.ShareIDs) != 1 || grant.ShareIDs[0] != share.ID {
		t.Fatalf("grant share ids = %v", grant.ShareIDs)
	}
	if grant.OwnerID != owner.ID || grant.Recipient != recipient.ID || grant.DeviceID != deviceID {
		t.Fatalf("grant binding = %+v", grant)
	}
	if grant.SessionID != "" {
		t.Fatalf("host share grant must not pin a session, got %q", grant.SessionID)
	}
	if grant.Permission != PermissionReadWrite || grant.ExpiresAt != share.ExpiresAt {
		t.Fatalf("grant permission/expiry = %+v", grant)
	}

	rows := fixture.auditRows(t, auditKindHostTerminal)
	if len(rows) != 1 || rows[0]["outcome"] != "allow" || rows[0]["recipient"] != recipient.ID {
		t.Fatalf("terminal audits = %+v", rows)
	}
}

func TestHostShareCreateAuthorization(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	other := fixture.createUser(t, "bob")
	recipient := fixture.createUser(t, "carol")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")

	_, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(other), deviceID, recipient.ID, true, 0)
	requireIPCCode(t, err, ipc.CodeForbidden)

	rows := fixture.auditRows(t, auditKindHostCreate)
	if len(rows) != 1 || rows[0]["outcome"] != "deny" || rows[0]["reason"] != "not_owner" {
		t.Fatalf("create audits = %+v", rows)
	}
}

func TestHostShareRequiresDaemonHost(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	recipient := fixture.createUser(t, "bob")
	plainDevice := fixture.createDevice(t, owner, "desktop-only")

	_, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), plainDevice, recipient.ID, true, 0)
	requireIPCCode(t, err, ipc.CodeForbidden)

	agentDevice := fixture.createAgentDevice(t, owner, "agent-host")
	fixture.setAgentOnline(t, agentDevice, false)
	_, err = fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), agentDevice, recipient.ID, true, 0)
	requireIPCCode(t, err, ipc.CodeDisconnected)
}

func TestHostShareRecipientValidation(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	disabled := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	if err := fixture.accounts.SetUserDisabled(context.Background(), disabled.ID, true); err != nil {
		t.Fatal(err)
	}

	_, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, owner.ID, true, 0)
	requireIPCCode(t, err, ipc.CodeBadParam)

	_, err = fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, "missing-user", true, 0)
	requireIPCCode(t, err, ipc.CodeNotFound)

	_, err = fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, disabled.ID, true, 0)
	requireIPCCode(t, err, ipc.CodeForbidden)
}

func TestHostShareRecipientIsolation(t *testing.T) {
	fixture := newServiceFixture(t)
	admin := fixture.createSuperadmin(t, "root")
	owner := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	carol := fixture.createUser(t, "carol")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")

	bobShare, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, bob.ID, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, carol.ID, true, 0); err != nil {
		t.Fatal(err)
	}
	adminShare, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(admin), deviceID, bob.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if adminShare.OwnerID != admin.ID {
		t.Fatalf("superadmin grant owner = %q, want %q", adminShare.OwnerID, admin.ID)
	}
	if adminShare.ID == bobShare.ID {
		t.Fatal("grants from different owners must be independent rows")
	}

	if err := fixture.service.RevokeHostShare(context.Background(), fixture.identity(owner), bobShare.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.AuthorizeTerminalOpen(context.Background(), fixture.identity(bob), deviceID); err != nil {
		t.Fatalf("bob must keep access through the superadmin share: %v", err)
	}
	grant, err := fixture.service.AuthorizeTerminalOpen(context.Background(), fixture.identity(carol), deviceID)
	if err != nil {
		t.Fatalf("carol's share must be unaffected by bob's revocation: %v", err)
	}
	if grant.Permission != PermissionReadWrite {
		t.Fatalf("carol permission = %q", grant.Permission)
	}

	if err := fixture.service.RevokeHostShare(context.Background(), fixture.identity(admin), adminShare.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.AuthorizeTerminalOpen(context.Background(), fixture.identity(bob), deviceID); err == nil {
		t.Fatal("bob must lose access once every share is revoked")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
}

func TestHostShareUnionPermissionAndEarliestExpiry(t *testing.T) {
	fixture := newServiceFixture(t)
	admin := fixture.createSuperadmin(t, "root")
	owner := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")

	share, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, bob.ID, false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	adminShare, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(admin), deviceID, bob.ID, true, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	grant, err := fixture.service.AuthorizeTerminalOpen(context.Background(), fixture.identity(bob), deviceID)
	if err != nil {
		t.Fatal(err)
	}
	if grant.Permission != PermissionReadWrite {
		t.Fatalf("union permission = %q, want %q", grant.Permission, PermissionReadWrite)
	}
	if grant.ExpiresAt != share.ExpiresAt {
		t.Fatalf("expiry = %d, want earliest %d", grant.ExpiresAt, share.ExpiresAt)
	}
	if len(grant.ShareIDs) != 2 {
		t.Fatalf("grant share ids = %v", grant.ShareIDs)
	}
	seen := map[string]bool{}
	for _, id := range grant.ShareIDs {
		seen[id] = true
	}
	if !seen[share.ID] || !seen[adminShare.ID] {
		t.Fatalf("grant share ids = %v", grant.ShareIDs)
	}
}

func TestHostShareExpiryStopsAccess(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	if _, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, bob.ID, true, minShareTTL); err != nil {
		t.Fatal(err)
	}

	fixture.now += minShareTTL.Milliseconds() + 1
	_, err := fixture.service.AuthorizeTerminalOpen(context.Background(), fixture.identity(bob), deviceID)
	requireIPCCode(t, err, ipc.CodeForbidden)

	rows := fixture.auditRows(t, auditKindHostTerminal)
	if len(rows) != 1 || rows[0]["outcome"] != "deny" || rows[0]["reason"] != "no_share" {
		t.Fatalf("terminal audits = %+v", rows)
	}
}

func TestHostShareRevokeAuthorization(t *testing.T) {
	fixture := newServiceFixture(t)
	admin := fixture.createSuperadmin(t, "root")
	owner := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	carol := fixture.createUser(t, "carol")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")

	ownerShare, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, bob.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	adminShare, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(admin), deviceID, carol.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}

	if err := fixture.service.RevokeHostShare(context.Background(), fixture.identity(bob), ownerShare.ID); err == nil {
		t.Fatal("recipient must not revoke the share")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	if err := fixture.service.RevokeHostShare(context.Background(), fixture.identity(owner), adminShare.ID); err != nil {
		t.Fatalf("device owner must revoke any share on their device: %v", err)
	}
	if err := fixture.service.RevokeHostShare(context.Background(), fixture.identity(admin), ownerShare.ID); err != nil {
		t.Fatalf("superadmin must revoke any share: %v", err)
	}
	if err := fixture.service.RevokeHostShare(context.Background(), fixture.identity(owner), ownerShare.ID); err != nil {
		t.Fatalf("repeated revoke must be idempotent: %v", err)
	}
	if err := fixture.service.RevokeHostShare(context.Background(), fixture.identity(owner), "missing-share"); err == nil {
		t.Fatal("unknown share must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeNotFound)
	}
}

func TestHostShareReshareRefreshes(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")

	share, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, bob.ID, false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RevokeHostShare(context.Background(), fixture.identity(owner), share.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.AuthorizeTerminalOpen(context.Background(), fixture.identity(bob), deviceID); err == nil {
		t.Fatal("revoked share must not authorize")
	}

	fixture.now += time.Minute.Milliseconds()
	reshare, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, bob.ID, true, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if reshare.ID != share.ID {
		t.Fatalf("reshare must reuse the triple row: %s vs %s", reshare.ID, share.ID)
	}
	if reshare.Permission != PermissionReadWrite || reshare.ExpiresAt != fixture.now+2*time.Hour.Milliseconds() {
		t.Fatalf("reshare = %+v", reshare)
	}
	if _, err := fixture.service.AuthorizeTerminalOpen(context.Background(), fixture.identity(bob), deviceID); err != nil {
		t.Fatalf("reshare must restore access: %v", err)
	}
}

func TestHostShareDaemonOfflineStopsOpen(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	if _, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, bob.ID, true, 0); err != nil {
		t.Fatal(err)
	}

	fixture.setAgentOnline(t, deviceID, false)
	_, err := fixture.service.AuthorizeTerminalOpen(context.Background(), fixture.identity(bob), deviceID)
	requireIPCCode(t, err, ipc.CodeDisconnected)

	rows := fixture.auditRows(t, auditKindHostTerminal)
	last := rows[len(rows)-1]
	if last["outcome"] != "deny" || last["reason"] != "agent_offline" {
		t.Fatalf("terminal audits = %+v", rows)
	}
}

func TestHostShareDeviceRevokedStopsOpen(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	if _, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, bob.ID, true, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.ExecContext(context.Background(), "UPDATE user_device SET revoked_at = ? WHERE id = ?", fixture.now, deviceID); err != nil {
		t.Fatal(err)
	}

	_, err := fixture.service.AuthorizeTerminalOpen(context.Background(), fixture.identity(bob), deviceID)
	requireIPCCode(t, err, ipc.CodeForbidden)
	rows := fixture.auditRows(t, auditKindHostTerminal)
	last := rows[len(rows)-1]
	if last["outcome"] != "deny" || last["reason"] != "device_revoked" {
		t.Fatalf("terminal audits = %+v", rows)
	}
}

func TestHostShareListScope(t *testing.T) {
	fixture := newServiceFixture(t)
	admin := fixture.createSuperadmin(t, "root")
	owner := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	carol := fixture.createUser(t, "carol")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	if _, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, bob.ID, true, 0); err != nil {
		t.Fatal(err)
	}

	shares, err := fixture.service.ListHostShares(context.Background(), fixture.identity(owner))
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 1 || shares[0].RecipientUsername != "bob" || shares[0].OwnerUsername != "alice" {
		t.Fatalf("owner shares = %+v", shares)
	}
	shares, err = fixture.service.ListHostShares(context.Background(), fixture.identity(bob))
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 1 || shares[0].RecipientID != bob.ID {
		t.Fatalf("recipient shares = %+v", shares)
	}
	shares, err = fixture.service.ListHostShares(context.Background(), fixture.identity(carol))
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 0 {
		t.Fatalf("unrelated shares = %+v", shares)
	}
	shares, err = fixture.service.ListHostShares(context.Background(), fixture.identity(admin))
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 1 {
		t.Fatalf("superadmin shares = %+v", shares)
	}
}
