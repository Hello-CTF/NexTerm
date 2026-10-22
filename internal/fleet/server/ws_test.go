package fleetserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"github.com/Hello-CTF/NexTerm/internal/version"
	"github.com/coder/websocket"
)

func dialDeviceWS(t *testing.T, f *httpFixture) *websocket.Conn {
	t.Helper()
	conn, response, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(f.http.URL, "http")+agent.PathDeviceWS, &websocket.DialOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		t.Fatal(err)
	}
	if got := response.Header.Get("Sec-WebSocket-Extensions"); !strings.Contains(got, "permessage-deflate") {
		t.Fatalf("compression extensions = %q, want permessage-deflate", got)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

func helloDevice(t *testing.T, conn *websocket.Conn, hello agent.HelloMessage) controlMessage {
	t.Helper()
	payload, err := json.Marshal(hello)
	if err != nil {
		t.Fatal(err)
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(writeCtx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
	var response controlMessage
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	if _, raw, err := conn.Read(readCtx); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func enrollWSAgent(t *testing.T, f *httpFixture, owner *account.User, name string) (string, string) {
	t.Helper()
	return enrollAgent(t, f, owner, name)
}

func dialRelay(t *testing.T, f *httpFixture, deviceID string, cookie *http.Cookie, purpose string) *websocket.Conn {
	t.Helper()
	options := &websocket.DialOptions{}
	if cookie != nil {
		options.HTTPHeader = http.Header{"Cookie": []string{cookie.Name + "=" + cookie.Value}}
	}
	options.CompressionMode = websocket.CompressionContextTakeover
	url := "ws" + strings.TrimPrefix(f.http.URL, "http") + "/fleet/devices/" + deviceID + "/bridge"
	if purpose != "" {
		url += "?purpose=" + purpose
	}
	conn, response, err := websocket.Dial(context.Background(), url, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := response.Header.Get("Sec-WebSocket-Extensions"); !strings.Contains(got, "permessage-deflate") {
		t.Fatalf("compression extensions = %q, want permessage-deflate", got)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

func TestDeviceWSHelloAndAuth(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "ws-owner")
	deviceID, secret := enrollWSAgent(t, f, owner, "ws-box")

	t.Run("valid credentials return hello_ok", func(t *testing.T) {
		response := helloDevice(t, dialDeviceWS(t, f), agent.HelloMessage{
			Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
		})
		if response.Type != "hello_ok" {
			t.Fatalf("hello response = %+v", response)
		}
	})
	t.Run("protocol mismatch returns version_mismatch", func(t *testing.T) {
		response := helloDevice(t, dialDeviceWS(t, f), agent.HelloMessage{
			Type: "hello", Protocol: agent.ProtocolVersion + 1, DeviceID: deviceID, Secret: secret,
		})
		if response.Type != "error" || response.Code != "version_mismatch" {
			t.Fatalf("hello response = %+v", response)
		}
		if response.ServerVersion != version.Version {
			t.Fatalf("version_mismatch frame must carry the server version for agent self-upgrade, got %q", response.ServerVersion)
		}
	})
	t.Run("invalid secret returns forbidden", func(t *testing.T) {
		response := helloDevice(t, dialDeviceWS(t, f), agent.HelloMessage{
			Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: "wrong",
		})
		if response.Type != "error" || response.Code != "forbidden" {
			t.Fatalf("hello response = %+v", response)
		}
	})
	t.Run("revoked device returns forbidden", func(t *testing.T) {
		other := f.createUser(t, "ws-revoker")
		revokedID, revokedSecret := enrollWSAgent(t, f, other, "ws-revoked")
		session := f.session(t, other)
		if response := f.call(t, "POST", "/fleet/devices/"+revokedID+"/revoke", nil, session, session.csrf); response.status != http.StatusOK {
			t.Fatalf("revoke: HTTP %d %v", response.status, response.body)
		}
		response := helloDevice(t, dialDeviceWS(t, f), agent.HelloMessage{
			Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: revokedID, Secret: revokedSecret,
		})
		if response.Type != "error" || response.Code != "forbidden" {
			t.Fatalf("hello response = %+v", response)
		}
	})
}

func TestDeviceWSAuthOffRejected(t *testing.T) {
	f := newHTTPFixture(t, true)

	// Under --auth=off the whole fleet is closed: HTTP gets 403, WS cannot upgrade (no real device needed;
	// the guard rejects before auth).
	response := f.call(t, "POST", "/agent/sync", map[string]any{"device_id": "x", "secret": "y"}, nil, "")
	if response.status != http.StatusForbidden {
		t.Fatalf("sync under auth=off: HTTP %d %v", response.status, response.body)
	}
	_, raw, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(f.http.URL, "http")+agent.PathDeviceWS, nil)
	if err == nil {
		t.Fatal("auth=off allowed /ws/device upgrade")
	}
	if raw != nil && raw.StatusCode != http.StatusForbidden {
		t.Fatalf("dial status = %d, want 403", raw.StatusCode)
	}
}

func readControlMessage(t *testing.T, conn *websocket.Conn, timeout time.Duration) controlMessage {
	t.Helper()
	var message controlMessage
	readCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, raw, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("read control message: %v", err)
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func TestDeviceRevokeKicksControlChannel(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "kick-owner")
	deviceID, secret := enrollWSAgent(t, f, owner, "kick-box")

	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
	}); response.Type != "hello_ok" {
		t.Fatalf("hello response = %+v", response)
	}

	session := f.session(t, owner)
	if response := f.call(t, "POST", "/fleet/devices/"+deviceID+"/revoke", nil, session, session.csrf); response.status != http.StatusOK {
		t.Fatalf("revoke: HTTP %d %v", response.status, response.body)
	}
	message := readControlMessage(t, control, 5*time.Second)
	if message.Type != "revoke" {
		t.Fatalf("control message = %+v, want revoke", message)
	}
	// After the revoke frame the connection is closed; the agent stops per the contract and never reconnects.
	readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := control.Read(readCtx); err == nil {
		t.Fatal("control channel still open after revoke")
	}
}

func TestDeviceControlChannelReplacement(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "replace-owner")
	deviceID, secret := enrollWSAgent(t, f, owner, "replace-box")

	first := dialDeviceWS(t, f)
	if response := helloDevice(t, first, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
	}); response.Type != "hello_ok" {
		t.Fatalf("first hello = %+v", response)
	}
	second := dialDeviceWS(t, f)
	if response := helloDevice(t, second, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
	}); response.Type != "hello_ok" {
		t.Fatalf("second hello = %+v", response)
	}
	readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := first.Read(readCtx); err == nil {
		t.Fatal("first control channel still open after replacement")
	}
}

func TestDeviceBridgePairing(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "bridge-owner")
	deviceID, secret := enrollWSAgent(t, f, owner, "bridge-box")

	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
		Capabilities: []string{agent.PurposeTerminal},
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}

	type bridgeOutcome struct {
		conn *wsConn
		err  error
	}
	paired := make(chan bridgeOutcome, 1)
	go func() {
		conn, err := f.service.registry.RequestBridge(context.Background(), deviceID, agent.PurposeTerminal)
		if err != nil {
			paired <- bridgeOutcome{err: err}
			return
		}
		paired <- bridgeOutcome{conn: conn.(*wsConn)}
	}()

	request := readControlMessage(t, control, 5*time.Second)
	if request.Type != "bridge" || request.BridgeID == "" || request.Purpose != "terminal" {
		t.Fatalf("control message = %+v, want terminal bridge request", request)
	}

	bridgeWS := dialDeviceWS(t, f)
	response := helloDevice(t, bridgeWS, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret, BridgeID: request.BridgeID,
	})
	if response.Type != "hello_ok" {
		t.Fatalf("bridge hello = %+v", response)
	}

	var serverSide *wsConn
	select {
	case outcome := <-paired:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		serverSide = outcome.conn
	case <-time.After(5 * time.Second):
		t.Fatal("RequestBridge did not pair")
	}

	// Byte transparency: each side of the bridge writes one binary message, and the peer receives it as-is.
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := bridgeWS.Write(writeCtx, websocket.MessageBinary, []byte("from-agent")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64)
	count, err := serverSide.Read(buffer)
	if err != nil || string(buffer[:count]) != "from-agent" {
		t.Fatalf("server read = %q %v", buffer[:count], err)
	}
	if _, err := serverSide.Write([]byte("from-server")); err != nil {
		t.Fatal(err)
	}
	_, raw, err := bridgeWS.Read(writeCtx)
	if err != nil || string(raw) != "from-server" {
		t.Fatalf("agent read = %q %v", raw, err)
	}
}

func TestDeviceBridgeUnknownIDRejected(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "bridge-unknown")
	deviceID, secret := enrollWSAgent(t, f, owner, "unknown-box")

	bridgeWS := dialDeviceWS(t, f)
	response := helloDevice(t, bridgeWS, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret, BridgeID: "01J5MISSING000000000000000",
	})
	if response.Type != "error" {
		t.Fatalf("bridge hello = %+v, want error", response)
	}
}

func TestDeviceBridgeRelayAuthorization(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "relay-owner")
	other := f.createUser(t, "relay-other")
	deviceID, secret := enrollWSAgent(t, f, owner, "relay-box")

	// No session: HTTP 401, WS cannot upgrade.
	if response := f.call(t, "GET", "/fleet/devices/"+deviceID+"/bridge", nil, nil, ""); response.status != http.StatusUnauthorized {
		t.Fatalf("anonymous relay: HTTP %d %v", response.status, response.body)
	}
	// Non-owner: 403 and an audit is written.
	otherSession := f.session(t, other)
	if response := f.call(t, "GET", "/fleet/devices/"+deviceID+"/bridge", nil, otherSession, ""); response.status != http.StatusForbidden {
		t.Fatalf("non-owner relay: HTTP %d %v", response.status, response.body)
	}
	// Owner session + online control channel: the upgrade succeeds and bridge pairing completes.
	ownerSession := f.session(t, owner)
	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
		Capabilities: []string{agent.PurposeTerminal},
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}

	relayConn := dialRelay(t, f, deviceID, ownerSession.cookie, "")
	request := readControlMessage(t, control, 5*time.Second)
	if request.Type != "bridge" || request.BridgeID == "" {
		t.Fatalf("control message = %+v, want bridge request", request)
	}
	bridgeWS := dialDeviceWS(t, f)
	if response := helloDevice(t, bridgeWS, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret, BridgeID: request.BridgeID,
	}); response.Type != "hello_ok" {
		t.Fatalf("bridge hello = %+v", response)
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := relayConn.Write(writeCtx, websocket.MessageBinary, []byte("ping-through")); err != nil {
		t.Fatal(err)
	}
	_, raw, err := bridgeWS.Read(writeCtx)
	if err != nil || string(raw) != "ping-through" {
		t.Fatalf("agent read = %q %v", raw, err)
	}
}

func TestDeviceBridgeRelayRequiresTerminalEnabled(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "terminal-off")
	deviceID, _ := enrollWSAgent(t, f, owner, "terminal-off-box")
	session := f.session(t, owner)

	// Turn off the terminal authorization bit directly (the product currently has no HTTP entry to change terminal_enabled; this verifies the guard itself).
	deviceAgent := query.Use(f.service.db).DeviceAgent
	if _, err := deviceAgent.WithContext(context.Background()).Where(deviceAgent.DeviceID.Eq(deviceID)).
		UpdateColumns(map[string]any{"terminal_enabled": false}); err != nil {
		t.Fatal(err)
	}
	response := f.call(t, "GET", "/fleet/devices/"+deviceID+"/bridge", nil, session, "")
	if response.status != http.StatusForbidden {
		t.Fatalf("relay with terminal disabled: HTTP %d %v", response.status, response.body)
	}
}

// presenceCapture captures device://status pushes for online/offline assertions.
type presenceCapture struct {
	mu     sync.Mutex
	events []ipc.DeviceStatusEvent
}

func (c *presenceCapture) Emit(_ context.Context, event ipc.Event) error {
	if event.Event != ipc.TopicDeviceStatus {
		return nil
	}
	payload, ok := event.Payload.(ipc.DeviceStatusEvent)
	if !ok {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, payload)
	return nil
}

func (c *presenceCapture) snapshot() []ipc.DeviceStatusEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ipc.DeviceStatusEvent(nil), c.events...)
}

func (c *presenceCapture) waitFor(t *testing.T, count int) []ipc.DeviceStatusEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if events := c.snapshot(); len(events) >= count {
			return events
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d device status events, current %+v", count, c.snapshot())
	return nil
}

// requireNoPresenceEvent checks for unexpected transitions after the caller has observed the lifecycle operation complete.
func (c *presenceCapture) requireNoPresenceEvent(t *testing.T, want int) {
	t.Helper()
	if events := c.snapshot(); len(events) != want {
		t.Fatalf("device status event count = %d, want %d: %+v", len(events), want, events)
	}
}

func TestDevicePresenceLifecycle(t *testing.T) {
	capture := &presenceCapture{}
	f := newHTTPFixture(t, false, WithEvents(capture))
	owner := f.createUser(t, "presence-owner")
	deviceID, secret := enrollWSAgent(t, f, owner, "presence-box")
	hello := agent.HelloMessage{Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret}

	control := dialDeviceWS(t, f)
	helloDevice(t, control, hello)
	events := capture.waitFor(t, 1)
	if events[0].DeviceID != deviceID || !events[0].Online {
		t.Fatalf("first event = %+v, want device online", events[0])
	}
	if ids := auditAssetIDs(t, f.service.db, auditKindDeviceOnline); len(ids) != 1 || ids[0] != deviceID {
		t.Fatalf("online audit asset_ids=%v", ids)
	}

	// Replacement reconnect: the old connection is torn down by the server, but the device stays online the whole time and produces no new events.
	replacement := dialDeviceWS(t, f)
	helloDevice(t, replacement, hello)
	readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := control.Read(readCtx); err == nil {
		t.Fatal("replaced control channel is still open")
	}
	capture.requireNoPresenceEvent(t, 1)

	// The replaced connection drops: exactly one offline event and one offline audit.
	_ = replacement.Close(websocket.StatusNormalClosure, "")
	events = capture.waitFor(t, 2)
	if events[1].DeviceID != deviceID || events[1].Online {
		t.Fatalf("second event = %+v, want device offline", events[1])
	}
	if ids := auditAssetIDs(t, f.service.db, auditKindDeviceOffline); len(ids) != 1 || ids[0] != deviceID {
		t.Fatalf("offline audit asset_ids=%v", ids)
	}

	// Reconnecting is a real transition: online again.
	reconnected := dialDeviceWS(t, f)
	helloDevice(t, reconnected, hello)
	events = capture.waitFor(t, 3)
	if events[2].DeviceID != deviceID || !events[2].Online {
		t.Fatalf("third event = %+v, want device back online", events[2])
	}

	// Revocation kick: KickDevice already removed the table entry, so the unregistration callback never fires; the revocation path makes up the offline record.
	session := f.session(t, owner)
	if response := f.call(t, "POST", "/fleet/devices/"+deviceID+"/revoke", nil, session, session.csrf); response.status != http.StatusOK {
		t.Fatalf("revoke: HTTP %d %v", response.status, response.body)
	}
	events = capture.waitFor(t, 4)
	if events[3].DeviceID != deviceID || events[3].Online {
		t.Fatalf("fourth event = %+v, want device offline after revocation", events[3])
	}
	if ids := auditAssetIDs(t, f.service.db, auditKindDeviceOffline); len(ids) != 2 || ids[1] != deviceID {
		t.Fatalf("offline audit asset_ids=%v", ids)
	}
	if ids := auditAssetIDs(t, f.service.db, auditKindDeviceRevoke); len(ids) != 1 || ids[0] != deviceID {
		t.Fatalf("revoke audit asset_ids=%v", ids)
	}
}

// Service shutdown produces no offline flurry: after Registry.Close swaps in the empty table, unregistration can no longer get the current connection, so all stays silent.
func TestDevicePresenceShutdownSilent(t *testing.T) {
	capture := &presenceCapture{}
	f := newHTTPFixture(t, false, WithEvents(capture))
	owner := f.createUser(t, "shutdown-owner")
	deviceID, secret := enrollWSAgent(t, f, owner, "shutdown-box")

	control := dialDeviceWS(t, f)
	helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
	})
	capture.waitFor(t, 1)

	if err := f.service.Close(); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := control.Read(readCtx); err == nil {
		t.Fatal("control channel remains open after service shutdown")
	}
	capture.requireNoPresenceEvent(t, 1)
	if ids := auditAssetIDs(t, f.service.db, auditKindDeviceOffline); len(ids) != 0 {
		t.Fatalf("shutdown wrote offline audits: %v", ids)
	}
}

// waitForAppVersion polls the stored agent version: the server acks hello_ok before persisting the hello version, so
// the refreshed row is never visible synchronously after the ack.
func waitForAppVersion(t *testing.T, f *httpFixture, deviceID, want string) {
	t.Helper()
	deviceAgent := query.Use(f.service.db).DeviceAgent
	deadline := time.Now().Add(5 * time.Second)
	var got string
	var lastErr error
	for time.Now().Before(deadline) {
		row, err := deviceAgent.WithContext(context.Background()).Where(deviceAgent.DeviceID.Eq(deviceID)).First()
		if err == nil {
			if row.AppVersion == want {
				return
			}
			got = row.AppVersion
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for app_version to become %q, current %q err=%v", want, got, lastErr)
}

func TestDeviceWSHelloRefreshesAppVersion(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "ws-version-owner")
	deviceID, secret := enrollWSAgent(t, f, owner, "ws-version-box")

	response := helloDevice(t, dialDeviceWS(t, f), agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret, AppVersion: "9.9.9",
	})
	if response.Type != "hello_ok" {
		t.Fatalf("hello response = %+v", response)
	}
	waitForAppVersion(t, f, deviceID, "9.9.9")

	// The first refresh is confirmed persisted above; an empty version is rejected before any database write.
	response = helloDevice(t, dialDeviceWS(t, f), agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
	})
	if response.Type != "hello_ok" {
		t.Fatalf("hello response = %+v", response)
	}
	deviceAgent := query.Use(f.service.db).DeviceAgent
	row, err := deviceAgent.WithContext(context.Background()).Where(deviceAgent.DeviceID.Eq(deviceID)).First()
	if err != nil || row.AppVersion != "9.9.9" {
		t.Fatalf("agent app_version = %q err=%v, want unchanged 9.9.9", row.AppVersion, err)
	}
}

// Old-peer compatibility: a hello carrying fields unknown to this build parses without error, and the new
// purpose/capabilities fields round-trip; peers ignoring unknown fields see the same contract as before.
func TestDeviceHelloIgnoresUnknownFields(t *testing.T) {
	var hello agent.HelloMessage
	payload := `{"type":"hello","protocol":1,"device_id":"d","secret":"s","purpose":"file","capabilities":["terminal","file"],"future_field":{"x":1}}`
	if err := json.Unmarshal([]byte(payload), &hello); err != nil {
		t.Fatal(err)
	}
	if hello.Purpose != "file" || len(hello.Capabilities) != 2 || hello.Capabilities[1] != "file" {
		t.Fatalf("hello = %+v", hello)
	}
}

func TestDeviceBridgeRelayPurposePreCheck(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "relay-purpose")
	deviceID, secret := enrollWSAgent(t, f, owner, "relay-purpose-box")
	session := f.session(t, owner)

	// Offline device: any purpose gets 503 before any upgrade.
	if response := f.call(t, "GET", "/fleet/devices/"+deviceID+"/bridge?purpose=file", nil, session, ""); response.status != http.StatusServiceUnavailable {
		t.Fatalf("offline relay: HTTP %d %v", response.status, response.body)
	}

	// Online agent without capabilities (an old agent): file gets 403 with the purpose error, bogus gets 400.
	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}
	defer func() { _ = control.Close(websocket.StatusNormalClosure, "") }()
	if response := f.call(t, "GET", "/fleet/devices/"+deviceID+"/bridge?purpose=file", nil, session, ""); response.status != http.StatusForbidden {
		t.Fatalf("unadvertised file relay: HTTP %d %v", response.status, response.body)
	}
	for _, purpose := range []string{"listen", "accept"} {
		if response := f.call(t, "GET", "/fleet/devices/"+deviceID+"/bridge?purpose="+purpose, nil, session, ""); response.status != http.StatusForbidden {
			t.Fatalf("unadvertised %s relay: HTTP %d %v", purpose, response.status, response.body)
		}
	}
	if response := f.call(t, "GET", "/fleet/devices/"+deviceID+"/bridge?purpose=bogus", nil, session, ""); response.status != http.StatusBadRequest {
		t.Fatalf("unknown purpose relay: HTTP %d %v", response.status, response.body)
	}
}

// Remote-forward bridges: an agent advertising listen/accept serves both purposes through the relay - the control
// request echoes the purpose, pairing requires the same echo, and both audit under the forward_open family.
func TestDeviceBridgeRelayListenAcceptPurposes(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "relay-listen")
	deviceID, secret := enrollWSAgent(t, f, owner, "relay-listen-box")
	session := f.session(t, owner)

	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
		Capabilities: []string{agent.PurposeTerminal, "file", "forward", "listen", "accept"},
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}
	defer func() { _ = control.Close(websocket.StatusNormalClosure, "") }()

	for _, purpose := range []string{"listen", "accept"} {
		relayConn := dialRelay(t, f, deviceID, session.cookie, purpose)
		request := readControlMessage(t, control, 5*time.Second)
		if request.Type != "bridge" || request.BridgeID == "" || request.Purpose != purpose {
			t.Fatalf("%s control message = %+v, want %s bridge request", purpose, request, purpose)
		}
		bridgeWS := dialDeviceWS(t, f)
		if response := helloDevice(t, bridgeWS, agent.HelloMessage{
			Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
			BridgeID: request.BridgeID, Purpose: purpose,
		}); response.Type != "hello_ok" {
			t.Fatalf("%s bridge hello = %+v", purpose, response)
		}
		writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := relayConn.Write(writeCtx, websocket.MessageBinary, []byte("ping-"+purpose)); err != nil {
			cancel()
			t.Fatal(err)
		}
		_, raw, err := bridgeWS.Read(writeCtx)
		cancel()
		if err != nil || string(raw) != "ping-"+purpose {
			t.Fatalf("%s agent read = %q %v", purpose, raw, err)
		}
	}

	audits := auditPayloadRows(t, f.service.db, auditSourceFleet, auditKindDeviceTerminal)
	if len(audits) != 2 || audits[0]["action"] != "forward_open" || audits[1]["action"] != "forward_open" {
		t.Fatalf("listen/accept audits = %v; want two forward_open", audits)
	}
}
