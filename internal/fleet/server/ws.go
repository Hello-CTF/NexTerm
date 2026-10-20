package fleetserver

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
	"github.com/Hello-CTF/NexTerm/internal/version"
	"github.com/coder/websocket"
)

const (
	maxHelloBytes  = 64 << 10
	helloReadLimit = 10 * time.Second
)

// controlMessage is the server -> agent control channel frame; its field semantics correspond one-to-one with
// internal/fleet/agent's serverMessage (that type is unexported, mirrored here).
// ServerVersion appears only in version_mismatch frames: the agent uses it as the target version to self-upgrade from the fixed official
// release source; old agents ignore the unknown field when parsing.
type controlMessage struct {
	Type              string `json:"type"`
	Code              string `json:"code,omitempty"`
	Message           string `json:"message,omitempty"`
	ServerVersion     string `json:"server_version,omitempty"`
	DesiredAutostart  *bool  `json:"desired_autostart,omitempty"`
	MetricsIntervalMS *int64 `json:"metrics_interval_ms,omitempty"`
	TerminalEnabled   *bool  `json:"terminal_enabled,omitempty"`
	BridgeID          string `json:"bridge_id,omitempty"`
	// Purpose selects the bridge service; empty means terminal. Non-terminal purposes are requested only
	// from agents that advertised them in hello capabilities, so old agents never see them.
	Purpose string `json:"purpose,omitempty"`
}

func writeControlJSON(ctx context.Context, conn *websocket.Conn, message controlMessage) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}

// serveDeviceWS handles GET /ws/device: the shared entry for the agent's outbound control channel and bridges.
// The first frame must be hello; a protocol mismatch gets version_mismatch, bad credentials get forbidden,
// and both error frames make the agent stop per the contract (exit on protocol error / treat as revoked).
func (s *Service) serveDeviceWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		return
	}
	// deviceproto frames can reach 256KiB+overhead on the wire; the default 32KiB read limit would tear the bridge.
	conn.SetReadLimit(1 << 20)
	hello, err := readDeviceHello(conn)
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "bad hello")
		return
	}
	if hello.Protocol != agent.ProtocolVersion {
		_ = writeControlJSON(r.Context(), conn, controlMessage{
			Type: "error", Code: "version_mismatch",
			Message:       "控制通道协议版本不兼容",
			ServerVersion: version.Version,
		})
		_ = conn.Close(websocket.StatusProtocolError, "version mismatch")
		return
	}
	if _, err := s.AuthenticateAgent(r.Context(), hello.DeviceID, hello.Secret); err != nil {
		_ = writeControlJSON(r.Context(), conn, controlMessage{
			Type: "error", Code: "forbidden", Message: "设备凭证无效",
		})
		_ = conn.Close(websocket.StatusPolicyViolation, "forbidden")
		return
	}
	if hello.BridgeID != "" {
		s.serveDeviceBridge(conn, hello)
		return
	}
	s.serveDeviceControl(conn, hello)
}

func readDeviceHello(conn *websocket.Conn) (agent.HelloMessage, error) {
	var hello agent.HelloMessage
	readCtx, cancel := context.WithTimeout(context.Background(), helloReadLimit)
	defer cancel()
	_, payload, err := conn.Read(readCtx)
	if err != nil {
		return hello, err
	}
	if len(payload) > maxHelloBytes {
		return hello, errors.New("hello too large")
	}
	if err := json.Unmarshal(payload, &hello); err != nil {
		return hello, err
	}
	if hello.Type != "hello" || hello.DeviceID == "" || hello.Secret == "" {
		return hello, errors.New("malformed hello")
	}
	return hello, nil
}

// serveDeviceControl registers the control channel and replies hello_ok, then blocks reading until the connection ends;
// the read is only a keepalive probe (pings are answered automatically by the websocket layer), and the agent contract has no more upstream data frames.
// Online is recorded only after hello_ok is written (a failed handshake produces no online); offline is recorded only when the conn is still the currently registered
// connection (unregistration of a replaced old connection, or after service shutdown swapped the table, is rejected by the Registry).
func (s *Service) serveDeviceControl(conn *websocket.Conn, hello agent.HelloMessage) {
	s.registry.RegisterControl(hello.DeviceID, conn, hello.StateDigest, hello.Capabilities)
	defer func() {
		if s.registry.UnregisterControl(hello.DeviceID, conn) {
			s.deviceOffline(hello.DeviceID)
		}
	}()
	if err := writeControlJSON(context.Background(), conn, controlMessage{Type: "hello_ok"}); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "hello ack failed")
		return
	}
	s.refreshAgentAppVersion(context.Background(), hello.DeviceID, hello.AppVersion)
	s.deviceOnline(hello.DeviceID)
	for {
		if _, _, err := conn.Read(context.Background()); err != nil {
			conn.CloseNow()
			return
		}
	}
}

// serveDeviceBridge pairs the agent's outbound bridge to a pending request by bridge_id,
// replies hello_ok after successful pairing, and blocks until the connection closes (the byte stream is driven by the consumer via wsConn).
// hello_ok is a text control frame: wsConn.Read only accepts binary frames, so consumers never read it into the
// supervisor byte stream.
func (s *Service) serveDeviceBridge(conn *websocket.Conn, hello agent.HelloMessage) {
	bridge := newWSConn(conn)
	deliver, err := s.registry.PairBridge(hello.DeviceID, hello.BridgeID, hello.Purpose, bridge)
	if err != nil {
		_ = writeControlJSON(context.Background(), conn, controlMessage{
			Type: "error", Code: "not_found", Message: "桥接请求不存在或已超时",
		})
		_ = conn.Close(websocket.StatusPolicyViolation, "unknown bridge")
		return
	}
	// Unregister immediately after registration: if the hello_ok write fails, deliver is never called, so unregistration must
	// still happen, otherwise the conn stays in the bridges table forever.
	defer s.registry.UnregisterBridge(bridge)
	if err := writeControlJSON(context.Background(), conn, controlMessage{Type: "hello_ok"}); err != nil {
		_ = bridge.Close()
		return
	}
	deliver()
	<-bridge.closed
}

// wsConn adapts one websocket connection into a net.Conn byte stream: each binary message is a
// segment of the byte stream, byte-transparent (supervisor protocol frames pass through as-is, resume semantics unchanged).
type wsConn struct {
	conn     *websocket.Conn
	deviceID string
	bridgeID string

	mu        sync.Mutex
	readBuf   []byte
	closed    chan struct{}
	closeOnce sync.Once
}

func newWSConn(conn *websocket.Conn) *wsConn {
	return &wsConn{conn: conn, closed: make(chan struct{})}
}

func (c *wsConn) Read(p []byte) (int, error) {
	for {
		c.mu.Lock()
		if len(c.readBuf) > 0 {
			count := copy(p, c.readBuf)
			c.readBuf = c.readBuf[count:]
			c.mu.Unlock()
			return count, nil
		}
		c.mu.Unlock()
		messageType, payload, err := c.conn.Read(context.Background())
		if err != nil {
			return 0, err
		}
		// The bridge byte stream carries only binary frames (supervisor protocol); text control frames (hello_ok
		// and friends) never enter the byte stream.
		if messageType != websocket.MessageBinary {
			continue
		}
		c.mu.Lock()
		c.readBuf = append(c.readBuf, payload...)
		c.mu.Unlock()
	}
}

func (c *wsConn) Write(p []byte) (int, error) {
	if err := c.conn.Write(context.Background(), websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.conn.CloseNow()
	})
	return nil
}

type wsAddr struct{}

func (wsAddr) Network() string { return "websocket" }
func (wsAddr) String() string  { return "websocket" }

func (c *wsConn) LocalAddr() net.Addr  { return wsAddr{} }
func (c *wsConn) RemoteAddr() net.Addr { return wsAddr{} }

// SetDeadline/SetReadDeadline/SetWriteDeadline are no-ops: the supervisor client controls per-request timeouts with
// contexts, and the bridge layer introduces no second set of deadline semantics.
func (c *wsConn) SetDeadline(time.Time) error      { return nil }
func (c *wsConn) SetReadDeadline(time.Time) error  { return nil }
func (c *wsConn) SetWriteDeadline(time.Time) error { return nil }

var _ net.Conn = (*wsConn)(nil)

// pipeBridge shuttles bytes bidirectionally between the user-side websocket and the agent bridge byte stream until either side ends.
func pipeBridge(user *websocket.Conn, bridge *wsConn) {
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			messageType, payload, err := user.Read(context.Background())
			if err != nil {
				return
			}
			if messageType != websocket.MessageBinary {
				continue
			}
			if _, err := bridge.Write(payload); err != nil {
				return
			}
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		buffer := make([]byte, 64*1024)
		for {
			count, err := bridge.Read(buffer)
			if count > 0 {
				if writeErr := user.Write(context.Background(), websocket.MessageBinary, buffer[:count]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	<-done
	_ = bridge.Close()
	_ = user.Close(websocket.StatusNormalClosure, "bridge closed")
}
