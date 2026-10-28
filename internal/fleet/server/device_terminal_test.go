package fleetserver

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"github.com/Hello-CTF/NexTerm/internal/version"
)

func TestTerminalDevicesListsOnlyTheAccountsLiveDevices(t *testing.T) {
	fixture := newServiceFixture(t)
	alice := fixture.createUser(t, "agg-alice")
	bob := fixture.createUser(t, "agg-bob")
	kept := fixture.enrollAgent(t, alice, "alice-box")
	revoked := fixture.enrollAgent(t, alice, "alice-old")
	others := fixture.enrollAgent(t, bob, "bob-box")
	ctx := context.Background()
	if err := fixture.service.RevokeDevice(ctx, fixture.identity(alice), revoked.DeviceID); err != nil {
		t.Fatal(err)
	}
	audits := len(fixture.auditRows(t, auditKindDeviceTerminal))

	devices, err := fixture.service.TerminalDevices(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].ID != kept.DeviceID || devices[0].Name != "alice-box" {
		t.Fatalf("alice's devices = %+v; want only her live device", devices)
	}
	if devices[0].Digest != "" {
		t.Fatalf("offline device digest = %q; want none", devices[0].Digest)
	}
	bobs, err := fixture.service.TerminalDevices(ctx, bob.ID)
	if err != nil || len(bobs) != 1 || bobs[0].ID != others.DeviceID {
		t.Fatalf("bob's devices = %+v, %v", bobs, err)
	}
	if none, err := fixture.service.TerminalDevices(ctx, ""); err != nil || len(none) != 0 {
		t.Fatalf("anonymous devices = %+v, %v; want none", none, err)
	}
	if got := len(fixture.auditRows(t, auditKindDeviceTerminal)); got != audits {
		t.Fatalf("terminal audits %d -> %d; a read-only listing writes none", audits, got)
	}
}

func TestAuthorizeDeviceTerminal(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "dev-term-owner")
	other := fixture.createUser(t, "dev-term-other")
	result := fixture.enrollAgent(t, owner, "term-box")
	ctx := context.Background()

	if err := fixture.service.AuthorizeDeviceTerminal(ctx, owner.ID, result.DeviceID); err != nil {
		t.Fatalf("owner authorize = %v; want nil", err)
	}
	allows := fixture.auditRows(t, auditKindDeviceTerminal)
	if len(allows) != 1 || allows[0]["outcome"] != "allow" || allows[0]["action"] != "terminal_open" {
		t.Fatalf("terminal_open audits = %v; want one allow", allows)
	}

	var appErr *ipc.Error
	if err := fixture.service.AuthorizeDeviceTerminal(ctx, other.ID, result.DeviceID); !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
		t.Fatalf("non-owner authorize = %v; want forbidden", err)
	}
	if err := fixture.service.AuthorizeDeviceTerminal(ctx, "", result.DeviceID); !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
		t.Fatalf("empty user authorize = %v; want forbidden", err)
	}

	deviceAgent := query.Use(fixture.db).DeviceAgent
	if _, err := deviceAgent.WithContext(ctx).Where(deviceAgent.DeviceID.Eq(result.DeviceID)).
		UpdateColumns(map[string]any{"terminal_enabled": false}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.AuthorizeDeviceTerminal(ctx, owner.ID, result.DeviceID); !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
		t.Fatalf("terminal disabled authorize = %v; want forbidden", err)
	}
	if _, err := deviceAgent.WithContext(ctx).Where(deviceAgent.DeviceID.Eq(result.DeviceID)).
		UpdateColumns(map[string]any{"terminal_enabled": true}); err != nil {
		t.Fatal(err)
	}

	if err := fixture.service.RevokeDevice(ctx, fixture.identity(owner), result.DeviceID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.AuthorizeDeviceTerminal(ctx, owner.ID, result.DeviceID); !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
		t.Fatalf("revoked authorize = %v; want forbidden", err)
	}
	// Same semantics as the bridge endpoint: the authorization decision (owner/revocation) writes an audit every time, and the terminal switch check comes after the audit,
	// so an owner with the terminal off also produces an allow; an empty userID is rejected before authorization and writes no audit.
	audits := fixture.auditRows(t, auditKindDeviceTerminal)
	outcomes := make([]any, 0, len(audits))
	for _, row := range audits {
		outcomes = append(outcomes, row["outcome"])
	}
	if len(audits) != 4 || outcomes[0] != "allow" || outcomes[1] != "deny" || outcomes[2] != "allow" || outcomes[3] != "deny" {
		t.Fatalf("terminal_open audits = %v; want allow/deny/allow/deny", outcomes)
	}
}

func TestDeviceStateDigestFromControlHello(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "dev-digest-owner")
	deviceID, secret := enrollWSAgent(t, f, owner, "digest-box")

	if _, ok := f.service.DeviceStateDigest(deviceID); ok {
		t.Fatal("offline device must not report a state digest")
	}
	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret, StateDigest: "digest-abc",
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}
	if digest, ok := f.service.DeviceStateDigest(deviceID); !ok || digest != "digest-abc" {
		t.Fatalf("DeviceStateDigest = %q %v; want digest-abc", digest, ok)
	}
}

func TestRequestDeviceBridgeOffline(t *testing.T) {
	fixture := newServiceFixture(t)
	owner := fixture.createUser(t, "dev-bridge-owner")
	result := fixture.enrollAgent(t, owner, "bridge-box")

	if _, err := fixture.service.RequestDeviceBridge(context.Background(), result.DeviceID); err == nil {
		t.Fatal("offline device bridge must fail")
	}
}

func TestSetTerminalEnabledAuthorizationAndAudit(t *testing.T) {
	fixture := newServiceFixture(t)
	root := fixture.createSuperadmin(t, "term-root")
	alice := fixture.createUser(t, "term-alice")
	bob := fixture.createUser(t, "term-bob")
	result := fixture.enrollAgent(t, alice, "toggle-box")
	ctx := context.Background()
	var appErr *ipc.Error

	if err := fixture.service.SetTerminalEnabled(ctx, fixture.identity(bob), result.DeviceID, false); !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
		t.Fatalf("non-owner toggle = %v; want forbidden", err)
	}
	// Superadmin gets no extra right over another user's device, exactly like autostart.
	if err := fixture.service.SetTerminalEnabled(ctx, fixture.identity(root), result.DeviceID, false); !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
		t.Fatalf("superadmin toggle = %v; want forbidden", err)
	}
	if err := fixture.service.AuthorizeDeviceTerminal(ctx, alice.ID, result.DeviceID); err != nil {
		t.Fatalf("denied toggles must not change the switch: %v", err)
	}

	if err := fixture.service.SetTerminalEnabled(ctx, fixture.identity(alice), result.DeviceID, false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.AuthorizeDeviceTerminal(ctx, alice.ID, result.DeviceID); !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
		t.Fatalf("after disable authorize = %v; want forbidden", err)
	}
	devices, err := fixture.service.ListDevices(ctx, fixture.identity(alice))
	if err != nil || len(devices) != 1 || devices[0].Agent.TerminalEnabled {
		t.Fatalf("list after disable = %+v err=%v", devices, err)
	}
	if err := fixture.service.SetTerminalEnabled(ctx, fixture.identity(alice), result.DeviceID, true); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.AuthorizeDeviceTerminal(ctx, alice.ID, result.DeviceID); err != nil {
		t.Fatalf("after enable authorize = %v", err)
	}

	audits := fixture.auditRows(t, auditKindDeviceTerminalToggle)
	if len(audits) != 4 || audits[0]["outcome"] != "deny" || audits[0]["reason"] != "not_owner" || audits[0]["requester"] != bob.ID ||
		audits[1]["outcome"] != "deny" || audits[2]["outcome"] != "allow" || audits[3]["outcome"] != "allow" || audits[2]["action"] != "terminal_toggle" {
		t.Fatalf("toggle audits = %v", audits)
	}

	if err := fixture.service.RevokeDevice(ctx, fixture.identity(alice), result.DeviceID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.SetTerminalEnabled(ctx, fixture.identity(alice), result.DeviceID, false); !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
		t.Fatalf("revoked toggle = %v; want forbidden", err)
	}
	audits = fixture.auditRows(t, auditKindDeviceTerminalToggle)
	if last := audits[len(audits)-1]; last["outcome"] != "deny" || last["reason"] != "device_revoked" {
		t.Fatalf("revoked toggle audit = %v", last)
	}

	desktop, err := fixture.accounts.RegisterDevice(ctx, alice.ID, "desktop", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.SetTerminalEnabled(ctx, fixture.identity(alice), desktop.ID, false); !errors.As(err, &appErr) || appErr.Code != ipc.CodeNotFound {
		t.Fatalf("non-agent toggle = %v; want not_found", err)
	}
}

func TestDeviceTerminalAccessHTTPPushesConfigAndListsServerVersion(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "toggle-http-owner")
	other := f.createUser(t, "toggle-http-other")
	deviceID, secret := enrollWSAgent(t, f, owner, "toggle-http-box")

	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}

	otherSession := f.session(t, other)
	call := f.call(t, http.MethodPost, "/fleet/devices/"+deviceID+"/terminal-access", map[string]any{"enabled": false}, otherSession, otherSession.csrf)
	if call.status != http.StatusForbidden {
		t.Fatalf("other user status=%d body=%v", call.status, call.body)
	}

	session := f.session(t, owner)
	call = f.call(t, http.MethodPost, "/fleet/devices/"+deviceID+"/terminal-access", map[string]any{"enabled": false}, session, "")
	if call.status == http.StatusOK {
		t.Fatalf("missing CSRF must be rejected: %v", call.body)
	}
	call = f.call(t, http.MethodPost, "/fleet/devices/"+deviceID+"/terminal-access", map[string]any{}, session, session.csrf)
	if call.status != http.StatusBadRequest {
		t.Fatalf("a body without enabled must be rejected, got status=%d body=%v", call.status, call.body)
	}
	call = f.call(t, http.MethodPost, "/fleet/devices/"+deviceID+"/terminal-access", map[string]any{"enabled": false}, session, session.csrf)
	if call.status != http.StatusOK || call.body["terminal_enabled"] != false {
		t.Fatalf("owner status=%d body=%v", call.status, call.body)
	}
	message := readControlMessage(t, control, 5*time.Second)
	if message.Type != "config" || message.TerminalEnabled == nil || *message.TerminalEnabled || message.DesiredAutostart == nil || !*message.DesiredAutostart {
		t.Fatalf("pushed control message = %+v; want config with terminal disabled and autostart unchanged", message)
	}

	call = f.call(t, http.MethodGet, "/fleet/devices", nil, session, "")
	if call.status != http.StatusOK {
		t.Fatalf("list status=%d", call.status)
	}
	if got, ok := call.body["server_version"].(string); !ok || got != version.Version {
		t.Fatalf("server_version = %v; want %q", call.body["server_version"], version.Version)
	}
	agentView := call.body["devices"].([]any)[0].(map[string]any)["agent"].(map[string]any)
	if agentView["terminal_enabled"] != false {
		t.Fatalf("agent view = %v", agentView)
	}
}
