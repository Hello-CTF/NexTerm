package leg

import (
	"errors"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/stun/v4"
)

// iceAddr is the single fixed peer address QUIC sees. The real 5-tuple is
// whatever ICE has selected; hiding it keeps QUIC from treating an ICE pair
// switch as a connection migration.
type iceAddr struct{}

func (iceAddr) Network() string { return "ice" }
func (iceAddr) String() string  { return "ice-peer" }

var peerAddr net.Addr = iceAddr{}

// readBufferSize exceeds any UDP payload ICE can deliver, so a short QUIC
// read buffer never makes packetio fail.
const readBufferSize = 64 * 1024

// packetConn adapts the selected ICE pair to a fixed-peer net.PacketConn.
//
// quic-go closes the whole transport on any read error and the connection on
// any write error other than "message too large", so transient ICE failures
// are reported as dropped packets. Only a closed agent or a deadline surfaces
// as an error.
type packetConn struct {
	conn    *ice.Conn
	closed  atomic.Bool
	dropped atomic.Uint64
	buf     []byte
}

func newPacketConn(c *ice.Conn) *packetConn {
	return &packetConn{conn: c, buf: make([]byte, readBufferSize)}
}

func (c *packetConn) markClosed() { c.closed.Store(true) }

// ReadFrom is called only by the single quic-go read loop, so the scratch
// buffer needs no lock.
func (c *packetConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, err := c.conn.Read(c.buf)
		if err != nil {
			if errors.Is(err, io.ErrShortBuffer) {
				c.dropped.Add(1)
				continue
			}
			return 0, nil, err
		}
		if n > len(p) {
			c.dropped.Add(1)
			continue
		}
		return copy(p, c.buf[:n]), peerAddr, nil
	}
}

func (c *packetConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	// ice.Conn refuses payloads that parse as STUN; an encrypted QUIC packet
	// can match the magic cookie by chance. Treat it as lost.
	if stun.IsMessage(p) {
		c.dropped.Add(1)
		return len(p), nil
	}
	if _, err := c.conn.Write(p); err != nil {
		if errors.Is(err, ice.ErrClosed) || c.closed.Load() {
			return 0, err
		}
		c.dropped.Add(1)
	}
	return len(p), nil
}

func (c *packetConn) Close() error {
	c.markClosed()
	return nil
}

// SetReadBuffer and SetWriteBuffer accept quic-go's buffer sizing as no-ops:
// the UDP socket belongs to the ICE agent. Without them quic-go logs a
// warning on the standard logger.
func (c *packetConn) SetReadBuffer(int) error  { return nil }
func (c *packetConn) SetWriteBuffer(int) error { return nil }

func (c *packetConn) LocalAddr() net.Addr                { return iceAddr{} }
func (c *packetConn) SetDeadline(t time.Time) error      { return c.conn.SetDeadline(t) }
func (c *packetConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *packetConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }
