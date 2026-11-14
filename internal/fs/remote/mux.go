package remote

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// Preamble is the first line a file channel server writes, before the
// multiplexed session starts; a host whose helper predates the channel exits
// with a usage error instead.
const Preamble = "nexterm-files/1\n"

const preambleTimeout = 20 * time.Second

func muxConfig() *yamux.Config {
	config := yamux.DefaultConfig()
	config.AcceptBacklog = 256
	// Keepalive lets each side notice a dead peer: the helper exits instead
	// of lingering after the SSH connection is gone.
	config.EnableKeepAlive = true
	config.KeepAliveInterval = 30 * time.Second
	config.ConnectionWriteTimeout = 30 * time.Second
	config.MaxStreamWindowSize = 1 << 20
	config.LogOutput = io.Discard
	return config
}

type stdioConn struct {
	io.Reader
	io.Writer
	closers []io.Closer
}

func (c stdioConn) Close() error {
	for _, closer := range c.closers {
		_ = closer.Close()
	}
	return nil
}

// ServeStdio serves the file channel on stdin and stdout until the client
// closes the channel or stops answering keepalives; every operation still
// running is cancelled then.
func ServeStdio(ctx context.Context, stdin io.ReadCloser, stdout io.WriteCloser) error {
	if _, err := io.WriteString(stdout, Preamble); err != nil {
		return err
	}
	session, err := yamux.Server(stdioConn{Reader: stdin, Writer: stdout, closers: []io.Closer{stdin, stdout}}, muxConfig())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	server := NewServer()
	var operations sync.WaitGroup
	for {
		stream, err := session.AcceptStream()
		if err != nil {
			break
		}
		operations.Add(1)
		go func() {
			defer operations.Done()
			_ = server.ServeConn(ctx, stream)
		}()
	}
	cancel()
	_ = session.Close()
	operations.Wait()
	return nil
}

// Mux opens operation streams over one multiplexed channel, dialing the
// channel on first use and again after it broke.
type Mux struct {
	dial func(ctx context.Context) (net.Conn, error)

	mu      sync.Mutex
	session *yamux.Session
	closed  bool
}

// NewMux returns a Mux over the channels dial opens. dial's ctx only bounds
// opening the channel; the channel must outlive it.
func NewMux(dial func(ctx context.Context) (net.Conn, error)) *Mux {
	return &Mux{dial: dial}
}

var errMuxClosed = errors.New("file channel closed")

// Open returns a new stream for one operation.
func (m *Mux) Open(ctx context.Context) (net.Conn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errMuxClosed
	}
	if m.session == nil || m.session.IsClosed() {
		session, err := m.connect(ctx)
		if err != nil {
			return nil, err
		}
		m.session = session
	}
	stream, err := m.session.OpenStream()
	if err != nil {
		_ = m.session.Close()
		m.session = nil
		return nil, fmt.Errorf("open file channel stream: %w", err)
	}
	return stream, nil
}

// Ready reports whether the channel is connected now.
func (m *Mux) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.session != nil && !m.session.IsClosed()
}

func (m *Mux) connect(ctx context.Context) (*yamux.Session, error) {
	conn, err := m.dial(ctx)
	if err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(conn, len(Preamble))
	line := make(chan error, 1)
	go func() {
		got, err := reader.ReadString('\n')
		if err == nil && got != Preamble {
			err = fmt.Errorf("unexpected file channel preamble %q", strings.TrimSpace(got))
		}
		line <- err
	}()
	timer := time.NewTimer(preambleTimeout)
	defer timer.Stop()
	select {
	case err = <-line:
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
		err = errors.New("file channel did not answer")
	}
	if err != nil {
		_ = conn.Close()
		if detail := channelStderr(conn); detail != "" {
			return nil, fmt.Errorf("start file channel: %w: %s", err, detail)
		}
		return nil, fmt.Errorf("start file channel: %w", err)
	}
	session, err := yamux.Client(bufferedConn{Conn: conn, reader: reader}, muxConfig())
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return session, nil
}

// channelStderr returns what a failed channel process printed, when the
// connection carries its stderr.
func channelStderr(conn net.Conn) string {
	carrier, ok := conn.(interface{ Stderr() io.Reader })
	if !ok {
		return ""
	}
	output := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(io.LimitReader(carrier.Stderr(), 1024))
		output <- strings.TrimSpace(string(data))
	}()
	select {
	case detail := <-output:
		return detail
	case <-time.After(2 * time.Second):
		return ""
	}
}

// bufferedConn keeps the bytes read past the preamble.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c bufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

// Close closes the channel; later Opens fail.
func (m *Mux) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.session != nil {
		_ = m.session.Close()
		m.session = nil
	}
	return nil
}
