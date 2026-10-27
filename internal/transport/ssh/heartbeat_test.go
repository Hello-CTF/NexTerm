package ssh

import (
	"context"
	"errors"
	"io"
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

func newKeepaliveTestClient(t *testing.T, server *testSSHServer) *Client {
	t.Helper()
	// The background keepalive is disabled by the test config; tests drive
	// keepaliveLoop with their own tick channel.
	return connectTestClient(t, server, AuthConfig{Method: AuthPassword, Password: "secret"})
}

func runKeepaliveLoop(client *Client, ticks <-chan time.Time, maximum int) <-chan struct{} {
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		client.keepaliveLoop(ticks, maximum)
	}()
	return exited
}

func TestSSHKeepaliveClosesAfterMaxUnansweredSilentTicks(t *testing.T) {
	server := newTestSSHServer(t, nil)
	server.muteKeepalives.Store(true)
	client := newKeepaliveTestClient(t, server)
	ticks := make(chan time.Time)
	exited := runKeepaliveLoop(client, ticks, 3)
	// The first tick sends the request; each further tick counts one miss.
	for range 4 {
		ticks <- time.Time{}
	}
	<-exited
	if client.IsAlive() {
		t.Fatal("client stayed open after the keepalive went unanswered with no inbound traffic")
	}
}

func TestSSHKeepaliveStaysOpenWhileServerSendsData(t *testing.T) {
	server := newTestSSHServer(t, nil)
	server.muteKeepalives.Store(true)
	client := newKeepaliveTestClient(t, server)
	conn, err := client.OpenExecConn(context.Background(), "dial-stdio", base.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ticks := make(chan time.Time)
	exited := runKeepaliveLoop(client, ticks, 3)
	buffer := make([]byte, 1)
	for range 10 {
		// A completed echo round trip proves inbound bytes grew since the last tick.
		if _, err := conn.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(conn, buffer); err != nil {
			t.Fatal(err)
		}
		ticks <- time.Time{}
	}
	close(ticks)
	<-exited
	if !client.IsAlive() {
		t.Fatal("client closed although the server kept sending data")
	}
}
