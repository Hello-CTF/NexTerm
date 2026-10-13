package purpose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
	"github.com/Hello-CTF/NexTerm/internal/ids"
)

const defaultClaimTimeout = 30 * time.Second

// ListenHub holds inbound connections accepted by listen servers until the
// client claims them by ConnID over an accept bridge. One hub is shared by
// the listen and accept servers of an agent runtime.
type ListenHub struct {
	mu           sync.Mutex
	pending      map[string]*pendingAccept
	claimTimeout time.Duration
}

type pendingAccept struct {
	conn  net.Conn
	owner string
	timer *time.Timer
}

func NewListenHub() *ListenHub {
	return &ListenHub{pending: make(map[string]*pendingAccept), claimTimeout: defaultClaimTimeout}
}

// park holds conn under a fresh ConnID until it is claimed or the claim
// timeout expires; an expired conn is closed. owner tags the ListenServer
// ServeConn that parked it, so teardown can clean up per listener.
func (h *ListenHub) park(conn net.Conn, owner string) string {
	id := ids.New()
	h.mu.Lock()
	h.pending[id] = &pendingAccept{conn: conn, owner: owner, timer: time.AfterFunc(h.claimTimeout, func() { h.expire(id) })}
	h.mu.Unlock()
	return id
}

// dropOwner closes every pending conn parked with the given owner (one
// ListenServer ServeConn); claimed and expired entries are already gone from
// the map and are no-ops, so the pending set never grows past the claim window.
func (h *ListenHub) dropOwner(owner string) {
	h.mu.Lock()
	var owned []string
	for id, entry := range h.pending {
		if entry.owner == owner {
			owned = append(owned, id)
		}
	}
	h.mu.Unlock()
	for _, id := range owned {
		h.drop(id)
	}
}

func (h *ListenHub) expire(id string) {
	h.mu.Lock()
	entry, ok := h.pending[id]
	if ok {
		delete(h.pending, id)
	}
	h.mu.Unlock()
	if ok {
		_ = entry.conn.Close()
	}
}

// claim removes and returns the parked conn; the second result reports
// whether the ConnID was still pending.
func (h *ListenHub) claim(id string) (net.Conn, bool) {
	h.mu.Lock()
	entry, ok := h.pending[id]
	if ok {
		delete(h.pending, id)
	}
	h.mu.Unlock()
	if !ok {
		return nil, false
	}
	entry.timer.Stop()
	return entry.conn, true
}

// drop closes the parked conn if the ConnID is still pending; claimed or
// expired ConnIDs are no-ops.
func (h *ListenHub) drop(id string) {
	if conn, ok := h.claim(id); ok {
		_ = conn.Close()
	}
}

// ListenServer serves listen-purpose bridges: it binds one TCP/unix listener
// on the device per bridge, announces every inbound connection on the listen
// bridge, and parks it in the shared hub until the client claims it over an
// accept bridge. Closing the listen bridge tears down the listener and every
// connection it parked.
type ListenServer struct {
	bridge BridgeDialer
	hub    *ListenHub
}

func NewListenServer(bridge BridgeDialer, hub *ListenHub) *ListenServer {
	return &ListenServer{bridge: bridge, hub: hub}
}

// Serve claims the bridge and serves its listener; it matches the agent
// runtime's purpose handler signature.
func (s *ListenServer) Serve(ctx context.Context, bridgeID string) error {
	conn, err := s.bridge.DialBridge(ctx, bridgeID, deviceproto.PurposeListen)
	if err != nil {
		return err
	}
	return s.ServeConn(ctx, conn)
}

// ServeConn serves the listener requested on conn and closes conn when done.
// A failed bind is reported to the client as an error frame.
func (s *ListenServer) ServeConn(ctx context.Context, conn net.Conn) error {
	defer func() { _ = conn.Close() }()
	kind, payload, err := deviceproto.ReadFrame(conn)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	if kind != deviceproto.FrameListen {
		return deviceproto.WriteError(conn, fmt.Errorf("%w: first frame must be a listen request, got %d", deviceproto.ErrProtocol, byte(kind)))
	}
	var msg deviceproto.ListenMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	switch msg.Network {
	case "tcp", "tcp4", "tcp6", "unix":
	default:
		return deviceproto.WriteError(conn, fmt.Errorf("%w: unsupported listen network %q", deviceproto.ErrInvalid, msg.Network))
	}
	listener, err := net.Listen(msg.Network, msg.Address)
	if err != nil {
		return deviceproto.WriteError(conn, err)
	}
	defer func() { _ = listener.Close() }()
	if err := deviceproto.WriteMessage(conn, deviceproto.FrameListenAck, deviceproto.ListenAckMsg{Address: listener.Addr().String()}); err != nil {
		return err
	}

	// The client sends nothing more on a listen bridge; a read failure means
	// the bridge is gone and the listener must come down.
	bridgeDead := make(chan struct{})
	go func() {
		defer close(bridgeDead)
		for {
			if _, _, err := deviceproto.ReadFrame(conn); err != nil {
				return
			}
		}
	}()

	owner := ids.New()
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			inbound, err := listener.Accept()
			if err != nil {
				return
			}
			connID := s.hub.park(inbound, owner)
			announce := deviceproto.InboundMsg{ConnID: connID, Address: inbound.RemoteAddr().String()}
			if err := deviceproto.WriteMessage(conn, deviceproto.FrameInbound, announce); err != nil {
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
	case <-bridgeDead:
	case <-acceptDone:
	}
	_ = listener.Close()
	<-acceptDone
	s.hub.dropOwner(owner)
	return nil
}

// AcceptServer serves accept-purpose bridges: the first frame names a parked
// inbound connection by ConnID, which is then pumped with the same
// full-duplex/half-close logic as the forward dial server.
type AcceptServer struct {
	bridge BridgeDialer
	hub    *ListenHub
}

func NewAcceptServer(bridge BridgeDialer, hub *ListenHub) *AcceptServer {
	return &AcceptServer{bridge: bridge, hub: hub}
}

// Serve claims the bridge and serves its connection; it matches the agent
// runtime's purpose handler signature.
func (s *AcceptServer) Serve(ctx context.Context, bridgeID string) error {
	conn, err := s.bridge.DialBridge(ctx, bridgeID, deviceproto.PurposeAccept)
	if err != nil {
		return err
	}
	return s.ServeConn(ctx, conn)
}

// ServeConn claims the parked connection named on conn and closes conn when
// done. An unknown or expired ConnID is reported as an error frame.
func (s *AcceptServer) ServeConn(ctx context.Context, conn net.Conn) error {
	defer func() { _ = conn.Close() }()
	kind, payload, err := deviceproto.ReadFrame(conn)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	if kind != deviceproto.FrameAccept {
		return deviceproto.WriteError(conn, fmt.Errorf("%w: first frame must be an accept request, got %d", deviceproto.ErrProtocol, byte(kind)))
	}
	var msg deviceproto.AcceptMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		return deviceproto.WriteError(conn, err)
	}
	target, ok := s.hub.claim(msg.ConnID)
	if !ok {
		return deviceproto.WriteError(conn, fmt.Errorf("%w: unknown or expired inbound connection %q", deviceproto.ErrInvalid, msg.ConnID))
	}
	defer func() { _ = target.Close() }()
	pumpBridge(ctx, conn, target)
	return nil
}
