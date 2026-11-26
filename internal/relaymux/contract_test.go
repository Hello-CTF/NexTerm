package relaymux

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Every carrier runs the same contract: the fleet server and the agent runtime depend only on Session.
func TestCarrierContract(t *testing.T) {
	for _, c := range carriers {
		t.Run(c.name, func(t *testing.T) {
			t.Run("AcceptWithoutData", func(t *testing.T) { testAcceptWithoutData(t, c) })
			t.Run("Echo", func(t *testing.T) { testEcho(t, c) })
			t.Run("HalfClose", func(t *testing.T) { testHalfClose(t, c) })
			t.Run("CloseEndsLocalRead", func(t *testing.T) { testCloseEndsLocalRead(t, c) })
			t.Run("ConcurrentStreams", func(t *testing.T) { testConcurrentStreams(t, c) })
			t.Run("SessionClose", func(t *testing.T) { testSessionClose(t, c) })
			t.Run("Backpressure", func(t *testing.T) { testBackpressure(t, c) })
			t.Run("Handshake", func(t *testing.T) { testHandshake(t, c) })
			t.Run("Goaway", func(t *testing.T) { testGoaway(t, c) })
			t.Run("Kind", func(t *testing.T) {
				client, server := c.pair(t)
				if client.Kind() != c.name || server.Kind() != c.name {
					t.Fatalf("kinds = %s/%s, want %s", client.Kind(), server.Kind(), c.name)
				}
			})
		})
	}
}

func acceptWithin(t testing.TB, session Session) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := session.AcceptStream(ctx)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	return conn
}

func openStream(t testing.TB, session Session) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := session.OpenStream(ctx)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return conn
}

// A stream reaches the peer's AcceptStream before any application byte, in both directions.
func testAcceptWithoutData(t *testing.T, c carrier) {
	client, server := c.pair(t)
	for _, direction := range [][2]Session{{client, server}, {server, client}} {
		opened := openStream(t, direction[0])
		accepted := acceptWithin(t, direction[1])
		if _, err := accepted.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, 1)
		if _, err := io.ReadFull(opened, buffer); err != nil || buffer[0] != 'x' {
			t.Fatalf("read = %q, %v", buffer, err)
		}
		_ = opened.Close()
		_ = accepted.Close()
	}
}

func testEcho(t *testing.T, c carrier) {
	client, server := c.pair(t)
	go func() {
		conn := acceptWithin(t, server)
		_, _ = io.Copy(conn, conn)
		_ = CloseWrite(conn)
	}()
	conn := openStream(t, client)
	payload := make([]byte, 3<<20)
	_, _ = rand.Read(payload)
	go func() {
		_, _ = conn.Write(payload)
		_ = CloseWrite(conn)
	}()
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo returned %d bytes, want %d identical", len(got), len(payload))
	}
	_ = conn.Close()
}

// CloseWrite is a half close: the peer reads EOF and can still answer.
func testHalfClose(t *testing.T, c carrier) {
	client, server := c.pair(t)
	conn := openStream(t, client)
	if _, err := conn.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}
	if err := CloseWrite(conn); err != nil {
		t.Fatal(err)
	}
	peer := acceptWithin(t, server)
	got, err := io.ReadAll(peer)
	if err != nil || string(got) != "request" {
		t.Fatalf("peer read %q, %v", got, err)
	}
	if _, err := peer.Write([]byte("response")); err != nil {
		t.Fatal(err)
	}
	_ = CloseWrite(peer)
	got, err = io.ReadAll(conn)
	if err != nil || string(got) != "response" {
		t.Fatalf("read %q, %v", got, err)
	}
	_ = conn.Close()
	_ = peer.Close()
}

// Close ends a Read blocked on the local side and the peer reads EOF.
func testCloseEndsLocalRead(t *testing.T, c carrier) {
	client, server := c.pair(t)
	conn := openStream(t, client)
	peer := acceptWithin(t, server)
	readErr := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 16))
		readErr <- err
	}()
	// The read is blocked until Close: there is nothing to read.
	_ = conn.Close()
	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("read after Close returned no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not end the local Read")
	}
	if _, err := conn.Write([]byte("x")); err == nil {
		t.Fatal("write after Close succeeded")
	}
	_ = peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := peer.Read(make([]byte, 16)); !errors.Is(err, io.EOF) {
		t.Fatalf("peer read = %v, want EOF", err)
	}
	_ = peer.Close()
}

func testConcurrentStreams(t *testing.T, c carrier) {
	client, server := c.pair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			conn, err := server.AcceptStream(ctx)
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(conn, conn)
				_ = CloseWrite(conn)
			}()
		}
	}()
	const streams = 200
	var wg sync.WaitGroup
	errs := make(chan error, streams)
	for i := range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := client.OpenStream(ctx)
			if err != nil {
				errs <- err
				return
			}
			defer conn.Close()
			payload := bytes.Repeat([]byte(fmt.Sprintf("stream-%03d|", i)), 2000)
			go func() {
				_, _ = conn.Write(payload)
				_ = CloseWrite(conn)
			}()
			got, err := io.ReadAll(conn)
			if err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(got, payload) {
				errs <- fmt.Errorf("stream %d: %d bytes back, want %d", i, len(got), len(payload))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// Closing the session ends both sides and every stream on them.
func testSessionClose(t *testing.T, c carrier) {
	client, server := c.pair(t)
	conn := openStream(t, client)
	peer := acceptWithin(t, server)
	_ = client.Close()
	for _, session := range []Session{client, server} {
		select {
		case <-session.Done():
		case <-time.After(IdleTimeout):
			t.Fatal("session not done after Close")
		}
	}
	if _, err := client.OpenStream(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("open after close = %v, want ErrClosed", err)
	}
	if _, err := server.AcceptStream(context.Background()); err == nil {
		t.Fatal("accept after peer close succeeded")
	}
	_ = peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("read on a stream of a closed session succeeded")
	}
	_ = conn.Close()
}

// A writer whose peer stops reading blocks after a bounded amount: the carriers' flow control reaches it.
func testBackpressure(t *testing.T, c carrier) {
	client, server := c.pair(t)
	conn := openStream(t, client)
	peer := acceptWithin(t, server)
	const total = 64 << 20
	// The QUIC stream window or the yamux window plus the websocket and socket buffers of one stream.
	const bound = 2*StreamWindow + (8 << 20)
	var written atomic.Int64
	writeDone := make(chan error, 1)
	go func() {
		chunk := make([]byte, 32<<10)
		for written.Load() < total {
			n, err := conn.Write(chunk)
			written.Add(int64(n))
			if err != nil {
				writeDone <- err
				return
			}
		}
		writeDone <- nil
	}()
	// Wait for the writer to stall: the count stays put across a quiet period.
	last, stable := int64(-1), 0
	for deadline := time.Now().Add(20 * time.Second); stable < 5; {
		if time.Now().After(deadline) {
			t.Fatalf("writer never stalled (%d bytes written)", written.Load())
		}
		time.Sleep(50 * time.Millisecond)
		if current := written.Load(); current == last {
			stable++
		} else {
			last, stable = current, 0
		}
	}
	select {
	case err := <-writeDone:
		t.Fatalf("writer finished without a reader: %v", err)
	default:
	}
	if last > bound {
		t.Fatalf("writer got %d bytes ahead of a stopped reader, bound %d", last, bound)
	}
	// Draining the reader releases the writer and every byte arrives.
	go func() {
		<-writeDone
		_ = CloseWrite(conn)
	}()
	n, err := io.Copy(io.Discard, peer)
	if err != nil || n != total {
		t.Fatalf("drained %d bytes, %v; want %d", n, err, int64(total))
	}
}

func testHandshake(t *testing.T, c carrier) {
	for _, refuse := range []bool{false, true} {
		client, server := c.pair(t)
		serverDone := make(chan *Link, 1)
		go func() {
			pending, err := AcceptHandshake(context.Background(), server)
			if err != nil {
				serverDone <- nil
				return
			}
			if pending.Hello.Role != RoleAgent || pending.Hello.DeviceID != "device-1" || pending.Hello.Version != Version {
				pending.Refuse(Reply{Status: http.StatusBadRequest, Message: "bad hello"})
				serverDone <- nil
				return
			}
			if refuse {
				pending.Refuse(Reply{Status: http.StatusForbidden, Code: "forbidden", Message: "设备凭据无效"})
				serverDone <- nil
				return
			}
			link, err := pending.Accept(Reply{ServerVersion: "9.9.9"})
			if err != nil {
				serverDone <- nil
				return
			}
			serverDone <- link
		}()
		link, err := Handshake(context.Background(), client, Hello{Role: RoleAgent, DeviceID: "device-1", Secret: "s"})
		serverLink := <-serverDone
		if refuse {
			reply, ok := Refused(err)
			if !ok || reply.Status != http.StatusForbidden || reply.Code != "forbidden" {
				t.Fatalf("refused handshake = %v", err)
			}
			select {
			case <-client.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("a refused session stays open")
			}
			continue
		}
		if err != nil || serverLink == nil {
			t.Fatalf("handshake = %v", err)
		}
		if link.HelloReply().ServerVersion != "9.9.9" {
			t.Fatalf("hello reply = %+v", link.HelloReply())
		}
		// The server opens a device stream to the dialer and the dialer serves it.
		go func() {
			_ = link.ServeStreams(context.Background(), func(_ context.Context, incoming *IncomingStream) {
				if incoming.Header.Purpose != "terminal" {
					incoming.Refuse(Reply{Status: http.StatusForbidden, Code: "forbidden", Message: "unsupported"})
					return
				}
				conn, err := incoming.Accept(map[string]string{ParamStream: "1"})
				if err != nil {
					return
				}
				_, _ = io.Copy(conn, conn)
				_ = CloseWrite(conn)
			})
		}()
		conn, reply, err := serverLink.OpenDeviceStream(context.Background(), StreamHeader{Purpose: "terminal", RequestID: "r1"})
		if err != nil || reply.Params[ParamStream] != "1" {
			t.Fatalf("open device stream = %+v, %v", reply, err)
		}
		_, _ = conn.Write([]byte("ping"))
		_ = CloseWrite(conn)
		got, _ := io.ReadAll(conn)
		if string(got) != "ping" {
			t.Fatalf("echo = %q", got)
		}
		_ = conn.Close()
		_, _, err = serverLink.OpenDeviceStream(context.Background(), StreamHeader{Purpose: "file"})
		if reply, ok := Refused(err); !ok || reply.Status != http.StatusForbidden {
			t.Fatalf("refused stream = %v", err)
		}
	}
}

// A goaway on the control stream ends the session and its reason reaches the dialer.
func testGoaway(t *testing.T, c carrier) {
	client, server := c.pair(t)
	serverLink := make(chan *Link, 1)
	go func() {
		pending, err := AcceptHandshake(context.Background(), server)
		if err != nil {
			serverLink <- nil
			return
		}
		link, _ := pending.Accept(Reply{})
		serverLink <- link
	}()
	link, err := Handshake(context.Background(), client, Hello{Role: RoleClient, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	accepted := <-serverLink
	if accepted == nil {
		t.Fatal("server handshake failed")
	}
	accepted.Goaway(Reply{Status: http.StatusUnauthorized, Code: "forbidden", Message: "会话无效或缺失，请重新登录"})
	select {
	case <-link.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("goaway did not end the session")
	}
	reply, ok := Refused(link.Err())
	if !ok || reply.Status != http.StatusUnauthorized {
		t.Fatalf("link error = %v", link.Err())
	}
}
