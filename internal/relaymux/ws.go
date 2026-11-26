package relaymux

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// wsReadLimit is the websocket message limit of the carrier; yamux frames written by one Write are at most a
// stream window plus a header, and the coder/websocket NetConn writes each Write as one message.
const wsReadLimit = StreamWindow + (64 << 10)

func yamuxConfig() *yamux.Config {
	config := yamux.DefaultConfig()
	config.AcceptBacklog = 1024
	config.EnableKeepAlive = true
	config.KeepAliveInterval = KeepAlivePeriod
	config.ConnectionWriteTimeout = 30 * time.Second
	config.MaxStreamWindowSize = StreamWindow
	// A stream closed locally whose peer never closes its side is reset after this, freeing its window.
	config.StreamCloseTimeout = 30 * time.Second
	config.StreamOpenTimeout = HandshakeTimeout
	config.LogOutput = io.Discard
	return config
}

// yamuxSession is a Session of yamux streams over one websocket.
type yamuxSession struct {
	mux       *yamux.Session
	ws        *websocket.Conn
	done      chan struct{}
	closeOnce sync.Once
}

func newYamuxSession(ws *websocket.Conn, client bool) (*yamuxSession, error) {
	ws.SetReadLimit(wsReadLimit)
	carrier := &coalescingConn{Conn: websocket.NetConn(context.Background(), ws, websocket.MessageBinary)}
	var mux *yamux.Session
	var err error
	if client {
		mux, err = yamux.Client(carrier, yamuxConfig())
	} else {
		mux, err = yamux.Server(carrier, yamuxConfig())
	}
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	s := &yamuxSession{mux: mux, ws: ws, done: make(chan struct{})}
	go func() {
		<-mux.CloseChan()
		s.finish()
	}()
	return s, nil
}

func (s *yamuxSession) finish() {
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.mux.Close()
		s.ws.CloseNow()
	})
}

func (s *yamuxSession) Kind() string          { return KindWS }
func (s *yamuxSession) Done() <-chan struct{} { return s.done }

func (s *yamuxSession) Close() error {
	s.finish()
	return nil
}

func (s *yamuxSession) closed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *yamuxSession) wrap(err error) error {
	if s.closed() || errors.Is(err, yamux.ErrSessionShutdown) {
		return fmt.Errorf("%w: %w", ErrClosed, err)
	}
	return err
}

func (s *yamuxSession) OpenStream(ctx context.Context) (net.Conn, error) {
	if s.closed() {
		return nil, ErrClosed
	}
	type result struct {
		stream *yamux.Stream
		err    error
	}
	// yamux.OpenStream takes no context; it sends the SYN at once and only blocks on the session's write lock.
	opened := make(chan result, 1)
	go func() {
		stream, err := s.mux.OpenStream()
		opened <- result{stream, err}
	}()
	select {
	case r := <-opened:
		if r.err != nil {
			return nil, s.wrap(r.err)
		}
		return &yamuxStream{stream: r.stream}, nil
	case <-ctx.Done():
		go func() {
			if r := <-opened; r.stream != nil {
				_ = r.stream.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

func (s *yamuxSession) AcceptStream(ctx context.Context) (net.Conn, error) {
	if s.closed() {
		return nil, ErrClosed
	}
	stream, err := s.mux.AcceptStreamWithContext(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, s.wrap(err)
	}
	return &yamuxStream{stream: stream}, nil
}

// yamuxStream gives a yamux stream the carrier's net.Conn contract: yamux's Close is a half close, so Close
// here also ends local reads, while CloseWrite keeps yamux's half close.
type yamuxStream struct {
	stream *yamux.Stream

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
}

func (s *yamuxStream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *yamuxStream) Read(p []byte) (int, error) {
	n, err := s.stream.Read(p)
	if err != nil && s.isClosed() {
		return n, net.ErrClosed
	}
	return n, err
}

func (s *yamuxStream) Write(p []byte) (int, error) {
	if s.isClosed() {
		return 0, net.ErrClosed
	}
	n, err := s.stream.Write(p)
	if err != nil && s.isClosed() {
		return n, net.ErrClosed
	}
	return n, err
}

func (s *yamuxStream) CloseWrite() error { return s.stream.Close() }

func (s *yamuxStream) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		err = s.stream.Close()
		// Wake a Read blocked on the half-closed stream.
		_ = s.stream.SetReadDeadline(time.Now())
	})
	return err
}

func (s *yamuxStream) LocalAddr() net.Addr                { return s.stream.LocalAddr() }
func (s *yamuxStream) RemoteAddr() net.Addr               { return s.stream.RemoteAddr() }
func (s *yamuxStream) SetDeadline(t time.Time) error      { return s.stream.SetDeadline(t) }
func (s *yamuxStream) SetReadDeadline(t time.Time) error  { return s.stream.SetReadDeadline(t) }
func (s *yamuxStream) SetWriteDeadline(t time.Time) error { return s.stream.SetWriteDeadline(t) }

// DialWS dials the websocket carrier at url (ws:// or wss://, ending in WSPath) and starts a yamux client
// session over it. httpClient carries the caller's TLS settings and proxy; header adds request headers. No
// credential travels on the upgrade: the session authenticates with a Hello on its control stream.
func DialWS(ctx context.Context, url string, httpClient *http.Client, header http.Header) (Session, error) {
	conn, response, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: httpClient, HTTPHeader: header})
	if err != nil {
		if response != nil {
			return nil, fmt.Errorf("relaymux: websocket carrier: HTTP %d: %w", response.StatusCode, err)
		}
		return nil, fmt.Errorf("relaymux: websocket carrier: %w", err)
	}
	return newYamuxSession(conn, true)
}

// AcceptWS upgrades an HTTP request to the websocket carrier and starts the server side of its yamux session.
// The default Origin check of the upgrade stays on.
func AcceptWS(w http.ResponseWriter, r *http.Request) (Session, error) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil, err
	}
	return newYamuxSession(conn, false)
}

// yamuxHeaderSize and yamuxTypeData follow yamux's frame header: version, type, flags, stream id, length.
const (
	yamuxHeaderSize = 12
	yamuxTypeData   = 0
)

// coalescingConn sends a yamux data frame's header and body as one websocket message: yamux writes them with
// two Write calls from its single send loop, and each Write of the websocket NetConn is a message of its own
// (a frame header, a flush and usually a TCP segment).
type coalescingConn struct {
	net.Conn
	header []byte
}

func (c *coalescingConn) Write(p []byte) (int, error) {
	if c.header == nil && len(p) == yamuxHeaderSize && p[1] == yamuxTypeData && binary.BigEndian.Uint32(p[8:12]) > 0 {
		c.header = append(make([]byte, 0, yamuxHeaderSize), p...)
		return len(p), nil
	}
	if c.header != nil {
		message := make([]byte, 0, len(c.header)+len(p))
		message = append(append(message, c.header...), p...)
		c.header = nil
		if _, err := c.Conn.Write(message); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return c.Conn.Write(p)
}
