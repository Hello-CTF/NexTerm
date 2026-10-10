package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type wsFake struct {
	server *httptest.Server

	onHello func(conn *websocket.Conn, hello HelloMessage) error
}

func newWSFake(t *testing.T, onHello func(*websocket.Conn, HelloMessage) error) *wsFake {
	t.Helper()
	fake := &wsFake{onHello: onHello}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PathDeviceWS {
			t.Errorf("ws path = %s", r.URL.Path)
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		helloCtx, helloCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer helloCancel()
		_, payload, err := conn.Read(helloCtx)
		if err != nil {
			t.Error(err)
			return
		}
		var hello HelloMessage
		if err := json.Unmarshal(payload, &hello); err != nil {
			t.Error(err)
			return
		}
		if hello.Type != "hello" || hello.Protocol != ProtocolVersion || hello.DeviceID == "" || hello.Secret == "" {
			t.Errorf("hello = %+v", hello)
		}
		if err := fake.onHello(conn, hello); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func wsURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http") + PathDeviceWS
}

func writeServerMessage(conn *websocket.Conn, message serverMessage) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, payload)
}

func testHello() HelloMessage {
	return HelloMessage{
		Type: "hello", Protocol: ProtocolVersion,
		DeviceID: "01J5DEVICE0000000000000000", Secret: "device-secret",
		Platform: "linux", AppVersion: "dev",
	}
}

func TestChannelHandshakeConfigAndRevoke(t *testing.T) {
	configReceived := make(chan ConfigUpdate, 1)
	revoked := make(chan struct{}, 1)
	fake := newWSFake(t, func(conn *websocket.Conn, hello HelloMessage) error {
		if err := writeServerMessage(conn, serverMessage{Type: "hello_ok"}); err != nil {
			return err
		}
		if err := writeServerMessage(conn, serverMessage{
			Type: "config", DesiredAutostart: boolPointer(false), MetricsIntervalMS: int64Pointer(30000), TerminalEnabled: boolPointer(true),
		}); err != nil {
			return err
		}
		return writeServerMessage(conn, serverMessage{Type: "revoke"})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	channel, err := DialChannel(ctx, wsURL(fake.server.URL), testHello(), false, time.Hour, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	err = channel.Run(ctx, channelHandlers{
		onConfig: func(update ConfigUpdate) { configReceived <- update },
		onRevoke: func() { revoked <- struct{}{} },
	})
	if !errors.Is(err, ErrRevoked) {
		t.Fatalf("Run = %v, want ErrRevoked", err)
	}
	update := <-configReceived
	if update.DesiredAutostart || update.MetricsIntervalMS != 30000 || !update.TerminalEnabled {
		t.Fatalf("config update = %+v", update)
	}
	select {
	case <-revoked:
	default:
		t.Fatal("revoke handler not called")
	}
}

func TestChannelDevicePendingIsNotRevocation(t *testing.T) {
	fake := newWSFake(t, func(conn *websocket.Conn, hello HelloMessage) error {
		return writeServerMessage(conn, serverMessage{Type: "error", Code: CodeDevicePending, Message: "设备待审批"})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := DialChannel(ctx, wsURL(fake.server.URL), testHello(), false, time.Hour, 5*time.Second)
	if !isDevicePending(err) {
		t.Fatalf("DialChannel = %v, want device_pending ServerError", err)
	}
	if isForbidden(err) {
		t.Fatalf("device_pending must not be treated as revocation: %v", err)
	}
}

func TestChannelVersionMismatchIsFatal(t *testing.T) {
	fake := newWSFake(t, func(conn *websocket.Conn, hello HelloMessage) error {
		return writeServerMessage(conn, serverMessage{Type: "error", Code: "version_mismatch", Message: "protocol 2 required"})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := DialChannel(ctx, wsURL(fake.server.URL), testHello(), false, time.Hour, 5*time.Second)
	var serverError *ServerError
	if !errors.As(err, &serverError) || serverError.Code != "version_mismatch" {
		t.Fatalf("DialChannel = %v, want version_mismatch ServerError", err)
	}
}

func TestChannelMidRunVersionMismatch(t *testing.T) {
	fake := newWSFake(t, func(conn *websocket.Conn, hello HelloMessage) error {
		if err := writeServerMessage(conn, serverMessage{Type: "hello_ok"}); err != nil {
			return err
		}
		return writeServerMessage(conn, serverMessage{Type: "error", Code: "version_mismatch", Message: "please upgrade"})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	channel, err := DialChannel(ctx, wsURL(fake.server.URL), testHello(), false, time.Hour, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	err = channel.Run(ctx, channelHandlers{})
	if !errors.Is(err, ErrProtocolMismatch) {
		t.Fatalf("Run = %v, want ErrProtocolMismatch", err)
	}
}

func TestDialBridgeCarriesBinaryFrames(t *testing.T) {
	echoed := make(chan []byte, 1)
	fake := newWSFake(t, func(conn *websocket.Conn, hello HelloMessage) error {
		if hello.BridgeID != "bridge-1" {
			t.Errorf("bridge hello = %+v", hello)
		}
		if err := writeServerMessage(conn, serverMessage{Type: "hello_ok"}); err != nil {
			return err
		}
		_, payload, err := conn.Read(context.Background())
		if err != nil {
			t.Error(err)
			return nil
		}
		echoed <- payload
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bridgeHello := testHello()
	bridgeHello.BridgeID = "bridge-1"
	conn, err := DialBridge(ctx, wsURL(fake.server.URL), bridgeHello, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01, 0x02, 0x03}); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-echoed:
		if string(payload) != string([]byte{0x01, 0x02, 0x03}) {
			t.Fatalf("echoed payload = %v", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge payload not delivered")
	}
}

func boolPointer(value bool) *bool    { return &value }
func int64Pointer(value int64) *int64 { return &value }
