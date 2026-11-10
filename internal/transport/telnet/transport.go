// Package telnet opens interactive terminals on Telnet servers (RFC 854) such as switches, routers and other
// legacy equipment. Telnet carries no encryption and no exec channel: every terminal is its own TCP connection.
package telnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	DefaultPort        = 23
	DefaultDialTimeout = 15 * time.Second
	defaultTerm        = "xterm-256color"
)

var nextGeneration atomic.Uint64

type Config struct {
	Host        string
	Port        int
	DialTimeout time.Duration
	// Dialer replaces the direct TCP dial, for example with a jump connection. Nil dials directly.
	Dialer base.Dialer
	// LocalEcho chooses who shows typed characters; empty is EchoAuto.
	LocalEcho EchoMode
	// SendCRLF sends Enter as CR LF instead of CR NUL, for line-based text services behind a Telnet port.
	SendCRLF bool
}

func (c Config) normalized() (Config, error) {
	c.Host = strings.TrimSpace(c.Host)
	if c.Host == "" {
		return c, errors.New("telnet host is empty")
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.Port < 1 || c.Port > 65535 {
		return c, fmt.Errorf("telnet port %d is out of range", c.Port)
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = DefaultDialTimeout
	}
	echo, err := ParseEchoMode(string(c.LocalEcho))
	if err != nil {
		return c, err
	}
	c.LocalEcho = echo
	return c, nil
}

// pendingTTL bounds how long the connection Connect dialed waits for the first terminal before it is closed.
const pendingTTL = 30 * time.Second

// Transport holds the target of a Telnet asset. Each terminal is its own TCP connection: OpenPTY dials one per
// terminal and Close ends all of them.
type Transport struct {
	config     Config
	generation uint64

	mu       sync.Mutex
	closed   bool
	channels map[*Conn]struct{}
	// pending is the connection Connect dialed to prove the server is reachable; the first OpenPTY uses it, so a
	// device with a few login lines sees one connection per terminal.
	pending      net.Conn
	pendingTimer *time.Timer
}

func New(config Config) (*Transport, error) {
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	return &Transport{config: normalized, generation: nextGeneration.Add(1), channels: map[*Conn]struct{}{}}, nil
}

// Connect dials the server, so a wrong host or port fails at connect time instead of at the first terminal, and
// keeps that connection for the first OpenPTY.
func Connect(ctx context.Context, config Config) (*Transport, error) {
	transport, err := New(config)
	if err != nil {
		return nil, err
	}
	conn, err := transport.dial(ctx)
	if err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.pending = conn
	transport.pendingTimer = time.AfterFunc(pendingTTL, func() {
		if stale := transport.takePending(conn); stale != nil {
			_ = stale.Close()
		}
	})
	transport.mu.Unlock()
	return transport, nil
}

// takePending returns the pre-dialed connection and clears it. A non-nil want only takes that connection.
func (t *Transport) takePending(want net.Conn) net.Conn {
	t.mu.Lock()
	defer t.mu.Unlock()
	conn := t.pending
	if conn == nil || want != nil && conn != want {
		return nil
	}
	t.pending = nil
	if t.pendingTimer != nil {
		t.pendingTimer.Stop()
		t.pendingTimer = nil
	}
	return conn
}

func (t *Transport) Address() string {
	return net.JoinHostPort(t.config.Host, strconv.Itoa(t.config.Port))
}

func (t *Transport) dial(ctx context.Context) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, t.config.DialTimeout)
	defer cancel()
	var conn net.Conn
	var err error
	if t.config.Dialer != nil {
		conn, err = t.config.Dialer.DialContext(dialCtx, "tcp", t.Address())
	} else {
		var dialer net.Dialer
		conn, err = dialer.DialContext(dialCtx, "tcp", t.Address())
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("telnet dial %s: %w", t.Address(), err)
	}
	return conn, nil
}

func (t *Transport) Kind() string { return "telnet" }

func (t *Transport) Generation() uint64 { return t.generation }

func (t *Transport) Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error) {
	return base.ExecResult{}, fmt.Errorf("%w: telnet has no command channel", base.ErrUnsupported)
}

// Ping reports the transport state without dialing: devices often allow only a few concurrent Telnet logins, and a
// probe connection would take one of them.
func (t *Transport) Ping(ctx context.Context) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !t.IsAlive() {
		return 0, base.ErrClosed
	}
	return 0, nil
}

func (t *Transport) IsAlive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.closed
}

func (t *Transport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	pending := t.pending
	t.pending = nil
	if t.pendingTimer != nil {
		t.pendingTimer.Stop()
		t.pendingTimer = nil
	}
	channels := make([]*Conn, 0, len(t.channels))
	for channel := range t.channels {
		channels = append(channels, channel)
	}
	t.channels = nil
	t.mu.Unlock()
	if pending != nil {
		_ = pending.Close()
	}
	for _, channel := range channels {
		_ = channel.Close()
	}
	return nil
}

func (t *Transport) OpenPTY(ctx context.Context, options base.PTYOptions) (base.Channel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.ExpectedGeneration != 0 && options.ExpectedGeneration != t.generation {
		return nil, base.ErrStaleGeneration
	}
	if !t.IsAlive() {
		return nil, base.ErrClosed
	}
	raw := t.takePending(nil)
	if raw == nil {
		var err error
		raw, err = t.dial(ctx)
		if err != nil {
			return nil, err
		}
	}
	term := options.Term
	if term == "" {
		term = defaultTerm
	}
	conn := newConn(raw, connOptions{term: term, echo: t.config.LocalEcho, crlf: t.config.SendCRLF}, uint16(min(options.Cols, 0xffff)), uint16(min(options.Rows, 0xffff)), t.generation)
	conn.onClose = func() { t.forget(conn) }
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		_ = conn.Close()
		return nil, base.ErrClosed
	}
	t.channels[conn] = struct{}{}
	t.mu.Unlock()
	if err := conn.start(ctx); err != nil {
		_ = conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return conn, nil
}

func (t *Transport) forget(conn *Conn) {
	t.mu.Lock()
	delete(t.channels, conn)
	t.mu.Unlock()
}

var (
	_ base.Transport    = (*Transport)(nil)
	_ base.PTYTransport = (*Transport)(nil)
)
