package device

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestHeartbeatDialFailureMarksTransportDead(t *testing.T) {
	transport := New("dev", "digest", func(context.Context, string) (net.Conn, error) {
		return nil, errors.New("bridge unreachable")
	}, 1)
	if !transport.IsAlive() {
		t.Fatal("new transport must be alive")
	}
	err := transport.Heartbeat(context.Background())
	if !errors.Is(err, base.ErrDisconnected) {
		t.Fatalf("Heartbeat = %v, want base.ErrDisconnected", err)
	}
	if transport.IsAlive() {
		t.Fatal("transport must not be alive after a confirmed heartbeat failure")
	}
}

func TestHeartbeatTimeoutIsAMissNotAVerdict(t *testing.T) {
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
	transport := New("dev", "digest", func(context.Context, string) (net.Conn, error) { return local, nil }, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := transport.Heartbeat(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Heartbeat = %v, want context.DeadlineExceeded", err)
	}
	if !transport.IsAlive() {
		t.Fatal("a missed heartbeat must not mark the transport dead")
	}
}

func TestInboundBytesCountsBridgeReads(t *testing.T) {
	local, peer := net.Pipe()
	t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
	transport := New("dev", "digest", nil, 1)
	conn := countingConn{Conn: local, counter: &transport.inbound}
	go func() { _, _ = peer.Write([]byte("abc")) }()
	buf := make([]byte, 8)
	n, err := conn.Read(buf)
	if err != nil || n != 3 {
		t.Fatalf("Read = %d, %v", n, err)
	}
	if got := transport.InboundBytes(); got != 3 {
		t.Fatalf("InboundBytes = %d, want 3", got)
	}
}
