package sharing

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestDeviceLinkCreateDefaultsReadOnly(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")

	link, token, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if link.Permission != PermissionRead {
		t.Fatalf("default permission = %q, want %q", link.Permission, PermissionRead)
	}
	if link.OwnerID != owner.ID || link.DeviceID != deviceID {
		t.Fatalf("link not bound to owner/device: %+v", link)
	}
	if link.NotBefore != fixture.now {
		t.Fatalf("not_before = %d, want immediate (now = %d)", link.NotBefore, fixture.now)
	}
	if link.ExpiresAt != fixture.now+defaultDeviceShareTTL.Milliseconds() {
		t.Fatalf("expiresAt = %d, want default TTL", link.ExpiresAt)
	}
	if token == "" {
		t.Fatal("token must be returned once")
	}
	var tokenHash string
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT token_hash FROM device_share_link WHERE id = ?", link.ID).Scan(&tokenHash); err != nil {
		t.Fatal(err)
	}
	if tokenHash == token {
		t.Fatal("raw token must not be stored")
	}
}

func TestDeviceLinkCreateWithNotBeforeAndWrite(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")

	notBefore := fixture.now + time.Hour.Milliseconds()
	link, _, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, true, notBefore, 6*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if link.Permission != PermissionReadWrite {
		t.Fatalf("permission = %q, want %q", link.Permission, PermissionReadWrite)
	}
	if link.NotBefore != notBefore {
		t.Fatalf("not_before = %d, want %d", link.NotBefore, notBefore)
	}
	if link.ExpiresAt != fixture.now+6*time.Hour.Milliseconds() {
		t.Fatalf("expiresAt = %d, want created + 6h", link.ExpiresAt)
	}

	// 生效时刻不早于过期时刻一律拒绝。
	if _, _, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, false, fixture.now+time.Hour.Milliseconds(), 30*time.Minute); err == nil {
		t.Fatal("not_before at/after expiry must be rejected")
	} else {
		requireIPCCode(t, err, ipc.CodeBadParam)
	}
	// 过去的生效时刻按当前时刻收敛 (立即生效)。
	past, _, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, false, fixture.now-time.Hour.Milliseconds(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if past.NotBefore != fixture.now {
		t.Fatalf("past not_before = %d, want clamped to now %d", past.NotBefore, fixture.now)
	}
}

func TestDeviceLinkCreateAuthorization(t *testing.T) {
	fixture := newServiceFixture(t)
	admin := fixture.createSuperadmin(t, "root")
	owner := fixture.createUser(t, "alice")
	other := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")

	if _, _, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(other), deviceID, false, 0, 0); err == nil {
		t.Fatal("non-owner create must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	if _, _, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(admin), deviceID, false, 0, 0); err != nil {
		t.Fatalf("superadmin create: %v", err)
	}

	rows := fixture.auditRows(t, auditKindDeviceLinkCreate)
	if len(rows) < 2 || rows[0]["outcome"] != "deny" || rows[0]["reason"] != "not_owner" {
		t.Fatalf("create audits = %+v", rows)
	}
}

func TestDeviceLinkCreateRequiresDaemonHost(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	plainDevice := fixture.createDevice(t, owner, "desktop-only")

	if _, _, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), plainDevice, false, 0, 0); err == nil {
		t.Fatal("plain device must be rejected")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	agentDevice := fixture.createAgentDevice(t, owner, "agent-host")
	fixture.setAgentOnline(t, agentDevice, false)
	if _, _, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), agentDevice, false, 0, 0); err == nil {
		t.Fatal("offline agent must be rejected")
	} else {
		requireIPCCode(t, err, ipc.CodeDisconnected)
	}
}

func TestDeviceLinkResolve(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")
	link, token, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, true, 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	grant, err := fixture.service.ResolveDeviceLink(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if len(grant.ShareIDs) != 1 || grant.ShareIDs[0] != link.ID {
		t.Fatalf("grant share ids = %v", grant.ShareIDs)
	}
	if grant.DeviceID != deviceID || grant.OwnerID != owner.ID {
		t.Fatalf("grant binding = %+v", grant)
	}
	if grant.SessionID != "" {
		t.Fatalf("device link grant must not bind a session, got %q", grant.SessionID)
	}
	if grant.Recipient != "" {
		t.Fatalf("device link grant must be anonymous, got recipient %q", grant.Recipient)
	}
	if grant.Permission != PermissionReadWrite || grant.ExpiresAt != link.ExpiresAt {
		t.Fatalf("grant permission/expiry = %+v", grant)
	}

	var lastAccessedAt int64
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT last_accessed_at FROM device_share_link WHERE id = ?", link.ID).Scan(&lastAccessedAt); err != nil {
		t.Fatal(err)
	}
	if lastAccessedAt != fixture.now {
		t.Fatalf("last_accessed_at = %d, want %d", lastAccessedAt, fixture.now)
	}

	rows := fixture.auditRows(t, auditKindDeviceLinkAccess)
	if len(rows) != 1 || rows[0]["outcome"] != "allow" || rows[0]["share_id"] != link.ID {
		t.Fatalf("access audits = %+v", rows)
	}
}

func TestDeviceLinkResolveNotYetValid(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")
	notBefore := fixture.now + 30*time.Minute.Milliseconds()
	_, token, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, false, notBefore, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.service.ResolveDeviceLink(context.Background(), token); err == nil {
		t.Fatal("link must not resolve before not_before")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	rows := fixture.auditRows(t, auditKindDeviceLinkAccess)
	if len(rows) != 1 || rows[0]["outcome"] != "deny" || rows[0]["reason"] != "not_yet_valid" {
		t.Fatalf("access audits = %+v", rows)
	}

	fixture.now = notBefore
	// 时间推进后同步刷新心跳, 否则 staleness 窗口会把代理判离线。
	fixture.setAgentOnline(t, deviceID, true)
	if _, err := fixture.service.ResolveDeviceLink(context.Background(), token); err != nil {
		t.Fatalf("link must resolve at not_before: %v", err)
	}
}

func TestDeviceLinkResolveInvalidToken(t *testing.T) {
	fixture := newServiceFixture(t)

	_, err := fixture.service.ResolveDeviceLink(context.Background(), "not-a-real-token")
	requireIPCCode(t, err, ipc.CodeForbidden)

	rows := fixture.auditRows(t, auditKindDeviceLinkAccess)
	if len(rows) != 1 || rows[0]["outcome"] != "deny" || rows[0]["reason"] != "not_found" {
		t.Fatalf("access audits = %+v", rows)
	}
}

func TestDeviceLinkExpiryStopsAccess(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")
	_, token, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, false, 0, minShareTTL)
	if err != nil {
		t.Fatal(err)
	}

	fixture.now += minShareTTL.Milliseconds() + 1
	if _, err := fixture.service.ResolveDeviceLink(context.Background(), token); err == nil {
		t.Fatal("expired link must not resolve")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	rows := fixture.auditRows(t, auditKindDeviceLinkAccess)
	if len(rows) != 1 || rows[0]["outcome"] != "deny" || rows[0]["reason"] != "expired" {
		t.Fatalf("access audits = %+v", rows)
	}
}

func TestDeviceLinkResolveDeviceRevoked(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")
	_, token, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.ExecContext(context.Background(), "UPDATE user_device SET revoked_at = ? WHERE id = ?", fixture.now, deviceID); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.service.ResolveDeviceLink(context.Background(), token); err == nil {
		t.Fatal("device-revoked link must not resolve")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	rows := fixture.auditRows(t, auditKindDeviceLinkAccess)
	if last := rows[len(rows)-1]; last["outcome"] != "deny" || last["reason"] != "device_revoked" {
		t.Fatalf("access audits = %+v", rows)
	}
}

func TestDeviceLinkResolveAgentOffline(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")
	_, token, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	fixture.setAgentOnline(t, deviceID, false)

	if _, err := fixture.service.ResolveDeviceLink(context.Background(), token); err == nil {
		t.Fatal("offline agent must not resolve")
	} else {
		requireIPCCode(t, err, ipc.CodeDisconnected)
	}
	rows := fixture.auditRows(t, auditKindDeviceLinkAccess)
	if last := rows[len(rows)-1]; last["outcome"] != "deny" || last["reason"] != "agent_offline" {
		t.Fatalf("access audits = %+v", rows)
	}
}

func TestDeviceLinkRevocationStopsAccess(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	other := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")
	link, token, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	if err := fixture.service.RevokeDeviceLink(context.Background(), fixture.identity(other), link.ID); err == nil {
		t.Fatal("non-owner revoke must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	if _, err := fixture.service.ResolveDeviceLink(context.Background(), token); err != nil {
		t.Fatalf("link must stay active after failed revoke: %v", err)
	}

	if err := fixture.service.RevokeDeviceLink(context.Background(), fixture.identity(owner), link.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RevokeDeviceLink(context.Background(), fixture.identity(owner), link.ID); err != nil {
		t.Fatalf("repeated revoke must be idempotent: %v", err)
	}
	if _, err := fixture.service.ResolveDeviceLink(context.Background(), token); err == nil {
		t.Fatal("revoked link must not resolve")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	rows := fixture.auditRows(t, auditKindDeviceLinkAccess)
	if last := rows[len(rows)-1]; last["outcome"] != "deny" || last["reason"] != "revoked" {
		t.Fatalf("access audits = %+v", rows)
	}
}

func TestDeviceLinkInputRevalidation(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")

	readLink, readToken, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	readGrant, err := fixture.service.ResolveDeviceLink(context.Background(), readToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidateDeviceLinkInput(context.Background(), readGrant); err == nil {
		t.Fatal("read-only grant must reject input")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	writeLink, writeToken, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeGrant, err := fixture.service.ResolveDeviceLink(context.Background(), writeToken)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := fixture.service.RevalidateDeviceLinkInput(context.Background(), writeGrant)
	if err != nil {
		t.Fatalf("read-write grant must allow input: %v", err)
	}
	if refreshed.Permission != PermissionReadWrite || refreshed.ExpiresAt != writeGrant.ExpiresAt {
		t.Fatalf("refreshed grant = %+v", refreshed)
	}

	fixture.now = writeGrant.ExpiresAt
	if _, err := fixture.service.RevalidateDeviceLinkInput(context.Background(), writeGrant); err == nil {
		t.Fatal("expired grant must reject input")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	rows := fixture.auditRows(t, auditKindDeviceLinkInput)
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

func TestDeviceLinkInputStopsAfterRevocation(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")
	link, token, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.ResolveDeviceLink(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidateDeviceLinkInput(context.Background(), grant); err != nil {
		t.Fatalf("input must flow before revocation: %v", err)
	}

	if err := fixture.service.RevokeDeviceLink(context.Background(), fixture.identity(owner), link.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidateDeviceLinkInput(context.Background(), grant); err == nil {
		t.Fatal("revoked link must reject input even with a pre-revocation grant")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}

	rows := fixture.auditRows(t, auditKindDeviceLinkInput)
	if last := rows[len(rows)-1]; last["outcome"] != "deny" || last["reason"] != "revoked" || last["share_id"] != link.ID {
		t.Fatalf("input audits = %+v", rows)
	}
}

func TestDeviceLinkPermissionShrinkStopsInput(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")
	_, token, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.ResolveDeviceLink(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidateDeviceLinkInput(context.Background(), grant); err != nil {
		t.Fatalf("input must flow before shrink: %v", err)
	}

	// 权限收缩 (read_write -> read) 按当前行即时生效: 输入停止, 输出授权仍在。
	if _, err := fixture.db.ExecContext(context.Background(), "UPDATE device_share_link SET permission = 'read' WHERE device_id = ?", deviceID); err != nil {
		t.Fatal(err)
	}
	refreshed, err := fixture.service.RevalidateDeviceLink(context.Background(), grant)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Permission != PermissionRead {
		t.Fatalf("refreshed permission = %q, want read", refreshed.Permission)
	}
	if _, err := fixture.service.RevalidateDeviceLinkInput(context.Background(), grant); err == nil {
		t.Fatal("shrunk grant must reject input")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	rows := fixture.auditRows(t, auditKindDeviceLinkInput)
	if last := rows[len(rows)-1]; last["outcome"] != "deny" || last["reason"] != "read_only" {
		t.Fatalf("input audits = %+v", rows)
	}
}

func TestDeviceLinkRevalidateStopsOnDeviceRevoked(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")
	_, token, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.ResolveDeviceLink(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.ExecContext(context.Background(), "UPDATE user_device SET revoked_at = ? WHERE id = ?", fixture.now, deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidateDeviceLink(context.Background(), grant); err == nil {
		t.Fatal("device-revoked grant must fail revalidation")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
}

func TestDeviceLinkRevalidateStopsOnOwnerMismatch(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	other := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")
	_, token, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.ResolveDeviceLink(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.ExecContext(context.Background(), "UPDATE user_device SET user_id = ? WHERE id = ?", other.ID, deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RevalidateDeviceLink(context.Background(), grant); err == nil {
		t.Fatal("owner-mismatched grant must fail revalidation")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
}

func TestDeviceLinkGateStopsOnRevoke(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")
	link, token, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.ResolveDeviceLink(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	// Gate 时钟独立于服务时钟: 递增即越过 TTL 缓存窗口, 触发对当前行的真实重查。
	gateNow := time.Now().UnixMilli()
	gate := fixture.service.NewDeviceLinkGate(grant, WithGateNow(func() int64 { return gateNow }))

	if err := fixture.service.RevokeDeviceLink(context.Background(), fixture.identity(owner), link.ID); err != nil {
		t.Fatal(err)
	}
	gateNow += linkRevalidateTTL.Milliseconds() + 10

	var out bytes.Buffer
	if err := gate.PipeOutput(context.Background(), &out, bytes.NewReader([]byte("output"))); err == nil {
		t.Fatal("output must stop after revocation")
	}
	var in bytes.Buffer
	if err := gate.PipeInput(context.Background(), &in, bytes.NewReader([]byte("ls\n"))); err == nil {
		t.Fatal("input must stop after revocation")
	}
}

func TestDeviceLinkListScope(t *testing.T) {
	fixture := newServiceFixture(t)
	admin := fixture.createSuperadmin(t, "root")
	owner := fixture.createUser(t, "alice")
	other := fixture.createUser(t, "bob")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")
	if _, _, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, false, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(admin), deviceID, true, 0, 0); err != nil {
		t.Fatal(err)
	}

	links, err := fixture.service.ListDeviceLinks(context.Background(), fixture.identity(owner))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Permission != PermissionRead {
		t.Fatalf("owner links = %+v", links)
	}
	links, err = fixture.service.ListDeviceLinks(context.Background(), fixture.identity(other))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("other user links = %+v", links)
	}
	links, err = fixture.service.ListDeviceLinks(context.Background(), fixture.identity(admin))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("admin links = %+v", links)
	}
}

func TestDeviceLinkNeverDisclosesCredentials(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "alice")
	deviceID := fixture.createAgentDevice(t, owner, "edge-box")
	_, token, err := fixture.service.CreateDeviceLink(context.Background(), fixture.identity(owner), deviceID, true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.service.ResolveDeviceLink(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}

	var row string
	if err := fixture.db.QueryRowContext(context.Background(), `SELECT id || '|' || owner_id || '|' || device_id || '|' || token_hash || '|' || permission FROM device_share_link`).Scan(&row); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row, token) {
		t.Fatal("raw token leaked into device_share_link row")
	}
	encoded, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), token) {
		t.Fatal("raw token leaked into grant")
	}
	for _, kind := range []string{auditKindDeviceLinkCreate, auditKindDeviceLinkAccess} {
		for _, payload := range fixture.auditRows(t, kind) {
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
