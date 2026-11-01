package purpose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/wol/netrule"
)

const defaultDialTimeout = 15 * time.Second

// DialServer serves forward-purpose bridges: it parses one dial request,
// connects to the target from the device, and pipes bytes both ways with
// half-close propagation, one connection per bridge.
type DialServer struct {
	bridge  BridgeDialer
	timeout time.Duration
	// udpAllowed vets udp4 targets; tests replace it to reach a loopback listener.
	udpAllowed func(address string) error
}

func NewDialServer(bridge BridgeDialer) *DialServer {
	return &DialServer{bridge: bridge, timeout: defaultDialTimeout, udpAllowed: checkUDPTarget}
}

// Serve claims the bridge and serves its connection; it matches the agent
// runtime's purpose handler signature.
func (s *DialServer) Serve(ctx context.Context, bridgeID string) error {
	conn, err := s.bridge.DialBridge(ctx, bridgeID, deviceproto.PurposeForward)
	if err != nil {
		return err
	}
	return s.ServeConn(ctx, conn)
}

// ServeConn serves the dial requested on conn and closes conn when done. A
// failed target dial is reported to the client as an error frame.
func (s *DialServer) ServeConn(ctx context.Context, conn net.Conn) error {
	defer func() { _ = conn.Close() }()
	kind, payload, err := deviceproto.ReadFrame(conn)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	if kind != deviceproto.FrameDial {
		return deviceproto.WriteError(conn, fmt.Errorf("%w: first frame must be a dial request, got %d", deviceproto.ErrProtocol, byte(kind)))
	}
	var msg deviceproto.DialMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	switch msg.Network {
	case "tcp", "tcp4", "tcp6", "unix", "npipe":
	case "udp4":
		// Datagram forwarding exists for Wake-on-LAN magic packets only; it is limited to routable IPv4 literals.
		if err := s.udpAllowed(msg.Address); err != nil {
			return deviceproto.WriteError(conn, err)
		}
	default:
		return deviceproto.WriteError(conn, fmt.Errorf("%w: unsupported dial network %q", deviceproto.ErrInvalid, msg.Network))
	}
	target, err := dialTarget(ctx, msg.Network, msg.Address, s.timeout)
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	defer func() { _ = target.Close() }()
	if err := deviceproto.WriteFrame(conn, deviceproto.FrameDialed, nil); err != nil {
		return err
	}
	if msg.Network == "udp4" {
		return serveWakeDatagram(conn, target)
	}
	pumpBridge(ctx, conn, target)
	return nil
}

const wakeFirstFrameTimeout = 10 * time.Second

// serveWakeDatagram is the whole udp4 conversation: exactly one data frame holding exactly one magic packet is
// written to the target as one datagram, then the agent reports the end of the stream and closes the socket. Nothing
// the target sends is read or relayed, and anything else from the client is refused.
func serveWakeDatagram(bridge, target net.Conn) error {
	_ = bridge.SetReadDeadline(time.Now().Add(wakeFirstFrameTimeout))
	kind, payload, err := deviceproto.ReadFrame(bridge)
	if err != nil {
		return err
	}
	if kind != deviceproto.FrameDialData || !netrule.IsMagicPacket(payload) {
		return deviceproto.WriteError(bridge, fmt.Errorf("%w: udp payload must be exactly one magic packet", deviceproto.ErrInvalid))
	}
	if _, err := target.Write(payload); err != nil {
		return deviceproto.WriteError(bridge, err)
	}
	return deviceproto.WriteFrame(bridge, deviceproto.FrameDialEOF, nil)
}

// checkUDPTarget accepts an IPv4 literal with a port that is not on the always-deny list, so the agent cannot be
// aimed at its own services or a metadata endpoint with datagrams.
func checkUDPTarget(address string) error {
	target, err := netip.ParseAddrPort(address)
	if err != nil || !target.Addr().Is4() || target.Port() == 0 {
		return fmt.Errorf("%w: udp target must be an IPv4 address and port", deviceproto.ErrInvalid)
	}
	if reason := netrule.Denied(target.Addr()); reason != "" {
		return fmt.Errorf("%w: udp target refused (%s)", deviceproto.ErrInvalid, reason)
	}
	return nil
}

// pumpBridge pipes bytes between the bridge and the target in both
// directions with half-close propagation. It returns when the context ends,
// the bridge dies, or both directions wound down after the target's EOF.
func pumpBridge(ctx context.Context, bridge, target net.Conn) {
	bridgeDone := make(chan struct{})
	targetDone := make(chan struct{})
	go func() {
		defer close(bridgeDone)
		bridgeToTarget(bridge, target)
	}()
	go func() {
		defer close(targetDone)
		targetToBridge(bridge, target)
	}()
	select {
	case <-ctx.Done():
		// Close both ends explicitly so the pump goroutines unwind now
		// instead of at the deferred closes on return.
		_ = bridge.Close()
		_ = target.Close()
		<-bridgeDone
		<-targetDone
	case <-bridgeDone:
		// The client is gone; nothing more can reach it, so abort the target.
		_ = target.Close()
		<-targetDone
	case <-targetDone:
		// The target closed its write side and the EOF marker is out; the
		// client may still send, so keep pumping until it closes.
		<-bridgeDone
	}
}

// bridgeToTarget forwards client data frames to the target; a FrameDialEOF
// half-closes the target's write side. It returns when the bridge fails or
// the client violates the stream order.
func bridgeToTarget(bridge, target net.Conn) {
	halfClosed := false
	for {
		kind, payload, err := deviceproto.ReadFrame(bridge)
		if err != nil {
			return
		}
		switch kind {
		case deviceproto.FrameDialData:
			if halfClosed {
				return
			}
			if _, err := target.Write(payload); err != nil {
				return
			}
		case deviceproto.FrameDialEOF:
			if halfClosed {
				return
			}
			halfClosed = true
			closeTargetWrite(target)
		default:
			return
		}
	}
}

// targetToBridge forwards target bytes as data frames; target EOF or failure
// ends the stream with a FrameDialEOF marker.
func targetToBridge(bridge, target net.Conn) {
	buffer := make([]byte, deviceproto.DataChunkSize)
	for {
		count, err := target.Read(buffer)
		if count > 0 {
			if writeErr := deviceproto.WriteFrame(bridge, deviceproto.FrameDialData, buffer[:count]); writeErr != nil {
				return
			}
		}
		if err != nil {
			_ = deviceproto.WriteFrame(bridge, deviceproto.FrameDialEOF, nil)
			return
		}
	}
}

func closeTargetWrite(conn net.Conn) {
	if closer, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
		return
	}
	_ = conn.Close()
}
