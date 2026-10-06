package fleetserver

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/fleet/agent"
	"github.com/coder/websocket"
)

const (
	maxHelloBytes  = 64 << 10
	helloReadLimit = 10 * time.Second
)

// controlMessage 是服务端 -> agent 的控制通道帧, 字段语义与
// internal/fleet/agent 的 serverMessage 一一对应 (该类型未导出, 此处镜像)。
type controlMessage struct {
	Type              string `json:"type"`
	Code              string `json:"code,omitempty"`
	Message           string `json:"message,omitempty"`
	DesiredAutostart  *bool  `json:"desired_autostart,omitempty"`
	MetricsIntervalMS *int64 `json:"metrics_interval_ms,omitempty"`
	TerminalEnabled   *bool  `json:"terminal_enabled,omitempty"`
	BridgeID          string `json:"bridge_id,omitempty"`
}

func writeControlJSON(ctx context.Context, conn *websocket.Conn, message controlMessage) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}

// serveDeviceWS 处理 GET /ws/device: agent 出站控制通道与桥接共用入口。
// 首帧必须是 hello; protocol 不匹配回 version_mismatch, 凭证无效回 forbidden,
// 两种错误帧都会让 agent 按合同停止 (协议错误退出 / 视同吊销)。
func (s *Service) serveDeviceWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	hello, err := readDeviceHello(conn)
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "bad hello")
		return
	}
	if hello.Protocol != agent.ProtocolVersion {
		_ = writeControlJSON(r.Context(), conn, controlMessage{
			Type: "error", Code: "version_mismatch",
			Message: "控制通道协议版本不兼容",
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

// serveDeviceControl 注册控制通道并回复 hello_ok, 随后阻塞读直到连接结束;
// 读只是保活探测 (ping 由 websocket 层自动应答), agent 合同里不再上行数据帧。
func (s *Service) serveDeviceControl(conn *websocket.Conn, hello agent.HelloMessage) {
	s.registry.RegisterControl(hello.DeviceID, conn, hello.StateDigest)
	defer s.registry.UnregisterControl(hello.DeviceID, conn)
	if err := writeControlJSON(context.Background(), conn, controlMessage{Type: "hello_ok"}); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "hello ack failed")
		return
	}
	for {
		if _, _, err := conn.Read(context.Background()); err != nil {
			conn.CloseNow()
			return
		}
	}
}

// serveDeviceBridge 把 agent 出站桥接按 bridge_id 配对给 pending 请求,
// 配对成功后回 hello_ok 并阻塞到连接关闭 (字节流由消费方经 wsConn 驱动)。
// hello_ok 是文本控制帧: wsConn.Read 只认二进制帧, 消费方不会把它读进
// supervisor 字节流。
func (s *Service) serveDeviceBridge(conn *websocket.Conn, hello agent.HelloMessage) {
	bridge := newWSConn(conn)
	deliver, err := s.registry.PairBridge(hello.DeviceID, hello.BridgeID, bridge)
	if err != nil {
		_ = writeControlJSON(context.Background(), conn, controlMessage{
			Type: "error", Code: "not_found", Message: "桥接请求不存在或已超时",
		})
		_ = conn.Close(websocket.StatusPolicyViolation, "unknown bridge")
		return
	}
	// 登记后立即挂注销: hello_ok 写失败时 deliver 不会被调用, 注销必须
	// 仍然发生, 否则 conn 永久留在 bridges 表里。
	defer s.registry.UnregisterBridge(bridge)
	if err := writeControlJSON(context.Background(), conn, controlMessage{Type: "hello_ok"}); err != nil {
		_ = bridge.Close()
		return
	}
	deliver()
	<-bridge.closed
}

// wsConn 把一条 websocket 连接适配为 net.Conn 字节流: 每个二进制消息是
// 字节流的一段, 字节透明 (supervisor 协议帧原样穿过, resume 语义不变)。
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
		// 桥接字节流只承载二进制帧 (supervisor 协议); 文本控制帧 (hello_ok
		// 等) 不进入字节流。
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

// SetDeadline/SetReadDeadline/SetWriteDeadline 是 no-op: supervisor 客户端以
// context 控制单次请求超时, 桥接层不引入第二套时限语义。
func (c *wsConn) SetDeadline(time.Time) error      { return nil }
func (c *wsConn) SetReadDeadline(time.Time) error  { return nil }
func (c *wsConn) SetWriteDeadline(time.Time) error { return nil }

var _ net.Conn = (*wsConn)(nil)

// pipeBridge 在用户侧 websocket 与 agent 桥接字节流之间双向搬运, 直到任一侧结束。
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
		buffer := make([]byte, 32*1024)
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
