package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

const (
	defaultWSKeepAlive   = 25 * time.Second
	defaultWSPingTimeout = 10 * time.Second
)

// wsDialOptions applies the endpoint TLS policy to websocket dials: default
// verification stays strict, and only an explicit insecure opt-in skips it.
// Redirects are refused so a bridge or control dial can never be silently
// downgraded or forwarded to another origin.
func wsDialOptions(insecure bool) *websocket.DialOptions {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &websocket.DialOptions{
		HTTPClient: &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		CompressionMode: websocket.CompressionContextTakeover,
	}
}

type HelloMessage struct {
	Type       string `json:"type"`
	Protocol   int    `json:"protocol"`
	DeviceID   string `json:"device_id"`
	Secret     string `json:"secret"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version"`
	BridgeID   string `json:"bridge_id,omitempty"`
	// StateDigest 是设备端 supervisor 状态目录摘要 (sha256(identity\x00dir)),
	// 服务端代管 attach/create 会话时以此通过 helper 的 hello 校验; 摘要本身
	// 不是凭据, 只标识设备上的状态目录。
	StateDigest string `json:"state_digest,omitempty"`
}

type serverMessage struct {
	Type              string `json:"type"`
	Code              string `json:"code,omitempty"`
	Message           string `json:"message,omitempty"`
	DesiredAutostart  *bool  `json:"desired_autostart,omitempty"`
	MetricsIntervalMS *int64 `json:"metrics_interval_ms,omitempty"`
	TerminalEnabled   *bool  `json:"terminal_enabled,omitempty"`
	BridgeID          string `json:"bridge_id,omitempty"`
}

type ConfigUpdate struct {
	DesiredAutostart  bool
	MetricsIntervalMS int64
	TerminalEnabled   bool
}

type channelHandlers struct {
	onConfig func(ConfigUpdate)
	onBridge func(bridgeID string)
	onRevoke func()
}

type Channel struct {
	conn        *websocket.Conn
	keepAlive   time.Duration
	pingTimeout time.Duration
}

// DialChannel opens the outbound control channel and performs the hello
// handshake. A server error frame comes back as a *ServerError with Code
// set to the wire code (for example version_mismatch or forbidden).
func DialChannel(ctx context.Context, wsURL string, hello HelloMessage, insecure bool, keepAlive, pingTimeout time.Duration) (*Channel, error) {
	conn, response, err := websocket.Dial(ctx, wsURL, wsDialOptions(insecure))
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		return nil, &ServerError{Status: status, Message: err.Error()}
	}
	channel := &Channel{conn: conn, keepAlive: keepAlive, pingTimeout: pingTimeout}
	if err := channel.handshake(ctx, hello); err != nil {
		_ = conn.Close(websocket.StatusProtocolError, "handshake failed")
		return nil, err
	}
	return channel, nil
}

func (c *Channel) handshake(ctx context.Context, hello HelloMessage) error {
	payload, err := json.Marshal(hello)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, c.pingTimeoutDuration())
	defer cancel()
	if err := c.conn.Write(writeCtx, websocket.MessageText, payload); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}
	var first serverMessage
	if err := c.readJSON(ctx, &first); err != nil {
		return err
	}
	switch first.Type {
	case "hello_ok":
		return nil
	case "error":
		return &ServerError{Code: first.Code, Message: first.Message}
	default:
		return fmt.Errorf("unexpected hello response %q", first.Type)
	}
}

// Run reads control messages until the connection fails, the context ends,
// or the server revokes the device. It sends periodic pings to detect dead
// connections through NAT timeouts.
func (c *Channel) Run(ctx context.Context, handlers channelHandlers) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go c.keepAliveLoop(ctx)
	for {
		var message serverMessage
		if err := c.readJSON(ctx, &message); err != nil {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			return err
		}
		switch message.Type {
		case "config":
			if handlers.onConfig != nil {
				update := ConfigUpdate{}
				if message.DesiredAutostart != nil {
					update.DesiredAutostart = *message.DesiredAutostart
				}
				if message.MetricsIntervalMS != nil {
					update.MetricsIntervalMS = clampMetricsInterval(*message.MetricsIntervalMS)
				}
				if message.TerminalEnabled != nil {
					update.TerminalEnabled = *message.TerminalEnabled
				}
				handlers.onConfig(update)
			}
		case "bridge":
			if handlers.onBridge != nil && message.BridgeID != "" {
				handlers.onBridge(message.BridgeID)
			}
		case "revoke":
			if handlers.onRevoke != nil {
				handlers.onRevoke()
			}
			return ErrRevoked
		case "error":
			if message.Code == "version_mismatch" {
				return ErrProtocolMismatch
			}
			return &ServerError{Code: message.Code, Message: message.Message}
		default:
			return fmt.Errorf("unknown control message %q", message.Type)
		}
	}
}

func (c *Channel) keepAliveLoop(ctx context.Context) {
	interval := c.keepAlive
	if interval <= 0 {
		interval = defaultWSKeepAlive
	}
	timeout := c.pingTimeoutDuration()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		pingCtx, cancel := context.WithTimeout(ctx, timeout)
		err := c.conn.Ping(pingCtx)
		cancel()
		if err != nil {
			_ = c.conn.Close(websocket.StatusGoingAway, "keepalive failed")
			return
		}
	}
}

func (c *Channel) pingTimeoutDuration() time.Duration {
	if c.pingTimeout <= 0 {
		return defaultWSPingTimeout
	}
	return c.pingTimeout
}

func (c *Channel) readJSON(ctx context.Context, message *serverMessage) error {
	_, payload, err := c.conn.Read(ctx)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, message); err != nil {
		return fmt.Errorf("malformed control message: %w", err)
	}
	return nil
}

func (c *Channel) Close() error {
	return c.conn.Close(websocket.StatusNormalClosure, "")
}

// DialBridge opens a dedicated outbound bridge connection carrying one
// supervisor protocol stream as binary frames.
func DialBridge(ctx context.Context, wsURL string, hello HelloMessage, insecure bool) (*websocket.Conn, error) {
	conn, response, err := websocket.Dial(ctx, wsURL, wsDialOptions(insecure))
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		return nil, &ServerError{Status: status, Message: err.Error()}
	}
	payload, err := json.Marshal(hello)
	if err != nil {
		_ = conn.Close(websocket.StatusProtocolError, "bad hello")
		return nil, err
	}
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		_ = conn.Close(websocket.StatusProtocolError, "hello failed")
		return nil, fmt.Errorf("send bridge hello: %w", err)
	}
	_, first, err := conn.Read(ctx)
	if err != nil {
		_ = conn.Close(websocket.StatusProtocolError, "hello failed")
		return nil, err
	}
	var message serverMessage
	if err := json.Unmarshal(first, &message); err != nil {
		_ = conn.Close(websocket.StatusProtocolError, "bad handshake")
		return nil, fmt.Errorf("malformed bridge handshake: %w", err)
	}
	switch message.Type {
	case "hello_ok":
		return conn, nil
	case "error":
		_ = conn.Close(websocket.StatusPolicyViolation, message.Code)
		return nil, &ServerError{Code: message.Code, Message: message.Message}
	default:
		_ = conn.Close(websocket.StatusProtocolError, "bad handshake")
		return nil, fmt.Errorf("unexpected bridge handshake response %q", message.Type)
	}
}

func isForbidden(err error) bool {
	var serverError *ServerError
	return errors.As(err, &serverError) && (serverError.forbidden() || serverError.Code == "forbidden")
}

func isDevicePending(err error) bool {
	var serverError *ServerError
	return errors.As(err, &serverError) && serverError.Code == CodeDevicePending
}
