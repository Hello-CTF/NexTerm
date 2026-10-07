package sharing

import (
	"bytes"
	"context"
	"errors"
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

func TestHostShareCreatePopulatesUsernames(t *testing.T) {
	fixture := newServiceFixture(t)
	admin := fixture.createSuperadmin(t, "root")
	owner := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")

	ownerShare, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, bob.ID, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ownerShare.OwnerUsername != "alice" || ownerShare.RecipientUsername != "bob" {
		t.Fatalf("owner share usernames = %q/%q, want %q/%q", ownerShare.OwnerUsername, ownerShare.RecipientUsername, "alice", "bob")
	}

	adminShare, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(admin), deviceID, bob.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if adminShare.OwnerUsername != "root" || adminShare.RecipientUsername != "bob" {
		t.Fatalf("superadmin share usernames = %q/%q, want %q/%q", adminShare.OwnerUsername, adminShare.RecipientUsername, "root", "bob")
	}

	shares, err := fixture.service.ListHostShares(context.Background(), fixture.identity(admin))
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 2 {
		t.Fatalf("list shares = %+v", shares)
	}
	byID := make(map[string]*HostShare, len(shares))
	for _, share := range shares {
		byID[share.ID] = share
	}
	for _, created := range []*HostShare{ownerShare, adminShare} {
		listed := byID[created.ID]
		if listed == nil {
			t.Fatalf("share %s missing from list", created.ID)
		}
		if listed.OwnerUsername != created.OwnerUsername || listed.RecipientUsername != created.RecipientUsername {
			t.Fatalf("share %s create usernames = %q/%q, list = %q/%q", created.ID,
				created.OwnerUsername, created.RecipientUsername, listed.OwnerUsername, listed.RecipientUsername)
		}
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

func TestHostShareUnionPermissionPerLevelExpiry(t *testing.T) {
	fixture := newServiceFixture(t)
	admin := fixture.createSuperadmin(t, "root")
	owner := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")

	readShare, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, bob.ID, false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	writeShare, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(admin), deviceID, bob.ID, true, 2*time.Hour)
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
	if grant.ExpiresAt != writeShare.ExpiresAt {
		t.Fatalf("read_write expiry = %d, want the read_write row's %d (not the shorter read row %d)", grant.ExpiresAt, writeShare.ExpiresAt, readShare.ExpiresAt)
	}
	if len(grant.ShareIDs) != 2 {
		t.Fatalf("grant share ids = %v", grant.ShareIDs)
	}
	seen := map[string]bool{}
	for _, id := range grant.ShareIDs {
		seen[id] = true
	}
	if !seen[readShare.ID] || !seen[writeShare.ID] {
		t.Fatalf("grant share ids = %v", grant.ShareIDs)
	}

	fixture.now = readShare.ExpiresAt + 1
	refreshed, err := fixture.service.RevalidateHostAccess(context.Background(), grant)
	if err != nil {
		t.Fatalf("shorter read row expiring must not terminate the read_write grant: %v", err)
	}
	if refreshed.Permission != PermissionReadWrite || refreshed.ExpiresAt != writeShare.ExpiresAt {
		t.Fatalf("refreshed = %+v", refreshed)
	}
	if len(refreshed.ShareIDs) != 1 || refreshed.ShareIDs[0] != writeShare.ID {
		t.Fatalf("refreshed share ids = %v", refreshed.ShareIDs)
	}
}

func TestHostShareRevalidateShrinksAndStops(t *testing.T) {
	fixture := newServiceFixture(t)
	admin := fixture.createSuperadmin(t, "root")
	owner := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")

	readShare, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, bob.ID, false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	writeShare, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(admin), deviceID, bob.ID, true, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.AuthorizeTerminalOpen(context.Background(), fixture.identity(bob), deviceID)
	if err != nil {
		t.Fatal(err)
	}

	if err := fixture.service.RevokeHostShare(context.Background(), fixture.identity(admin), writeShare.ID); err != nil {
		t.Fatal(err)
	}
	refreshed, err := fixture.service.RevalidateHostAccess(context.Background(), grant)
	if err != nil {
		t.Fatalf("revoking the read_write share must fall back to the surviving read share: %v", err)
	}
	if refreshed.Permission != PermissionRead {
		t.Fatalf("refreshed permission = %q, want %q", refreshed.Permission, PermissionRead)
	}
	if refreshed.ExpiresAt != readShare.ExpiresAt {
		t.Fatalf("refreshed expiry = %d, want %d", refreshed.ExpiresAt, readShare.ExpiresAt)
	}
	if len(refreshed.ShareIDs) != 1 || refreshed.ShareIDs[0] != readShare.ID {
		t.Fatalf("refreshed share ids = %v", refreshed.ShareIDs)
	}

	if err := fixture.service.RevokeHostShare(context.Background(), fixture.identity(owner), readShare.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidateHostAccess(context.Background(), grant); err == nil {
		t.Fatal("all shares revoked must stop the session")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
}

func TestHostShareInputStopsAfterRevocation(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	share, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, bob.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.AuthorizeTerminalOpen(context.Background(), fixture.identity(bob), deviceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidateHostAccess(context.Background(), grant); err != nil {
		t.Fatalf("access must flow before revocation: %v", err)
	}

	if err := fixture.service.RevokeHostShare(context.Background(), fixture.identity(owner), share.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidateHostAccess(context.Background(), grant); err == nil {
		t.Fatal("revoked share must reject revalidation even with a pre-revocation grant")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
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

func TestHostGateShrinksToReadThenStops(t *testing.T) {
	fixture := newServiceFixture(t)
	admin := fixture.createSuperadmin(t, "root")
	owner := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	writeShare, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(admin), deviceID, bob.ID, true, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	readShare, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, bob.ID, false, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.AuthorizeTerminalOpen(context.Background(), fixture.identity(bob), deviceID)
	if err != nil {
		t.Fatal(err)
	}
	if grant.Permission != PermissionReadWrite || grant.ExpiresAt != writeShare.ExpiresAt {
		t.Fatalf("grant = %+v", grant)
	}
	gate := fixture.service.NewHostGate(grant)

	fixture.now = writeShare.ExpiresAt + 1
	var output bytes.Buffer
	if err := gate.PipeOutput(context.Background(), &output, bytes.NewReader([]byte("screen"))); err != nil {
		t.Fatalf("output must continue under the surviving read share: %v", err)
	}
	if output.String() != "screen" {
		t.Fatalf("output = %q", output.String())
	}
	if gate.InputAllowed() {
		t.Fatal("gate must hold the shrunk read grant")
	}
	var input bytes.Buffer
	if err := gate.PipeInput(context.Background(), &input, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrInputNotAllowed) {
		t.Fatalf("input err = %v, want ErrInputNotAllowed", err)
	}
	if input.Len() != 0 {
		t.Fatalf("shrunk gate forwarded %q", input.String())
	}

	if err := fixture.service.RevokeHostShare(context.Background(), fixture.identity(owner), readShare.ID); err != nil {
		t.Fatal(err)
	}
	err = gate.PipeOutput(context.Background(), &output, bytes.NewReader([]byte("more")))
	requireIPCCode(t, err, ipc.CodeForbidden)
	if output.String() != "screen" {
		t.Fatalf("output after full revocation = %q", output.String())
	}
}

func TestHostGateStopsAfterRevocation(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	share, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, bob.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.AuthorizeTerminalOpen(context.Background(), fixture.identity(bob), deviceID)
	if err != nil {
		t.Fatal(err)
	}
	gate := fixture.service.NewHostGate(grant)

	if err := fixture.service.RevokeHostShare(context.Background(), fixture.identity(owner), share.ID); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = gate.PipeOutput(context.Background(), &output, bytes.NewReader([]byte("screen")))
	requireIPCCode(t, err, ipc.CodeForbidden)
	if output.Len() != 0 {
		t.Fatalf("revoked gate forwarded output %q", output.String())
	}
	var input bytes.Buffer
	err = gate.PipeInput(context.Background(), &input, bytes.NewReader([]byte("x")))
	requireIPCCode(t, err, ipc.CodeForbidden)
	if input.Len() != 0 {
		t.Fatalf("revoked gate forwarded input %q", input.String())
	}
}

func TestRevalidateHostOwnerMismatchAndDeviceRevoked(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	mallory := fixture.createUser(t, "mallory")
	bob := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "build-host")
	if _, err := fixture.service.CreateHostShare(context.Background(), fixture.identity(owner), deviceID, bob.ID, true, 0); err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.AuthorizeTerminalOpen(context.Background(), fixture.identity(bob), deviceID)
	if err != nil {
		t.Fatal(err)
	}

	tampered := *grant
	tampered.OwnerID = mallory.ID
	if _, err := fixture.service.RevalidateHostAccess(context.Background(), &tampered); err == nil {
		t.Fatal("tampered owner must fail revalidation")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	if _, err := fixture.db.ExecContext(context.Background(), "UPDATE user_device SET revoked_at = ? WHERE id = ?", fixture.now, deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidateHostAccess(context.Background(), grant); err == nil {
		t.Fatal("revoked device must fail revalidation")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
}
