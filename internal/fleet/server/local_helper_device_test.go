package fleetserver

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/coder/websocket"
)

// enrollLocalHelper enrolls a desktop exposing its local helper through the public enroll route.
func enrollLocalHelper(t *testing.T, f *httpFixture, owner *account.User, name string) (deviceID, secret string, body map[string]any) {
	t.Helper()
	session := f.session(t, owner)
	issue := f.call(t, "POST", "/device/enroll-codes", map[string]any{"ttl_ms": 600000}, session, session.csrf)
	if issue.status != http.StatusOK {
		t.Fatalf("issue enroll code: HTTP %d %v", issue.status, issue.body)
	}
	code, _ := issue.body["code"].(string)
	enroll := f.call(t, "POST", "/device/enroll", map[string]any{
		"code": code, "name": name, "platform": "linux", "app_version": "e2e", "kind": agent.DeviceKindLocalHelper,
	}, nil, "")
	if enroll.status != http.StatusOK {
		t.Fatalf("enroll: HTTP %d %v", enroll.status, enroll.body)
	}
	deviceID, _ = enroll.body["device_id"].(string)
	secret, _ = enroll.body["secret"].(string)
	if deviceID == "" || secret == "" {
		t.Fatalf("enroll: missing credentials %v", enroll.body)
	}
	return deviceID, secret, enroll.body
}

func TestLocalHelperEnrollKind(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "desk-owner")
	deviceID, _, body := enrollLocalHelper(t, f, owner, "desk")
	if body["kind"] != agent.DeviceKindLocalHelper {
		t.Fatalf("enroll kind = %v, want the requested kind echoed", body["kind"])
	}
	if body["desired_autostart"] != false || body["terminal_enabled"] != true {
		t.Fatalf("enroll config = %v, want no autostart and terminal access on", body)
	}

	devices, err := f.service.ListDevices(context.Background(), &account.Identity{UserID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].ID != deviceID || devices[0].Kind != store.DeviceKindLocalHelper {
		t.Fatalf("devices = %+v, want one local helper device", devices)
	}

	session := f.session(t, owner)
	issue := f.call(t, "POST", "/device/enroll-codes", map[string]any{"ttl_ms": 600000}, session, session.csrf)
	code, _ := issue.body["code"].(string)
	unknown := f.call(t, "POST", "/device/enroll", map[string]any{"code": code, "name": "odd", "kind": "router"}, nil, "")
	if unknown.status != http.StatusBadRequest {
		t.Fatalf("unknown kind: HTTP %d %v, want 400", unknown.status, unknown.body)
	}
}

func TestLocalHelperDeviceOwnerOnly(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "desk-only-owner")
	other := f.createUser(t, "desk-only-other")
	deviceID, secret, _ := enrollLocalHelper(t, f, owner, "desk")
	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
		Capabilities: []string{agent.PurposeTerminal},
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}

	otherSession := f.session(t, other)
	if response := f.call(t, "GET", "/fleet/devices/"+deviceID+"/bridge", nil, otherSession, ""); response.status != http.StatusForbidden {
		t.Fatalf("another account's relay: HTTP %d %v, want 403", response.status, response.body)
	}
	if err := f.service.AuthorizeDeviceTerminal(context.Background(), other.ID, deviceID); err == nil {
		t.Fatal("another account was authorized for a device session")
	}
	if err := f.service.AuthorizeDeviceTerminal(context.Background(), owner.ID, deviceID); err != nil {
		t.Fatalf("owner device session: %v", err)
	}

	ownerSession := f.session(t, owner)
	share := f.call(t, "POST", "/share/host-shares", map[string]any{
		"device_id": deviceID, "recipient_username": other.Username, "ttl_ms": 600000,
	}, ownerSession, ownerSession.csrf)
	if share.status != http.StatusForbidden {
		t.Fatalf("share a local helper device: HTTP %d %v, want 403", share.status, share.body)
	}
	// The same share of an ordinary, online host agent is allowed.
	hostID, hostSecret := enrollAgent(t, f, owner, "host")
	hostControl := dialDeviceWS(t, f)
	if response := helloDevice(t, hostControl, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: hostID, Secret: hostSecret,
		Capabilities: []string{agent.PurposeTerminal},
	}); response.Type != "hello_ok" {
		t.Fatalf("host hello = %+v", response)
	}
	if hostShare := f.call(t, "POST", "/share/host-shares", map[string]any{
		"device_id": hostID, "recipient_username": other.Username, "ttl_ms": 600000,
	}, ownerSession, ownerSession.csrf); hostShare.status != http.StatusOK {
		t.Fatalf("share a host agent: HTTP %d %v", hostShare.status, hostShare.body)
	}
	if response := f.call(t, "GET", "/share/devices/"+deviceID+"/terminal", nil, otherSession, ""); response.status != http.StatusForbidden {
		t.Fatalf("share terminal of a local helper device: HTTP %d %v, want 403", response.status, response.body)
	}

	autostart := f.call(t, "POST", "/fleet/devices/"+deviceID+"/autostart", map[string]any{"desired": true}, ownerSession, ownerSession.csrf)
	if autostart.status == http.StatusOK {
		t.Fatalf("autostart on a local helper device: HTTP %d %v, want a refusal", autostart.status, autostart.body)
	}
}

// Revoking the device closes a terminal bridge that is already relayed to the owner's browser.
func TestLocalHelperRevokeClosesOpenBridge(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "desk-revoke")
	deviceID, secret, _ := enrollLocalHelper(t, f, owner, "desk")
	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
		Capabilities: []string{agent.PurposeTerminal},
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}
	session := f.session(t, owner)
	relay := dialRelay(t, f, deviceID, session.cookie, "")
	request := readControlMessage(t, control, 5*time.Second)
	if request.Type != "bridge" {
		t.Fatalf("control message = %+v, want bridge request", request)
	}
	bridge := dialDeviceWS(t, f)
	if response := helloDevice(t, bridge, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret, BridgeID: request.BridgeID,
	}); response.Type != "hello_ok" {
		t.Fatalf("bridge hello = %+v", response)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := relay.Write(ctx, websocket.MessageBinary, []byte("before-revoke")); err != nil {
		t.Fatal(err)
	}
	if _, raw, err := bridge.Read(ctx); err != nil || string(raw) != "before-revoke" {
		t.Fatalf("bridge read = %q %v", raw, err)
	}

	if response := f.call(t, "POST", "/fleet/devices/"+deviceID+"/revoke", nil, session, session.csrf); response.status != http.StatusOK {
		t.Fatalf("revoke: HTTP %d %v", response.status, response.body)
	}
	if _, _, err := bridge.Read(ctx); err == nil {
		t.Fatal("device side of the bridge still open after revoke")
	}
	if _, _, err := relay.Read(ctx); err == nil {
		t.Fatal("browser side of the bridge still open after revoke")
	}
	if response := f.call(t, "GET", "/fleet/devices/"+deviceID+"/bridge", nil, session, ""); response.status != http.StatusForbidden {
		t.Fatalf("relay after revoke: HTTP %d %v, want 403", response.status, response.body)
	}
}
