package relaymux

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// streamPreface is the first byte of every QUIC stream: QUIC announces a stream only once it carries data, so
// the opener sends it at once and the acceptor strips it on the first read.
const streamPreface = 0x00

// quicCodeNormal is the QUIC application error code of a session close; the reason of a server-side end
// travels as a goaway Reply on the control stream instead.
const quicCodeNormal quic.ApplicationErrorCode = 0

// quicConfig is the carrier's QUIC configuration on both sides.
func quicConfig() *quic.Config {
	return &quic.Config{
		Versions:                       []quic.Version{quic.Version1},
		HandshakeIdleTimeout:           DefaultQUICTimeout,
		MaxIdleTimeout:                 IdleTimeout,
		KeepAlivePeriod:                KeepAlivePeriod,
		InitialStreamReceiveWindow:     512 << 10,
		MaxStreamReceiveWindow:         StreamWindow,
		InitialConnectionReceiveWindow: 1 << 20,
		MaxConnectionReceiveWindow:     ConnectionWindow,
		MaxIncomingStreams:             MaxStreams,
		MaxIncomingUniStreams:          -1,
	}
}

// quicSession is a Session over one QUIC connection. closeConn releases what the session owns beyond the
// connection (the dialer's transport and socket).
type quicSession struct {
	conn      *quic.Conn
	closeConn func()
	done      chan struct{}
	closeOnce sync.Once
}

func newQUICSession(conn *quic.Conn, release func()) *quicSession {
	s := &quicSession{conn: conn, closeConn: release, done: make(chan struct{})}
	go func() {
		<-conn.Context().Done()
		s.finish()
	}()
	return s
}

func (s *quicSession) finish() {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.closeConn != nil {
			s.closeConn()
		}
	})
}

func (s *quicSession) Kind() string          { return KindQUIC }
func (s *quicSession) Done() <-chan struct{} { return s.done }

func (s *quicSession) Close() error {
	_ = s.conn.CloseWithError(quicCodeNormal, "closed")
	s.finish()
	return nil
}

func (s *quicSession) closed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *quicSession) wrap(err error) error {
	if s.closed() {
		return fmt.Errorf("%w: %w", ErrClosed, err)
	}
	return err
}

func (s *quicSession) OpenStream(ctx context.Context) (net.Conn, error) {
	if s.closed() {
		return nil, ErrClosed
	}
	stream, err := s.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, s.wrap(err)
	}
	if _, err := stream.Write([]byte{streamPreface}); err != nil {
		stream.CancelRead(0)
		stream.CancelWrite(0)
		return nil, s.wrap(err)
	}
	return &quicStream{stream: stream, conn: s.conn}, nil
}

func (s *quicSession) AcceptStream(ctx context.Context) (net.Conn, error) {
	if s.closed() {
		return nil, ErrClosed
	}
	stream, err := s.conn.AcceptStream(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, s.wrap(err)
	}
	return &quicStream{stream: stream, conn: s.conn, preface: true}, nil
}

// quicStream is one QUIC stream as a net.Conn. Close closes both directions: buffered writes are still
// delivered followed by FIN, and the peer's further writes fail. CloseWrite sends FIN only.
type quicStream struct {
	stream *quic.Stream
	conn   *quic.Conn

	readMu  sync.Mutex
	preface bool // the acceptor's first Read still has to strip the preface byte

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
}

func (s *quicStream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *quicStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	for s.preface {
		var first [1]byte
		n, err := s.stream.Read(first[:])
		if n == 1 {
			if first[0] != streamPreface {
				s.stream.CancelRead(0)
				return 0, errors.New("relaymux: unknown stream preface")
			}
			s.preface = false
			break
		}
		if err != nil {
			return 0, s.readError(err)
		}
	}
	n, err := s.stream.Read(p)
	if err != nil {
		err = s.readError(err)
	}
	return n, err
}

func (s *quicStream) readError(err error) error {
	if s.isClosed() {
		return net.ErrClosed
	}
	return err
}

func (s *quicStream) Write(p []byte) (int, error) {
	if s.isClosed() {
		return 0, net.ErrClosed
	}
	return s.stream.Write(p)
}

// CloseWrite sends FIN after any buffered data.
func (s *quicStream) CloseWrite() error { return s.stream.Close() }

func (s *quicStream) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		err = s.stream.Close()
		s.stream.CancelRead(0)
	})
	return err
}

// Abort resets both directions, discarding buffered data: the peer reads an error instead of EOF.
func (s *quicStream) Abort() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.stream.CancelWrite(0)
	s.stream.CancelRead(0)
}

func (s *quicStream) LocalAddr() net.Addr                { return s.conn.LocalAddr() }
func (s *quicStream) RemoteAddr() net.Addr               { return s.conn.RemoteAddr() }
func (s *quicStream) SetDeadline(t time.Time) error      { return s.stream.SetDeadline(t) }
func (s *quicStream) SetReadDeadline(t time.Time) error  { return s.stream.SetReadDeadline(t) }
func (s *quicStream) SetWriteDeadline(t time.Time) error { return s.stream.SetWriteDeadline(t) }

// aborter resets a stream instead of closing it gracefully.
type aborter interface{ Abort() }

// Abort resets conn when its carrier supports a reset and closes it otherwise.
func Abort(conn net.Conn) {
	if a, ok := conn.(aborter); ok {
		a.Abort()
		return
	}
	_ = conn.Close()
}
