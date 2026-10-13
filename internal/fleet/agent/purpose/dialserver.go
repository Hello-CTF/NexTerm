package purpose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
)

const defaultDialTimeout = 15 * time.Second

// DialServer serves forward-purpose bridges: it parses one dial request,
// connects to the target from the device, and pipes bytes both ways with
// half-close propagation, one connection per bridge.
type DialServer struct {
	bridge  BridgeDialer
	timeout time.Duration
}

func NewDialServer(bridge BridgeDialer) *DialServer {
	return &DialServer{bridge: bridge, timeout: defaultDialTimeout}
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
	case "tcp", "tcp4", "tcp6", "unix":
	default:
		return deviceproto.WriteError(conn, fmt.Errorf("%w: unsupported dial network %q", deviceproto.ErrInvalid, msg.Network))
	}
	target, err := (&net.Dialer{Timeout: s.timeout}).DialContext(ctx, msg.Network, msg.Address)
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	defer func() { _ = target.Close() }()
	if err := deviceproto.WriteFrame(conn, deviceproto.FrameDialed, nil); err != nil {
		return err
	}

	bridgeDone := make(chan struct{})
	targetDone := make(chan struct{})
	go func() {
		defer close(bridgeDone)
		bridgeToTarget(conn, target)
	}()
	go func() {
		defer close(targetDone)
		targetToBridge(conn, target)
	}()
	select {
	case <-ctx.Done():
		// Close both ends explicitly so the pump goroutines unwind now
		// instead of at the deferred closes on return.
		_ = conn.Close()
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
	return nil
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
