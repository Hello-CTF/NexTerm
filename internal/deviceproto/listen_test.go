package deviceproto

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"testing"
	"time"
)

// serveAcceptBridge is the scripted agent half of one accept bridge: the
// FrameAccept claims the conn ID, then the dial data plane echoes.
func serveAcceptBridge(t *testing.T, conn net.Conn, claimed chan<- AcceptMsg) {
	defer func() { _ = conn.Close() }()
	kind, payload, err := ReadFrame(conn)
	if err != nil {
		return
	}
	if kind != FrameAccept {
		_ = WriteError(conn, fmt.Errorf("%w: first frame must be an accept request, got %d", ErrProtocol, byte(kind)))
		return
	}
	var msg AcceptMsg
	if err := UnmarshalPayload(payload, &msg); err != nil {
		return
	}
	claimed <- msg
	for {
		kind, data, err := ReadFrame(conn)
		if err != nil {
			return
		}
		switch kind {
		case FrameDialData:
			if err := WriteFrame(conn, FrameDialData, data); err != nil {
				return
			}
		case FrameDialEOF:
			_ = WriteFrame(conn, FrameDialEOF, nil)
			return
		}
	}
}

func TestClientListenAcceptFramingAndHalfClose(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	claimed := make(chan AcceptMsg, 1)
	var dialPurpose string
	dial := func(ctx context.Context, purpose string) (net.Conn, error) {
		dialPurpose = purpose
		if purpose != PurposeAccept {
			return nil, fmt.Errorf("unexpected bridge purpose %q", purpose)
		}
		conn, server := net.Pipe()
		go serveAcceptBridge(t, server, claimed)
		return conn, nil
	}
	go func() {
		defer func() { _ = serverConn.Close() }()
		kind, payload, err := ReadFrame(serverConn)
		if err != nil || kind != FrameListen {
			return
		}
		var msg ListenMsg
		if err := UnmarshalPayload(payload, &msg); err != nil {
			return
		}
		if msg.Network != "tcp" || msg.Address != "127.0.0.1:0" {
			return
		}
		if err := WriteMessage(serverConn, FrameListenAck, ListenAckMsg{Address: "127.0.0.1:43210"}); err != nil {
			return
		}
		_ = WriteMessage(serverConn, FrameInbound, InboundMsg{ConnID: "conn-1", Address: "203.0.113.7:9000"})
	}()
	client := NewClient(clientConn, dial)
	listener, err := client.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if listener.Addr().Network() != "tcp" || listener.Addr().String() != "127.0.0.1:43210" {
		t.Fatalf("Addr = %s/%s; want tcp/127.0.0.1:43210 from the ack", listener.Addr().Network(), listener.Addr().String())
	}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-claimed:
		if msg.ConnID != "conn-1" {
			t.Fatalf("claimed conn ID = %q; want conn-1", msg.ConnID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("accept bridge never received the FrameAccept claim")
	}
	if dialPurpose != PurposeAccept {
		t.Fatalf("accept bridge purpose = %q; want %q", dialPurpose, PurposeAccept)
	}
	if conn.LocalAddr().String() != "127.0.0.1:43210" || conn.RemoteAddr().String() != "203.0.113.7:9000" {
		t.Fatalf("addrs = %q -> %q; want bound address -> inbound peer", conn.LocalAddr(), conn.RemoteAddr())
	}
	if _, err := conn.Write([]byte("inbound")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len("inbound"))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "inbound" {
		t.Fatalf("echo = %q; want inbound", buffer)
	}
	closer, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("accepted conn must expose CloseWrite like the SSH channel conn")
	}
	if err := closer.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("Read after peer EOF = %v; want io.EOF", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := listener.Accept(); err == nil {
		t.Fatal("Accept after Close must fail")
	}
}

func TestClientListenRejected(t *testing.T) {
	client := pipeClient(t, func(conn net.Conn) {
		if _, _, err := ReadFrame(conn); err != nil {
			return
		}
		_ = WriteMessage(conn, FrameError, ErrorMsg{Code: CodePermission, Message: "listen denied"})
	})
	_, err := client.Listen(context.Background(), "tcp", "0.0.0.0:80")
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Listen = %v; want the coded fs.ErrPermission", err)
	}
}

// A cancel during the handshake closes the listen bridge, unblocking both
// the client and the peer.
func TestClientListenCancelUnblocksHandshake(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close() })
	received := make(chan struct{})
	go func() {
		defer func() { _ = serverConn.Close() }()
		if _, _, err := ReadFrame(serverConn); err != nil {
			return
		}
		close(received)
		_, _, _ = ReadFrame(serverConn)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	listenErr := make(chan error, 1)
	go func() {
		_, err := NewClient(clientConn).Listen(ctx, "tcp", "127.0.0.1:0")
		listenErr <- err
	}()
	<-received
	cancel()
	select {
	case err := <-listenErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Listen = %v; want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not unblock the listen handshake")
	}
}
