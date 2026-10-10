package fleetserver

import (
	"context"
	"database/sql"
	"net/http"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestFleetEnrollFirstDeviceAutoActive(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "alice")

	first := fixture.enrollAgent(t, alice, "first-host")
	if first.State != DeviceStateActive {
		t.Fatalf("first device state=%q, want %q", first.State, DeviceStateActive)
	}
	var state string
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT state FROM user_device WHERE id = ?", first.DeviceID).Scan(&state); err != nil || state != DeviceStateActive {
		t.Fatalf("db state=%q err=%v", state, err)
	}
	second := fixture.enrollAgent(t, alice, "second-host")
	if second.State != DeviceStatePending {
		t.Fatalf("second device state=%q, want %q", second.State, DeviceStatePending)
	}

	// 账号已有任意设备行 (含桌面注册设备) 时, 第一台 fleet 设备同样进入待审批。
	bob := fixture.createUser(t, "bob")
	if _, err := fixture.accounts.RegisterDevice(context.Background(), bob.ID, "desktop", "desktop"); err != nil {
		t.Fatal(err)
	}
	agentResult := fixture.enrollAgent(t, bob, "bob-host")
	if agentResult.State != DeviceStatePending {
		t.Fatalf("state=%q, want %q (account already has a device)", agentResult.State, DeviceStatePending)
	}
}

func TestFleetPendingDeviceApproveLifecycle(t *testing.T) {
	fixture := newServiceFixture(t)
	root := fixture.createSuperadmin(t, "root")
	alice := fixture.createUser(t, "alice")
	fixture.enrollAgent(t, alice, "first-host")
	pending := fixture.enrollAgent(t, alice, "pending-host")
	if pending.State != DeviceStatePending {
		t.Fatalf("state=%q", pending.State)
	}

	devices, err := fixture.service.ListDevices(context.Background(), fixture.identity(alice))
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, device := range devices {
		states[device.ID] = device.State
	}
	if states[pending.DeviceID] != DeviceStatePending {
		t.Fatalf("list states=%v", states)
	}

	if _, err := fixture.service.AuthenticateAgent(context.Background(), pending.DeviceID, pending.Secret); err == nil {
		t.Fatal("pending device must not authenticate")
	} else {
		requireIPCCode(t, err, ipc.CodeDevicePending)
	}
	var lastSeen sql.NullInt64
	if err := fixture.db.QueryRowContext(context.Background(), "SELECT last_seen_at FROM user_device WHERE id = ?", pending.DeviceID).Scan(&lastSeen); err != nil || lastSeen.Valid {
		t.Fatalf("pending device must not touch heartbeat, last_seen=%v err=%v", lastSeen, err)
	}

	if err := fixture.service.ApproveDevice(context.Background(), fixture.identity(root), pending.DeviceID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if userID, err := fixture.service.AuthenticateAgent(context.Background(), pending.DeviceID, pending.Secret); err != nil || userID != alice.ID {
		t.Fatalf("approved authenticate user=%q err=%v", userID, err)
	}
	audits := fixture.auditRows(t, auditKindDeviceApprove)
	if len(audits) != 1 || audits[0]["outcome"] != "allow" || audits[0]["device_id"] != pending.DeviceID || audits[0]["requester"] != root.ID {
		t.Fatalf("approve audits=%v", audits)
	}
	if ids := auditAssetIDs(t, fixture.db, auditKindDeviceApprove); len(ids) != 1 || ids[0] != pending.DeviceID {
		t.Fatalf("approve audit asset_ids=%v", ids)
	}
}

func TestFleetApproveRejectPermissionBoundary(t *testing.T) {
	fixture := newServiceFixture(t)
	root := fixture.createSuperadmin(t, "root")
	alice := fixture.createUser(t, "alice")
	bob := fixture.createUser(t, "bob")
	active := fixture.enrollAgent(t, alice, "active-host")
	pending := fixture.enrollAgent(t, alice, "pending-host")

	// 仅超管可审批: 设备 owner 与旁观者一律拒绝。
	if err := fixture.service.ApproveDevice(context.Background(), fixture.identity(alice), pending.DeviceID); err == nil {
		t.Fatal("owner approve must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	if err := fixture.service.RejectDevice(context.Background(), fixture.identity(bob), pending.DeviceID); err == nil {
		t.Fatal("non-admin reject must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	approveAudits := fixture.auditRows(t, auditKindDeviceApprove)
	if len(approveAudits) != 1 || approveAudits[0]["outcome"] != "deny" || approveAudits[0]["reason"] != "not_superadmin" {
		t.Fatalf("approve audits=%v", approveAudits)
	}
	rejectAudits := fixture.auditRows(t, auditKindDeviceReject)
	if len(rejectAudits) != 1 || rejectAudits[0]["outcome"] != "deny" || rejectAudits[0]["reason"] != "not_superadmin" {
		t.Fatalf("reject audits=%v", rejectAudits)
	}

	// 仅 pending 设备可审批: active / 不存在 / 已吊销全部拒绝。
	if err := fixture.service.ApproveDevice(context.Background(), fixture.identity(root), active.DeviceID); err == nil {
		t.Fatal("approve active must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeBadParam)
	}
	if err := fixture.service.RejectDevice(context.Background(), fixture.identity(root), active.DeviceID); err == nil {
		t.Fatal("reject active must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeBadParam)
	}
	if err := fixture.service.ApproveDevice(context.Background(), fixture.identity(root), "missing-device"); err == nil {
		t.Fatal("approve missing must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeNotFound)
	}
	if err := fixture.service.RevokeDevice(context.Background(), fixture.identity(alice), pending.DeviceID); err != nil {
		t.Fatalf("owner revoke pending: %v", err)
	}
	if err := fixture.service.ApproveDevice(context.Background(), fixture.identity(root), pending.DeviceID); err == nil {
		t.Fatal("approve revoked must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
}

func TestFleetRejectDevice(t *testing.T) {
	fixture := newServiceFixture(t)
	root := fixture.createSuperadmin(t, "root")
	alice := fixture.createUser(t, "alice")
	fixture.enrollAgent(t, alice, "first-host")
	pending := fixture.enrollAgent(t, alice, "pending-host")

	if err := fixture.service.RejectDevice(context.Background(), fixture.identity(root), pending.DeviceID); err != nil {
		t.Fatalf("reject: %v", err)
	}
	for table, column := range map[string]string{"user_device": "id", "device_agent": "device_id", "sync_credential": "device_id"} {
		var count int
		if err := fixture.db.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table+" WHERE "+column+" = ?", pending.DeviceID).Scan(&count); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows=%d after reject", table, count)
		}
	}
	if _, err := fixture.service.AuthenticateAgent(context.Background(), pending.DeviceID, pending.Secret); err == nil {
		t.Fatal("rejected device must not authenticate")
	} else {
		requireIPCCode(t, err, ipc.CodeForbidden)
	}
	audits := fixture.auditRows(t, auditKindDeviceReject)
	if len(audits) != 1 || audits[0]["outcome"] != "allow" || audits[0]["device_id"] != pending.DeviceID {
		t.Fatalf("reject audits=%v", audits)
	}
	if ids := auditAssetIDs(t, fixture.db, auditKindDeviceReject); len(ids) != 1 || ids[0] != pending.DeviceID {
		t.Fatalf("reject audit asset_ids=%v", ids)
	}
	if err := fixture.service.RejectDevice(context.Background(), fixture.identity(root), pending.DeviceID); err == nil {
		t.Fatal("second reject must fail")
	} else {
		requireIPCCode(t, err, ipc.CodeNotFound)
	}
}

func TestFleetHTTPDeviceApprovalFlow(t *testing.T) {
	fixture := newHTTPFixture(t, false)
	root := fixture.createSuperadmin(t, "root")
	alice := fixture.createUser(t, "alice")
	rootSession := fixture.session(t, root)
	aliceSession := fixture.session(t, alice)

	enroll := func(name string) httpCall {
		issue := fixture.call(t, "POST", "/device/enroll-codes", map[string]any{"ttl_ms": 600000}, aliceSession, aliceSession.csrf)
		if issue.status != http.StatusOK {
			t.Fatalf("issue: HTTP %d %v", issue.status, issue.body)
		}
		return fixture.call(t, "POST", "/device/enroll", map[string]any{"code": issue.body["code"], "name": name, "platform": "linux"}, nil, "")
	}

	first := enroll("first-host")
	if first.status != http.StatusOK || first.body["state"] != DeviceStateActive {
		t.Fatalf("first enroll = HTTP %d %v", first.status, first.body)
	}
	second := enroll("second-host")
	if second.status != http.StatusOK || second.body["state"] != DeviceStatePending {
		t.Fatalf("second enroll = HTTP %d %v", second.status, second.body)
	}
	deviceID, _ := second.body["device_id"].(string)
	secret, _ := second.body["secret"].(string)

	// pending 设备同步数据: 423 + device_pending; 不能是 403 (agent 合同里 403=吊销停止)。
	sync := fixture.call(t, "POST", "/agent/sync", map[string]any{"device_id": deviceID, "secret": secret}, nil, "")
	if sync.status != http.StatusLocked {
		t.Fatalf("pending sync = HTTP %d %v", sync.status, sync.body)
	}
	if code := sync.body["error"].(map[string]any)["code"]; code != string(ipc.CodeDevicePending) {
		t.Fatalf("pending sync code = %v", code)
	}

	if call := fixture.call(t, "POST", "/fleet/devices/"+deviceID+"/approve", map[string]any{}, aliceSession, aliceSession.csrf); call.status != http.StatusForbidden {
		t.Fatalf("owner approve = HTTP %d %v", call.status, call.body)
	}
	if call := fixture.call(t, "POST", "/fleet/devices/"+deviceID+"/approve", map[string]any{}, rootSession, ""); call.status != http.StatusForbidden {
		t.Fatalf("no-csrf approve = HTTP %d %v", call.status, call.body)
	}
	if call := fixture.call(t, "POST", "/fleet/devices/"+deviceID+"/approve", map[string]any{}, rootSession, rootSession.csrf); call.status != http.StatusOK {
		t.Fatalf("admin approve = HTTP %d %v", call.status, call.body)
	}
	if sync := fixture.call(t, "POST", "/agent/sync", map[string]any{"device_id": deviceID, "secret": secret}, nil, ""); sync.status != http.StatusOK {
		t.Fatalf("approved sync = HTTP %d %v", sync.status, sync.body)
	}

	third := enroll("third-host")
	if third.body["state"] != DeviceStatePending {
		t.Fatalf("third enroll = %v", third.body)
	}
	thirdID, _ := third.body["device_id"].(string)
	thirdSecret, _ := third.body["secret"].(string)
	if call := fixture.call(t, "POST", "/fleet/devices/"+thirdID+"/reject", map[string]any{}, aliceSession, aliceSession.csrf); call.status != http.StatusForbidden {
		t.Fatalf("owner reject = HTTP %d %v", call.status, call.body)
	}
	if call := fixture.call(t, "POST", "/fleet/devices/"+thirdID+"/reject", map[string]any{}, rootSession, rootSession.csrf); call.status != http.StatusOK {
		t.Fatalf("admin reject = HTTP %d %v", call.status, call.body)
	}

	list := fixture.call(t, http.MethodGet, "/fleet/devices", nil, aliceSession, "")
	states := map[string]string{}
	for _, raw := range list.body["devices"].([]any) {
		view := raw.(map[string]any)
		states[view["id"].(string)], _ = view["state"].(string)
	}
	if states[deviceID] != DeviceStateActive {
		t.Fatalf("approved device state in list = %q (%v)", states[deviceID], states)
	}
	if _, ok := states[thirdID]; ok {
		t.Fatalf("rejected device still listed: %v", states)
	}
	if sync := fixture.call(t, "POST", "/agent/sync", map[string]any{"device_id": thirdID, "secret": thirdSecret}, nil, ""); sync.status != http.StatusForbidden {
		t.Fatalf("rejected sync = HTTP %d %v", sync.status, sync.body)
	}
}

func TestDeviceWSPendingDeviceApproval(t *testing.T) {
	f := newHTTPFixture(t, false)
	root := f.createSuperadmin(t, "root")
	owner := f.createUser(t, "ws-owner")
	enrollWSAgent(t, f, owner, "first-box")
	pendingID, pendingSecret := enrollWSAgent(t, f, owner, "pending-box")

	response := helloDevice(t, dialDeviceWS(t, f), agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: pendingID, Secret: pendingSecret,
	})
	if response.Type != "error" || response.Code != string(ipc.CodeDevicePending) {
		t.Fatalf("pending hello response = %+v", response)
	}

	rootSession := f.session(t, root)
	if call := f.call(t, "POST", "/fleet/devices/"+pendingID+"/approve", map[string]any{}, rootSession, rootSession.csrf); call.status != http.StatusOK {
		t.Fatalf("approve: HTTP %d %v", call.status, call.body)
	}
	response = helloDevice(t, dialDeviceWS(t, f), agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: pendingID, Secret: pendingSecret,
	})
	if response.Type != "hello_ok" {
		t.Fatalf("approved hello response = %+v", response)
	}
}
