package purpose

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/deviceproto"
)

type listenHarness struct {
	hub    *ListenHub
	ctx    context.Context
	cancel context.CancelFunc
}

func newListenHarness(t *testing.T) *listenHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &listenHarness{hub: NewListenHub(), ctx: ctx, cancel: cancel}
}

// listenBridge runs ListenServer.ServeConn on one end of a pipe and returns
// the client end of the listen bridge.
func (h *listenHarness) listenBridge(t *testing.T) net.Conn {
	t.Helper()
	server := NewListenServer(nil, h.hub)
	clientConn, serverConn := net.Pipe()
	go func() { _ = server.ServeConn(h.ctx, serverConn) }()
	t.Cleanup(func() { _ = clientConn.Close() })
	return clientConn
}

// acceptBridge runs AcceptServer.ServeConn on one end of a pipe and returns
// the client end of the accept bridge.
func (h *listenHarness) acceptBridge(t *testing.T) net.Conn {
	t.Helper()
	server := NewAcceptServer(nil, h.hub)
	clientConn, serverConn := net.Pipe()
	go func() { _ = server.ServeConn(h.ctx, serverConn) }()
	t.Cleanup(func() { _ = clientConn.Close() })
	return clientConn
}

// bindListen requests a listener over the bridge and returns the bound
// address from the ack.
func bindListen(t *testing.T, bridge net.Conn, network, address string) string {
	t.Helper()
	if err := deviceproto.WriteMessage(bridge, deviceproto.FrameListen, deviceproto.ListenMsg{Network: network, Address: address}); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := deviceproto.ReadFrame(bridge)
	if err != nil {
		t.Fatal(err)
	}
	if kind != deviceproto.FrameListenAck {
		t.Fatalf("kind = %d; want FrameListenAck", kind)
	}
	var ack deviceproto.ListenAckMsg
	if err := deviceproto.UnmarshalPayload(payload, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Address == "" {
		t.Fatal("ListenAckMsg carries an empty bound address")
	}
	return ack.Address
}

// readInbound reads one FrameInbound announcement from the listen bridge.
func readInbound(t *testing.T, bridge net.Conn) deviceproto.InboundMsg {
	t.Helper()
	kind, payload, err := deviceproto.ReadFrame(bridge)
	if err != nil {
		t.Fatal(err)
	}
	if kind != deviceproto.FrameInbound {
		t.Fatalf("kind = %d; want FrameInbound", kind)
	}
	var msg deviceproto.InboundMsg
	if err := deviceproto.UnmarshalPayload(payload, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.ConnID == "" {
		t.Fatal("InboundMsg carries an empty ConnID")
	}
	return msg
}

func TestListenServerAcceptEchoRoundTrip(t *testing.T) {
	h := newListenHarness(t)
	listen := h.listenBridge(t)
	bound := bindListen(t, listen, "tcp", "127.0.0.1:0")

	inbound, err := net.Dial("tcp", bound)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inbound.Close() }()
	announce := readInbound(t, listen)
	if announce.Address == "" {
		t.Fatal("InboundMsg carries an empty peer address")
	}

	accept := h.acceptBridge(t)
	if err := deviceproto.WriteMessage(accept, deviceproto.FrameAccept, deviceproto.AcceptMsg{ConnID: announce.ConnID}); err != nil {
		t.Fatal(err)
	}

	// Bridge to device: a data frame lands on the accepted connection.
	if err := deviceproto.WriteFrame(accept, deviceproto.FrameDialData, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(inbound, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "ping" {
		t.Fatalf("inbound read = %q; want ping", buffer)
	}

	// Device to bridge: accepted-connection bytes arrive as data frames.
	if _, err := inbound.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := deviceproto.ReadFrame(accept)
	if err != nil {
		t.Fatal(err)
	}
	if kind != deviceproto.FrameDialData || string(payload) != "pong" {
		t.Fatalf("frame = %d %q; want FrameDialData pong", kind, payload)
	}

	// A client half-close reaches the accepted connection as EOF.
	if err := deviceproto.WriteFrame(accept, deviceproto.FrameDialEOF, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := inbound.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("inbound read after FrameDialEOF = %v; want io.EOF", err)
	}

	// Closing the accepted connection ends the bridge stream with FrameDialEOF.
	if err := inbound.Close(); err != nil {
		t.Fatal(err)
	}
	if err := accept.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	kind, _, err = deviceproto.ReadFrame(accept)
	if err != nil {
		t.Fatal(err)
	}
	if kind != deviceproto.FrameDialEOF {
		t.Fatalf("kind = %d; want FrameDialEOF after the accepted connection closed", kind)
	}
}

func TestListenServerRejectsBadNetwork(t *testing.T) {
	h := newListenHarness(t)
	listen := h.listenBridge(t)
	if err := deviceproto.WriteMessage(listen, deviceproto.FrameListen, deviceproto.ListenMsg{Network: "udp", Address: "127.0.0.1:53"}); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := deviceproto.ReadFrame(listen)
	if err != nil {
		t.Fatal(err)
	}
	if kind != deviceproto.FrameError {
		t.Fatalf("kind = %d; want FrameError", kind)
	}
	if !errors.Is(deviceproto.ReadError(payload), deviceproto.ErrInvalid) {
		t.Fatalf("error = %v; want ErrInvalid", deviceproto.ReadError(payload))
	}
}

func TestListenServerRejectsNonListenFirstFrame(t *testing.T) {
	h := newListenHarness(t)
	listen := h.listenBridge(t)
	if err := deviceproto.WriteMessage(listen, deviceproto.FrameDial, deviceproto.DialMsg{Network: "tcp", Address: "x:1"}); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := deviceproto.ReadFrame(listen)
	if err != nil {
		t.Fatal(err)
	}
	if kind != deviceproto.FrameError {
		t.Fatalf("kind = %d; want FrameError", kind)
	}
	if !errors.Is(deviceproto.ReadError(payload), deviceproto.ErrProtocol) {
		t.Fatalf("error = %v; want ErrProtocol", deviceproto.ReadError(payload))
	}
}

func TestAcceptServerUnknownConn(t *testing.T) {
	h := newListenHarness(t)
	accept := h.acceptBridge(t)
	if err := deviceproto.WriteMessage(accept, deviceproto.FrameAccept, deviceproto.AcceptMsg{ConnID: "no-such-conn"}); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := deviceproto.ReadFrame(accept)
	if err != nil {
		t.Fatal(err)
	}
	if kind != deviceproto.FrameError {
		t.Fatalf("kind = %d; want FrameError", kind)
	}
	if !errors.Is(deviceproto.ReadError(payload), deviceproto.ErrInvalid) {
		t.Fatalf("error = %v; want ErrInvalid", deviceproto.ReadError(payload))
	}
}

func TestListenHubPendingExpires(t *testing.T) {
	hub := NewListenHub()
	hub.claimTimeout = 50 * time.Millisecond
	serverConn, clientConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	connID := hub.park(serverConn, "test-owner")

	closed := make(chan error, 1)
	go func() {
		_, err := clientConn.Read(make([]byte, 1))
		closed <- err
	}()
	select {
	case err := <-closed:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("parked conn read = %v; want io.EOF after the claim timeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parked conn still open after the claim timeout")
	}
	if _, ok := hub.claim(connID); ok {
		t.Fatal("claim after expiry must fail")
	}
}

func TestListenServerBridgeCloseTearsDown(t *testing.T) {
	h := newListenHarness(t)
	listen := h.listenBridge(t)
	bound := bindListen(t, listen, "tcp", "127.0.0.1:0")

	inbound, err := net.Dial("tcp", bound)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inbound.Close() }()
	announce := readInbound(t, listen)

	if err := listen.Close(); err != nil {
		t.Fatal(err)
	}

	// Teardown closes the listener first and the parked conns after, so once
	// the parked conn reads EOF the listener must already refuse new dials.
	if err := inbound.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := inbound.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("parked conn read = %v; want io.EOF after the listen bridge closed", err)
	}
	if _, err := net.Dial("tcp", bound); err == nil {
		t.Fatal("listener still accepts after the listen bridge closed")
	}
	if _, ok := h.hub.claim(announce.ConnID); ok {
		t.Fatal("teardown must drop the parked conn from the hub")
	}
}

func TestListenServerCtxCancelTearsDown(t *testing.T) {
	h := newListenHarness(t)
	listen := h.listenBridge(t)
	bound := bindListen(t, listen, "tcp", "127.0.0.1:0")
	h.cancel()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.Dial("tcp", bound)
		if err != nil {
			break
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("listener still accepts after ctx cancel")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestListenAcceptServeClaimsBridge(t *testing.T) {
	listenEnds := make(chan net.Conn, 1)
	acceptEnds := make(chan net.Conn, 1)
	claimed := make(chan string, 2)
	dialer := func(ends chan<- net.Conn) BridgeDialer {
		return BridgeDialerFunc(func(ctx context.Context, bridgeID, purpose string) (net.Conn, error) {
			serverSide, clientSide := net.Pipe()
			ends <- clientSide
			claimed <- bridgeID + "/" + purpose
			return serverSide, nil
		})
	}
	hub := NewListenHub()
	listenServer := NewListenServer(dialer(listenEnds), hub)
	acceptServer := NewAcceptServer(dialer(acceptEnds), hub)
	go func() { _ = listenServer.Serve(context.Background(), "bridge-listen") }()
	go func() { _ = acceptServer.Serve(context.Background(), "bridge-accept") }()

	listen := <-listenEnds
	bound := bindListen(t, listen, "tcp", "127.0.0.1:0")
	inbound, err := net.Dial("tcp", bound)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inbound.Close() }()
	announce := readInbound(t, listen)

	accept := <-acceptEnds
	if err := deviceproto.WriteMessage(accept, deviceproto.FrameAccept, deviceproto.AcceptMsg{ConnID: announce.ConnID}); err != nil {
		t.Fatal(err)
	}
	if err := deviceproto.WriteFrame(accept, deviceproto.FrameDialData, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := inbound.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{
		"bridge-listen/" + deviceproto.PurposeListen: true,
		"bridge-accept/" + deviceproto.PurposeAccept: true,
	}
	for i := 0; i < 2; i++ {
		select {
		case got := <-claimed:
			if !want[got] {
				t.Fatalf("Serve claimed %q; want one of %v", got, want)
			}
			delete(want, got)
		case <-time.After(2 * time.Second):
			t.Fatalf("Serve claimed only %d bridges; want listen and accept", i)
		}
	}
	_ = listen.Close()
	_ = accept.Close()
}
