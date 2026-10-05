//go:build unix

package supervisor

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRemoteClientEndToEnd(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	digest, err := stateDigestFor(supervisor.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	dial := func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", server.SocketPath())
	}
	client := NewRemoteClient(dial, digest)
	ctx := context.Background()
	created, err := client.Create(ctx, CreateOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.List(ctx); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Attach(ctx, created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Write([]byte("echo remote-42\n")); err != nil {
		t.Fatal(err)
	}
	output := readUntil(t, stream, "remote-42")
	if len(output) == 0 {
		t.Fatal("no output through the remote client")
	}
	killCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := stream.Kill(killCtx); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteClientDigestMismatch(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	dial := func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", server.SocketPath())
	}
	client := NewRemoteClient(dial, "deadbeef")
	if _, err := client.List(context.Background()); !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("List error = %v; want ErrStateMismatch", err)
	}
}

func TestRemoteClientProtocolMismatch(t *testing.T) {
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "mismatch.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, err := readFrame(conn); err != nil {
			return
		}
		_ = writeFrame(conn, frameError, []byte(`{"code":"version_mismatch","message":"protocol 99"}`))
	}()
	dial := func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", listener.Addr().String())
	}
	client := NewRemoteClient(dial, "irrelevant")
	if _, err := client.List(context.Background()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("List error = %v; want ErrProtocol", err)
	}
}

func TestBridgeIO(t *testing.T) {
	bridge, peer := net.Pipe()
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- bridgeIO(stdinReader, stdoutWriter, bridge) }()

	if _, err := peer.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(stdoutReader, buffer); err != nil || string(buffer) != "ping" {
		t.Fatalf("stdout read = %q, %v; want ping", buffer, err)
	}
	if _, err := stdinWriter.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(peer, buffer); err != nil || string(buffer) != "pong" {
		t.Fatalf("peer read = %q, %v; want pong", buffer, err)
	}
	_ = peer.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("bridgeIO: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bridgeIO did not return after the peer closed")
	}
}

func TestParseHelperArgsBridge(t *testing.T) {
	config, err := parseHelperArgs([]string{"--bridge", "--state-dir", "/tmp/x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Bridge || config.StateDir != "/tmp/x" {
		t.Fatalf("config = %+v", config)
	}
	if _, err := parseHelperArgs([]string{"--bridge=yes"}, nil); err == nil {
		t.Fatal("expected --bridge=yes to be rejected")
	}
	if _, err := parseHelperArgs([]string{"--bridge", "--bogus"}, nil); err == nil {
		t.Fatal("expected unknown option to be rejected")
	}
	config, err = parseHelperArgs([]string{"--data-dir", "/data"}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if config.Bridge || config.StateDir != filepath.Join("/data", "durable", "supervisor") {
		t.Fatalf("config = %+v", config)
	}
}
