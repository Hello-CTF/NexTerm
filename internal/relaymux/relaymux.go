// Package relaymux keeps one persistent multiplexed session between a NexTerm client or agent and
// nexterm-server, and carries every device operation as a stream of that session instead of a websocket of
// its own.
//
// Two carriers implement the same Session contract: native QUIC (ALPN nexterm-relay/1) on the server's STUN
// UDP socket, and yamux over one websocket on the HTTPS port for networks or proxies that block UDP. Dial
// races them Happy-Eyeballs style, giving QUIC a short head start.
//
// The first stream a dialer opens is the control stream: it carries the Hello that authenticates the session
// and stays open for the session's lifetime, so the server can state why it ends a session (a revoked login,
// a revoked device). Every later stream starts with a StreamHeader the server authorizes, and continues as a
// raw byte stream once a Reply with status 200 came back.
//
// The package is a leaf: it knows neither the fleet server nor the agent runtime, which both import it.
package relaymux

import (
	"context"
	"errors"
	"net"
	"time"
)

const (
	// ALPN is the QUIC application protocol of the relay.
	ALPN = "nexterm-relay/1"
	// Capability is the agent hello capability announcing that the agent keeps a relay-mux session and serves
	// device streams over it. Such an agent adds the SSH session variables to its terminals itself.
	Capability = "relay-mux"
	// ConfigPath is the server route that hands out the relay configuration (Config) to an authenticated
	// client (account session cookie) or agent (device credential headers).
	ConfigPath = "/relay/config"
	// WSPath is the websocket fallback carrier route on the HTTPS port.
	WSPath = "/relay/ws"
	// Version is the wire version in Hello and Config.
	Version = 1

	// RoleClient and RoleAgent are the two kinds of session.
	RoleClient = "client"
	RoleAgent  = "agent"

	// KindQUIC and KindWS are the carrier kinds a Session reports.
	KindQUIC = "quic"
	KindWS   = "ws"

	// ParamStream and ParamResume are the stream header and reply params that mirror the relay bridge
	// query (rstream.RelayStreamParam / RelayResumeParam): stream=1 asks for an rstream carrier, resume=1 marks
	// a RESUME, and a reply with stream=1 states the agent reads the stream as an rstream carrier.
	ParamStream = "stream"
	ParamResume = "resume"
)

// Timeouts and keepalives shared by both carriers.
const (
	// HandshakeTimeout bounds reading a Hello, a StreamHeader or their Reply.
	HandshakeTimeout = 10 * time.Second
	// StreamReplyTimeout bounds waiting for the server's reply to a device stream. The server may wait up to a
	// minute for an older agent to dial a legacy bridge, so the client waits a little longer than that; the
	// caller's context still ends the wait sooner.
	StreamReplyTimeout = 65 * time.Second
	// KeepAlivePeriod is the idle keepalive of both carriers; it stays below common proxy idle timeouts.
	KeepAlivePeriod = 15 * time.Second
	// IdleTimeout closes a QUIC session whose peer has been silent this long.
	IdleTimeout = 45 * time.Second
	// DefaultHeadStart is how long Dial lets QUIC run before it also starts the websocket carrier.
	DefaultHeadStart = 300 * time.Millisecond
	// DefaultQUICBackoff is how long a server whose QUIC attempt failed is dialed over the websocket only.
	DefaultQUICBackoff = 10 * time.Minute
	// DefaultQUICTimeout bounds a QUIC carrier attempt.
	DefaultQUICTimeout = 3 * time.Second

	// StreamWindow is the per-stream receive window of both carriers; ConnectionWindow bounds a QUIC session.
	StreamWindow     = 4 << 20
	ConnectionWindow = 32 << 20
	// MaxStreams bounds concurrently open peer streams per session.
	MaxStreams = 4096
)

// Session is one multiplexed carrier session. Every stream is a net.Conn whose Close closes both directions;
// streams of the QUIC and websocket carriers also implement CloseWrite (a TCP-like half close).
type Session interface {
	// OpenStream opens a stream; the peer's AcceptStream returns it without waiting for application data.
	OpenStream(ctx context.Context) (net.Conn, error)
	// AcceptStream waits for the next stream the peer opened.
	AcceptStream(ctx context.Context) (net.Conn, error)
	// Close tears the session and all its streams down.
	Close() error
	// Done is closed once the session is unusable.
	Done() <-chan struct{}
	// Kind reports the carrier: KindQUIC or KindWS.
	Kind() string
}

// ErrClosed is returned by a closed session.
var ErrClosed = errors.New("relaymux: session closed")

// closeWriter is the half-close a stream of either carrier offers.
type closeWriter interface {
	CloseWrite() error
}

// CloseWrite half-closes conn when its carrier supports it and closes it otherwise.
func CloseWrite(conn net.Conn) error {
	if half, ok := conn.(closeWriter); ok {
		return half.CloseWrite()
	}
	return conn.Close()
}
