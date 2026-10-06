package fleetserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/account"
	"github.com/ProbiusOfficial/NexTerm/internal/fleet/agent"
	"github.com/coder/websocket"
)

func dialDeviceWS(t *testing.T, f *httpFixture) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(f.http.URL, "http")+agent.PathDeviceWS, nil)
	if err != nil {
		t.Fatal(err)
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

func dialRelay(t *testing.T, f *httpFixture, deviceID string, cookie *http.Cookie) *websocket.Conn {
	t.Helper()
	options := &websocket.DialOptions{}
	if cookie != nil {
		options.HTTPHeader = http.Header{"Cookie": []string{cookie.Name + "=" + cookie.Value}}
	}
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(f.http.URL, "http")+"/fleet/devices/"+deviceID+"/bridge", options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

func TestDeviceWSHelloAndAuth(t *testing.T) {
	f := newHTTPFixture(t, false)
	owner := f.createUser(t, "ws-owner")
	deviceID, secret := enrollWSAgent(t, f, owner, "ws-box")

	t.Run("凭证有效得到 hello_ok", func(t *testing.T) {
		response := helloDevice(t, dialDeviceWS(t, f), agent.HelloMessage{
			Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
		})
		if response.Type != "hello_ok" {
			t.Fatalf("hello response = %+v", response)
		}
	})
	t.Run("协议版本不匹配得到 version_mismatch", func(t *testing.T) {
		response := helloDevice(t, dialDeviceWS(t, f), agent.HelloMessage{
			Type: "hello", Protocol: agent.ProtocolVersion + 1, DeviceID: deviceID, Secret: secret,
		})
		if response.Type != "error" || response.Code != "version_mismatch" {
			t.Fatalf("hello response = %+v", response)
		}
	})
	t.Run("密钥错误得到 forbidden", func(t *testing.T) {
		response := helloDevice(t, dialDeviceWS(t, f), agent.HelloMessage{
			Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: "wrong",
		})
		if response.Type != "error" || response.Code != "forbidden" {
			t.Fatalf("hello response = %+v", response)
		}
	})
	t.Run("已吊销设备得到 forbidden", func(t *testing.T) {
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

	// --auth=off 下整个 fleet 关闭: HTTP 层 403, WS 无法升级 (无需真实设备,
	// guard 在鉴权之前拒绝)。
	response := f.call(t, "POST", "/agent/sync", map[string]any{"device_id": "x", "secret": "y"}, nil, "")
	if response.status != http.StatusForbidden {
		t.Fatalf("sync under auth=off: HTTP %d %v", response.status, response.body)
	}
	_, raw, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(f.http.URL, "http")+agent.PathDeviceWS, nil)
	if err == nil {
		t.Fatal("auth=off 下 /ws/device 仍允许升级")
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
	// 吊销帧之后连接被关闭, agent 按合同停止且不再重连。
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
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}

	type bridgeOutcome struct {
		conn *wsConn
		err  error
	}
	paired := make(chan bridgeOutcome, 1)
	go func() {
		conn, err := f.service.registry.RequestBridge(context.Background(), deviceID)
		if err != nil {
			paired <- bridgeOutcome{err: err}
			return
		}
		paired <- bridgeOutcome{conn: conn.(*wsConn)}
	}()

	request := readControlMessage(t, control, 5*time.Second)
	if request.Type != "bridge" || request.BridgeID == "" {
		t.Fatalf("control message = %+v, want bridge request", request)
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

	// 字节透明: 桥接两侧各写一个二进制消息, 对端原样收到。
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

	// 无会话: HTTP 401, WS 无法升级。
	if response := f.call(t, "GET", "/fleet/devices/"+deviceID+"/bridge", nil, nil, ""); response.status != http.StatusUnauthorized {
		t.Fatalf("anonymous relay: HTTP %d %v", response.status, response.body)
	}
	// 非 owner 非 superadmin: 403 且写审计。
	otherSession := f.session(t, other)
	if response := f.call(t, "GET", "/fleet/devices/"+deviceID+"/bridge", nil, otherSession, ""); response.status != http.StatusForbidden {
		t.Fatalf("non-owner relay: HTTP %d %v", response.status, response.body)
	}
	// owner 会话 + 在线控制通道: 升级成功并完成桥接配对。
	ownerSession := f.session(t, owner)
	control := dialDeviceWS(t, f)
	if response := helloDevice(t, control, agent.HelloMessage{
		Type: "hello", Protocol: agent.ProtocolVersion, DeviceID: deviceID, Secret: secret,
	}); response.Type != "hello_ok" {
		t.Fatalf("hello = %+v", response)
	}

	relayConn := dialRelay(t, f, deviceID, ownerSession.cookie)
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

	// 直接关闭终端授权位 (产品当前无 HTTP 入口改 terminal_enabled, 这里验证守卫本身)。
	if _, err := f.service.db.ExecContext(context.Background(), "UPDATE device_agent SET terminal_enabled = 0 WHERE device_id = ?", deviceID); err != nil {
		t.Fatal(err)
	}
	response := f.call(t, "GET", "/fleet/devices/"+deviceID+"/bridge", nil, session, "")
	if response.status != http.StatusForbidden {
		t.Fatalf("relay with terminal disabled: HTTP %d %v", response.status, response.body)
	}
}
