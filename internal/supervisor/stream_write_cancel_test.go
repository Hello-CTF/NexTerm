package supervisor

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type blockedInputConn struct {
	net.Conn
	mu      sync.Mutex
	armed   bool
	entered chan struct{}
}

func (c *blockedInputConn) Write(data []byte) (int, error) {
	c.mu.Lock()
	if c.armed {
		c.armed = false
		close(c.entered)
	}
	c.mu.Unlock()
	return c.Conn.Write(data)
}

func TestStreamWriteContextCancelsBlockedFrameWrite(t *testing.T) {
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
	conn := &blockedInputConn{Conn: local, entered: make(chan struct{})}
	serverDone := make(chan error, 1)
	go func() {
		for _, reply := range []struct {
			kind frameType
			body string
		}{
			{frameHelloAck, `{"version":2}`},
			{frameAttached, `{"id":"terminal","incarnation":"episode","cols":80,"rows":24}`},
		} {
			if _, _, err := readFrame(peer); err != nil {
				serverDone <- err
				return
			}
			if err := writeFrame(peer, reply.kind, []byte(reply.body)); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()
	client := NewRemoteClient(func(context.Context) (net.Conn, error) { return conn, nil }, "digest")
	stream, err := client.Attach(context.Background(), "terminal", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	conn.mu.Lock()
	conn.armed = true
	conn.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := stream.WriteContext(ctx, []byte("blocked input"))
		done <- err
	}()
	select {
	case <-conn.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("input did not reach the blocked connection")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked WriteContext error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not unblock the frame write")
	}
	if _, err := stream.WriteContext(context.Background(), []byte("again")); !errors.Is(err, context.Canceled) {
		t.Fatalf("subsequent write lost cancellation evidence: %v", err)
	}
	if err := stream.Close(); !errors.Is(err, context.Canceled) {
		t.Fatalf("close lost cancellation evidence: %v", err)
	}
}
