package ssh

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestSSHHeartbeatRoundTripsAndReportsClosedConnection(t *testing.T) {
	server := newTestSSHServer(t, nil)
	cfg := testClientConfig(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
	// Keep the background keepalive out of the count.
	cfg.KeepAliveInterval = time.Hour
	client, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var heartbeater base.Heartbeater = client
	before := heartbeater.InboundBytes()
	if before == 0 {
		t.Fatal("inbound byte count is zero after the handshake")
	}
	if err := heartbeater.Heartbeat(context.Background()); err != nil {
		t.Fatalf("heartbeat on a live connection = %v", err)
	}
	if after := heartbeater.InboundBytes(); after <= before {
		t.Fatalf("inbound byte count after a reply = %d, want more than %d", after, before)
	}
	if got := server.keepalives.Load(); got != 1 {
		t.Fatalf("server keepalive requests = %d, want 1", got)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := heartbeater.Heartbeat(context.Background()); !errors.Is(err, base.ErrDisconnected) {
		t.Fatalf("heartbeat after close = %v, want ErrDisconnected", err)
	}
}
