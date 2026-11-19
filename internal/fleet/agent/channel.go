package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/wscompress"
	"github.com/coder/websocket"
)

const (
	defaultWSKeepAlive   = 25 * time.Second
	defaultWSPingTimeout = 10 * time.Second
)

// wsDialOptions applies the endpoint TLS and proxy policy to websocket dials:
// default verification stays strict, and only an explicit insecure opt-in
// skips it. Redirects are refused so a bridge or control dial can never be
// silently downgraded or forwarded to another origin.
func wsDialOptions(policy DialPolicy, compression wscompress.Purpose) (*websocket.DialOptions, error) {
	return wsDialOptionsObserved(policy, compression, nil)
}

// wsDialOptionsObserved is wsDialOptions that also reports every connection the dial opens to onDial; behind a
// proxy that is the connection to the proxy.
func wsDialOptionsObserved(policy DialPolicy, compression wscompress.Purpose, onDial func(net.Conn)) (*websocket.DialOptions, error) {
	transport, err := policy.transport()
	if err != nil {
		return nil, err
	}
	if onDial != nil {
		dial := transport.DialContext
		if dial == nil {
			dial = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := dial(ctx, network, address)
			if err == nil {
				onDial(conn)
			}
			return conn, err
		}
	}
	return wscompress.Dial(compression, &websocket.DialOptions{
		HTTPClient: &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}), nil
}

type HelloMessage struct {
	Type       string `json:"type"`
	Protocol   int    `json:"protocol"`
	DeviceID   string `json:"device_id"`
	Secret     string `json:"secret"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version"`
	BridgeID   string `json:"bridge_id,omitempty"`
	// Purpose echoes the bridge request's purpose, so the server pairs the connection with the right consumer; empty means terminal.
	Purpose string `json:"purpose,omitempty"`
	// Capabilities advertises the bridge purposes this agent serves; the server requests a non-terminal
	// bridge only after the agent advertised it here.
	Capabilities []string `json:"capabilities,omitempty"`
	// StateDigest is the device-side supervisor state directory digest (sha256(identity\x00dir)),
	// with which the server passes the helper's hello verification for managed attach/create sessions; the digest itself
	// is not a credential, it only identifies the state directory on the device.
	StateDigest string `json:"state_digest,omitempty"`
	// HelperInstanceID is the random id of the helper state directory. It only tells apart helpers on different
	// machines that happen to share uid and path (and thus StateDigest); it is not a credential.
	HelperInstanceID string `json:"helper_instance_id,omitempty"`
}

type serverMessage struct {
	Type              string `json:"type"`
	Code              string `json:"code,omitempty"`
	Message           string `json:"message,omitempty"`
	ServerVersion     string `json:"server_version,omitempty"`
	Reason            string `json:"reason,omitempty"`
	DesiredAutostart  *bool  `json:"desired_autostart,omitempty"`
	MetricsIntervalMS *int64 `json:"metrics_interval_ms,omitempty"`
	TerminalEnabled   *bool  `json:"terminal_enabled,omitempty"`
	BridgeID          string `json:"bridge_id,omitempty"`
	Purpose           string `json:"purpose,omitempty"`
}

// ProtocolMismatchError is the runtime error form of the version_mismatch control frame:
// errors.Is(err, ErrProtocolMismatch) is always true, and ServerVersion carries the version reported by the server
// (empty when the server does not carry one), for self-upgrade to resolve the target version.
type ProtocolMismatchError struct {
	ServerVersion string
}

func (e *ProtocolMismatchError) Error() string { return ErrProtocolMismatch.Error() }

func (e *ProtocolMismatchError) Unwrap() error { return ErrProtocolMismatch }

type ConfigUpdate struct {
	DesiredAutostart  bool
	MetricsIntervalMS int64
	TerminalEnabled   bool
}

type channelHandlers struct {
	onConfig func(ConfigUpdate)
	onBridge func(bridgeID, purpose string)
	onRevoke func()
}

type Channel struct {
	conn        *websocket.Conn
	keepAlive   time.Duration
	pingTimeout time.Duration
	// serverVersion is the release the server reported in hello_ok; empty for a server that predates it.
	serverVersion string
}

// ServerVersion is the release the server reported when the channel opened, or empty.
func (c *Channel) ServerVersion() string { return c.serverVersion }

// DialChannel opens the outbound control channel and performs the hello
// handshake. A server error frame comes back as a *ServerError with Code
// set to the wire code (for example version_mismatch or forbidden).
func DialChannel(ctx context.Context, wsURL string, hello HelloMessage, policy DialPolicy, keepAlive, pingTimeout time.Duration) (*Channel, error) {
	options, err := wsDialOptions(policy, wscompress.Control)
	if err != nil {
		return nil, err
	}
	conn, response, err := websocket.Dial(ctx, wsURL, options)
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
		c.serverVersion = first.ServerVersion
		return nil
	case "error":
		return &ServerError{Code: first.Code, Message: first.Message, ServerVersion: first.ServerVersion, Reason: first.Reason}
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
				handlers.onBridge(message.BridgeID, message.Purpose)
			}
		case "revoke":
			if message.Reason == ErrorCodeOwnerUnavailable {
				return &ServerError{Code: ErrorCodeOwnerUnavailable, Message: message.Message, Reason: message.Reason}
			}
			if handlers.onRevoke != nil {
				handlers.onRevoke()
			}
			return ErrRevoked
		case "error":
			if message.Code == "version_mismatch" {
				return &ProtocolMismatchError{ServerVersion: message.ServerVersion}
			}
			return &ServerError{Code: message.Code, Message: message.Message, ServerVersion: message.ServerVersion, Reason: message.Reason}
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
func DialBridge(ctx context.Context, wsURL string, hello HelloMessage, policy DialPolicy) (*websocket.Conn, error) {
	conn, _, err := dialBridgeWithEnv(ctx, wsURL, hello, policy)
	return conn, err
}

// dialBridgeWithEnv is DialBridge that also returns the SSH session variables describing the connection, for
// the terminals created through it.
func dialBridgeWithEnv(ctx context.Context, wsURL string, hello HelloMessage, policy DialPolicy) (*websocket.Conn, []string, error) {
	var (
		mu       sync.Mutex
		endpoint net.Conn
	)
	options, err := wsDialOptionsObserved(policy, wscompress.ForBridge(hello.Purpose), func(conn net.Conn) {
		mu.Lock()
		endpoint = conn
		mu.Unlock()
	})
	if err != nil {
		return nil, nil, err
	}
	conn, err := finishBridgeDial(ctx, wsURL, hello, options)
	if err != nil {
		return nil, nil, err
	}
	mu.Lock()
	defer mu.Unlock()
	var env []string
	if endpoint != nil {
		env = sshSessionEnv(endpoint.LocalAddr(), endpoint.RemoteAddr())
	}
	return conn, env, nil
}

func finishBridgeDial(ctx context.Context, wsURL string, hello HelloMessage, options *websocket.DialOptions) (*websocket.Conn, error) {
	conn, response, err := websocket.Dial(ctx, wsURL, options)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		return nil, &ServerError{Status: status, Message: err.Error()}
	}
	// deviceproto frames can reach 256KiB+overhead on the wire; the default 32KiB read limit would tear the bridge.
	conn.SetReadLimit(1 << 20)
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
		return nil, &ServerError{Code: message.Code, Message: message.Message, ServerVersion: message.ServerVersion, Reason: message.Reason}
	default:
		_ = conn.Close(websocket.StatusProtocolError, "bad handshake")
		return nil, fmt.Errorf("unexpected bridge handshake response %q", message.Type)
	}
}

// isForbidden reports a real revocation. The temporary owner_unavailable signal also arrives as 403 but is not one.
func isForbidden(err error) bool {
	var serverError *ServerError
	return errors.As(err, &serverError) && !serverError.ownerUnavailable() && (serverError.forbidden() || serverError.Code == "forbidden")
}

// isOwnerUnavailable reports that the owning account is disabled or reset-required; the device is not revoked.
func isOwnerUnavailable(err error) bool {
	var serverError *ServerError
	return errors.As(err, &serverError) && serverError.ownerUnavailable()
}

// sshSessionEnv states a bridge connection the way sshd states the connection of a session: the peer is the
// client and this host the server. An address that is not TCP is reported as 0.0.0.0 port 0, so programs still
// see a remote session.
func sshSessionEnv(local, remote net.Addr) []string {
	clientIP, clientPort := tcpEndpoint(remote)
	serverIP, serverPort := tcpEndpoint(local)
	return []string{
		fmt.Sprintf("SSH_CONNECTION=%s %d %s %d", clientIP, clientPort, serverIP, serverPort),
		fmt.Sprintf("SSH_CLIENT=%s %d %d", clientIP, clientPort, serverPort),
	}
}

func tcpEndpoint(addr net.Addr) (string, int) {
	if tcp, ok := addr.(*net.TCPAddr); ok && tcp.IP != nil {
		return tcp.IP.String(), tcp.Port
	}
	return "0.0.0.0", 0
}
