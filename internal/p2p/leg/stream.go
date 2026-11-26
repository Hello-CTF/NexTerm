package leg

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// streamPreface is the first byte of every stream. QUIC only announces a
// stream once it carries data, so the opener sends it right away.
const streamPreface = 0x00

const prefaceTimeout = 5 * time.Second

func readPreface(ctx context.Context, s *quic.Stream) error {
	deadline := time.Now().Add(prefaceTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = s.SetReadDeadline(deadline)
	var b [1]byte
	_, err := s.Read(b[:])
	_ = s.SetReadDeadline(time.Time{})
	if err != nil {
		return err
	}
	if b[0] != streamPreface {
		return errors.New("p2p leg: unknown stream preface")
	}
	return nil
}

// stream is one QUIC stream as a net.Conn. Close closes both directions:
// pending writes are still delivered followed by FIN, and the peer's further
// writes fail. CloseWrite sends FIN only, like a TCP half close.
type stream struct {
	s         *quic.Stream
	conn      *quic.Conn
	closeOnce sync.Once
	mu        sync.Mutex
	closed    bool
}

func newStream(s *quic.Stream, conn *quic.Conn) *stream {
	return &stream{s: s, conn: conn}
}

func (s *stream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *stream) Read(p []byte) (int, error) {
	n, err := s.s.Read(p)
	if err != nil && s.isClosed() {
		return n, net.ErrClosed
	}
	return n, err
}

func (s *stream) Write(p []byte) (int, error) {
	if s.isClosed() {
		return 0, net.ErrClosed
	}
	return s.s.Write(p)
}

// CloseWrite sends FIN after any buffered data.
func (s *stream) CloseWrite() error { return s.s.Close() }

func (s *stream) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		err = s.s.Close()
		s.s.CancelRead(0)
	})
	return err
}

func (s *stream) LocalAddr() net.Addr                { return s.conn.LocalAddr() }
func (s *stream) RemoteAddr() net.Addr               { return s.conn.RemoteAddr() }
func (s *stream) SetDeadline(t time.Time) error      { return s.s.SetDeadline(t) }
func (s *stream) SetReadDeadline(t time.Time) error  { return s.s.SetReadDeadline(t) }
func (s *stream) SetWriteDeadline(t time.Time) error { return s.s.SetWriteDeadline(t) }
