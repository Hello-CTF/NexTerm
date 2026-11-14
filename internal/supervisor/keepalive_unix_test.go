//go:build unix

package supervisor

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func shortKeepalive(t *testing.T, timeout time.Duration) {
	t.Helper()
	previous := keepaliveTimeout
	keepaliveTimeout = timeout
	t.Cleanup(func() { keepaliveTimeout = previous })
}

func attachedCount(session *Session) int {
	session.mu.RLock()
	defer session.mu.RUnlock()
	return len(session.attachments)
}

// silentAttach says hello (promising keepalives or not) and attaches to id
// without ever sending another frame, like a client whose network vanished.
func silentAttach(t *testing.T, server *Server, id string, keepalive bool) (net.Conn, helloMsg) {
	t.Helper()
	conn, err := net.Dial("unix", server.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	digest, err := stateDigestFor(server.supervisor.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := marshalFrame(frameHello, helloMsg{Version: ProtocolVersion, StateDigest: digest, Keepalive: keepalive})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(conn, frameHello, payload); err != nil {
		t.Fatal(err)
	}
	kind, reply, err := readFrame(conn)
	if err != nil || kind != frameHelloAck {
		t.Fatalf("hello reply = %v, %v", kind, err)
	}
	var ack helloMsg
	if err := unmarshalFrame(reply, &ack); err != nil {
		t.Fatal(err)
	}
	payload, err = marshalFrame(frameAttach, attachMsg{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(conn, frameAttach, payload); err != nil {
		t.Fatal(err)
	}
	if kind, _, err := readFrame(conn); err != nil || kind != frameAttached {
		t.Fatalf("attach reply = %v, %v", kind, err)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, ack
}

func waitAttached(t *testing.T, session *Session, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for attachedCount(session) != want {
		if time.Now().After(deadline) {
			t.Fatalf("attachments = %d, want %d", attachedCount(session), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestServerDropsSilentConnectionThatPromisedKeepalive(t *testing.T) {
	shortKeepalive(t, 200*time.Millisecond)
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	session := testScriptSession(t, supervisor, "printf 'ready\\n'; exec cat")

	conn, ack := silentAttach(t, server, session.ID(), true)
	if ack.KeepaliveMS != 200 {
		t.Fatalf("hello ack keepalive = %d ms, want 200", ack.KeepaliveMS)
	}
	waitAttached(t, session, 1)
	// The helper closes the connection, so its reads end instead of timing out.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, err := io.Copy(io.Discard, conn)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatal("helper kept a silent connection that promised keepalives")
	}
	waitAttached(t, session, 0)
}

func TestServerKeepsSilentConnectionWithoutKeepalivePromise(t *testing.T) {
	shortKeepalive(t, 100*time.Millisecond)
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	session := testScriptSession(t, supervisor, "printf 'ready\\n'; exec cat")

	conn, _ := silentAttach(t, server, session.ID(), false)
	time.Sleep(time.Second)
	if got := attachedCount(session); got != 1 {
		t.Fatalf("attachments of a client without keepalives = %d, want 1", got)
	}
	// The connection still serves requests.
	payload, err := marshalFrame(frameList, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(conn, frameList, payload); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		kind, _, err := readFrame(conn)
		if err != nil {
			t.Fatalf("connection without keepalives was dropped: %v", err)
		}
		if kind == frameListed {
			return
		}
	}
}

func TestClientKeepsQuietStreamAndExecAlive(t *testing.T) {
	shortKeepalive(t, 150*time.Millisecond)
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	session := testScriptSession(t, supervisor, "printf 'ready\\n'; while IFS= read -r line; do printf 'in:%s\\n' \"$line\"; done")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stream, err := client.Attach(ctx, session.ID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	readUntil(t, stream, "ready")
	time.Sleep(time.Second)
	if got := attachedCount(session); got != 1 {
		t.Fatalf("attachments after a quiet second = %d, want 1", got)
	}
	if _, err := stream.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, stream, "in:ping")

	stdout, _, code, _, err := client.Exec(ctx, ExecSpec{Command: []string{"/bin/sh", "-c", "sleep 1; printf done"}})
	if err != nil || code != 0 || string(stdout) != "done" {
		t.Fatalf("quiet exec = %q, %d, %v", stdout, code, err)
	}
}
