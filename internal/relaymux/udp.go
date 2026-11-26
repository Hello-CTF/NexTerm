package relaymux

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// UDPMux serves the QUIC relay and a STUN responder on one UDP socket.
//
// The socket stays a *net.UDPConn owned by a quic.Transport, so quic-go keeps its batched reads, GSO and ECN
// paths (a wrapping PacketConn would hide the socket and disable them). quic-go hands every datagram that
// cannot be QUIC to Transport.ReadNonQUICPacket: the QUIC fixed bit (0x40) is set in every QUIC packet, and
// STUN messages start with two zero bits (RFC 7983), so STUN Binding requests always take that path. Its
// queue holds 32 packets and drops beyond that, which a rate-limited STUN responder tolerates (a client
// retransmits). STUN answers go out through Transport.WriteTo on the same socket, so the reflexive address a
// client learns is the address of the relay port.
type UDPMux struct {
	conn      net.PacketConn
	transport *quic.Transport
	listener  *quic.Listener
	stun      *stunPacketConn

	closeOnce sync.Once
	closeErr  error
}

// ListenUDPMux takes ownership of conn and starts accepting QUIC relay sessions on it with tlsConfig.
func ListenUDPMux(conn net.PacketConn, tlsConfig *tls.Config) (*UDPMux, error) {
	transport := &quic.Transport{Conn: conn}
	listener, err := transport.Listen(tlsConfig, quicConfig())
	if err != nil {
		_ = transport.Close()
		_ = conn.Close()
		return nil, err
	}
	m := &UDPMux{conn: conn, transport: transport, listener: listener}
	ctx, cancel := context.WithCancel(context.Background())
	m.stun = &stunPacketConn{mux: m, ctx: ctx, cancel: cancel}
	return m, nil
}

// Addr is the bound UDP address.
func (m *UDPMux) Addr() net.Addr { return m.conn.LocalAddr() }

// Accept waits for the next QUIC relay session. It is not yet authenticated: run AcceptHandshake on it.
func (m *UDPMux) Accept(ctx context.Context) (Session, error) {
	conn, err := m.listener.Accept(ctx)
	if err != nil {
		if errors.Is(err, quic.ErrServerClosed) || errors.Is(err, quic.ErrTransportClosed) {
			return nil, ErrClosed
		}
		return nil, err
	}
	return newQUICSession(conn, nil), nil
}

// STUNConn is the non-QUIC side of the socket as a net.PacketConn for stunserver.Server.Serve. Closing it
// stops only the STUN reader; the socket belongs to the mux.
func (m *UDPMux) STUNConn() net.PacketConn { return m.stun }

// Close stops the listener and the STUN reader and closes the socket with every session on it. It is
// idempotent.
func (m *UDPMux) Close() error {
	m.closeOnce.Do(func() {
		_ = m.stun.Close()
		_ = m.listener.Close()
		m.closeErr = m.transport.Close()
		if err := m.conn.Close(); m.closeErr == nil && !errors.Is(err, net.ErrClosed) {
			m.closeErr = err
		}
	})
	return m.closeErr
}

// stunPacketConn reads the non-QUIC datagrams of the mux and writes through the transport.
type stunPacketConn struct {
	mux    *UDPMux
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

func (c *stunPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.mux.transport.ReadNonQUICPacket(c.ctx, p)
	if err != nil {
		// The transport reports its close with a plain error; the STUN loop treats net.ErrClosed as shutdown.
		return 0, nil, net.ErrClosed
	}
	return n, addr, nil
}

func (c *stunPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if c.ctx.Err() != nil {
		return 0, net.ErrClosed
	}
	return c.mux.transport.WriteTo(p, addr)
}

func (c *stunPacketConn) Close() error {
	c.once.Do(c.cancel)
	return nil
}

func (c *stunPacketConn) LocalAddr() net.Addr              { return c.mux.conn.LocalAddr() }
func (c *stunPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *stunPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *stunPacketConn) SetWriteDeadline(time.Time) error { return nil }
