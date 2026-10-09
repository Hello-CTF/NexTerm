//go:build unix

package supervisor

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func testServer(t *testing.T, supervisor *Supervisor) *Server {
	t.Helper()
	runDir, err := os.MkdirTemp("/tmp", "nx-supervisor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	server, err := NewServer(supervisor, filepath.Join(runDir, "supervisor.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	return server
}

func TestIPCEndToEnd(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()

	script := `printf 'ready\n'
while IFS= read -r line; do
  case "$line" in
    size) stty size ;;
    quit*) exit "${line#quit}" ;;
    *) printf 'in:%s\n' "$line" ;;
  esac
done`
	id := ids.New()
	info, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", script},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != id || info.Dead {
		t.Fatalf("created info = %+v", info)
	}

	stream, err := client.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if stream.Identity().Incarnation != info.Incarnation {
		t.Fatalf("stream identity = %+v, want %+v", stream.Identity(), info.Incarnation)
	}
	readUntil(t, stream, "ready")

	if _, err := stream.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, stream, "in:hello")

	if err := stream.Resize(ctx, 100, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("size\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, stream, "30 100")

	if event, grid, err := stream.Versions(); err != nil || event != 0 || grid != 0 {
		t.Fatalf("fresh versions = %d/%d, %v", event, grid, err)
	}
	if err := stream.PersistVersions(4, 2); err != nil {
		t.Fatal(err)
	}
	if err := stream.PersistVersions(2, 6); err != nil {
		t.Fatal(err)
	}
	if event, grid, err := stream.Versions(); err != nil || event != 4 || grid != 6 {
		t.Fatalf("versions = %d/%d, %v, want floor 4/6", event, grid, err)
	}

	if _, err := stream.Write([]byte("quit7\n")); err != nil {
		t.Fatal(err)
	}
	err = stream.Wait(ctx)
	var exitErr *base.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 {
		t.Fatalf("Wait = %v, want exit code 7", err)
	}
	output := string(readToEOF(t, stream))
	if !strings.Contains(output, "quit7") {
		t.Fatalf("stream output = %q", output)
	}

	if _, err := stream.Write([]byte("more\n")); !errors.Is(err, ErrExited) {
		t.Fatalf("Write after exit = %v, want ErrExited", err)
	}
	if event, grid, err := stream.Versions(); err != nil || event != 4 || grid != 6 {
		t.Fatalf("Versions after failed Write = %d/%d, %v, want stream to stay usable", event, grid, err)
	}

	infos, err := client.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != id || !infos[0].Dead || infos[0].ExitCode == nil || *infos[0].ExitCode != 7 {
		t.Fatalf("listed infos = %+v", infos)
	}
}

func TestIPCReattachReplayWithoutDuplication(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()

	script := `i=0; while [ "$i" -lt 20 ]; do printf 'tick:%02d\n' "$i"; i=$((i+1)); sleep 0.03; done`
	id := ids.New()
	if _, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", script},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	}); err != nil {
		t.Fatal(err)
	}

	first, err := client.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, first, "tick:05")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)
	second, err := client.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	output := string(readToEOF(t, second))
	markers := make([]string, 0, 20)
	for index := 0; index < 20; index++ {
		markers = append(markers, fmt.Sprintf("tick:%02d", index))
	}
	assertOrderedOnce(t, output, markers...)

	recording, err := os.ReadFile(recordingPath(supervisor.StateDir(), id))
	if err != nil {
		t.Fatal(err)
	}
	if string(recording) != output {
		t.Fatalf("wire replay diverges from recording:\nreplay: %q\nrecord: %q", output, recording)
	}
}

func TestIPCOutputContinuesAfterClientDisconnect(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()

	id := ids.New()
	if _, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", `printf 'boot\n'; sleep 0.3; printf 'detached-marker\n'; sleep 0.3; printf 'late-marker\n'; sleep 30`},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	}); err != nil {
		t.Fatal(err)
	}

	stream, err := client.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, stream, "boot")
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}

	time.Sleep(800 * time.Millisecond)
	reattached, err := client.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	output := string(readUntil(t, reattached, "late-marker"))
	assertOrderedOnce(t, output, "boot", "detached-marker", "late-marker")
	_ = reattached.Close()

	if err := supervisor.Kill(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func TestIPCIdentityOverWire(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()

	id := ids.New()
	info, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "sleep 5"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stale := Identity{CreatedAt: info.CreatedAt, Incarnation: info.Incarnation}
	if err := supervisor.Kill(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "sleep 5"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := client.Attach(ctx, id, &stale); !errors.Is(err, ErrIdentity) {
		t.Fatalf("attach with stale identity = %v, want ErrIdentity", err)
	}
	if _, err := client.Attach(ctx, id, nil); err != nil {
		t.Fatalf("attach with current identity = %v", err)
	}
	if _, err := client.Attach(ctx, ids.New(), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attach unknown = %v, want ErrNotFound", err)
	}
}

func TestIPCKillOverWire(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()

	id := ids.New()
	if _, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", `printf 'kill-me\n'; sleep 30`},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	}); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, stream, "kill-me")
	if err := stream.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(sessionDir(supervisor.StateDir(), id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifacts remain after wire kill: %v", err)
	}
	if _, err := client.Attach(ctx, id, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attach after kill = %v, want ErrNotFound", err)
	}
}

func TestIPCMalformedRequests(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	ctx := context.Background()

	dial := func(t *testing.T) net.Conn {
		t.Helper()
		conn, err := net.DialTimeout("unix", server.SocketPath(), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}

	t.Run("garbage bytes", func(t *testing.T) {
		conn := dial(t)
		if _, err := conn.Write([]byte{0xff, 0xff, 0xff, 0xff, 0x01}); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Read(make([]byte, 16)); err == nil {
			t.Fatal("server must close the connection on garbage")
		}
	})

	t.Run("oversize frame", func(t *testing.T) {
		conn := dial(t)
		header := make([]byte, 5)
		binary.BigEndian.PutUint32(header, maxFramePayload+1)
		header[4] = byte(frameInput)
		if _, err := conn.Write(header); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Read(make([]byte, 16)); err == nil {
			t.Fatal("server must reject oversize frames")
		}
	})

	t.Run("version mismatch", func(t *testing.T) {
		conn := dial(t)
		payload, _ := marshalFrame(frameHello, helloMsg{Version: ProtocolVersion + 1})
		if err := writeFrame(conn, frameHello, payload); err != nil {
			t.Fatal(err)
		}
		kind, response := readErrorFrame(t, conn)
		if kind != frameError {
			t.Fatalf("response frame = %d, want error", kind)
		}
		var msg errorMsg
		if err := unmarshalFrame(response, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Code != codeVersionMismatch {
			t.Fatalf("error code = %q, want %q", msg.Code, codeVersionMismatch)
		}
	})

	t.Run("hello not first", func(t *testing.T) {
		conn := dial(t)
		payload, _ := marshalFrame(frameList, struct{}{})
		if err := writeFrame(conn, frameList, payload); err != nil {
			t.Fatal(err)
		}
		kind, response := readErrorFrame(t, conn)
		if kind != frameError {
			t.Fatalf("response frame = %d, want error", kind)
		}
		var msg errorMsg
		if err := unmarshalFrame(response, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Code != codeProtocol {
			t.Fatalf("error code = %q, want %q", msg.Code, codeProtocol)
		}
	})

	t.Run("unknown frame type", func(t *testing.T) {
		conn := dial(t)
		handshakeConn(t, conn, supervisor.StateDir())
		if err := writeFrame(conn, frameType(200), []byte("{}")); err != nil {
			t.Fatal(err)
		}
		kind, _ := readErrorFrame(t, conn)
		if kind != frameError {
			t.Fatalf("response frame = %d, want error", kind)
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		conn := dial(t)
		handshakeConn(t, conn, supervisor.StateDir())
		if err := writeFrame(conn, frameCreate, []byte("{not-json")); err != nil {
			t.Fatal(err)
		}
		kind, _ := readErrorFrame(t, conn)
		if kind != frameError {
			t.Fatalf("response frame = %d, want error", kind)
		}
	})

	client := testClient(t, server)
	if _, err := client.List(ctx); err != nil {
		t.Fatalf("server must survive malformed requests: %v", err)
	}
}

func TestIPCShutdownClosesConnections(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	client := testClient(t, server)
	ctx := context.Background()

	id := ids.New()
	if _, err := client.Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "sleep 30"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	}); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Attach(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}

	if _, err := stream.Read(make([]byte, 16)); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("Read after shutdown = %v, want disconnect error", err)
	}
	if _, err := client.List(ctx); err == nil {
		t.Fatal("dial after shutdown must fail")
	}
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("second shutdown = %v, want nil", err)
	}

	infos, err := supervisor.List(ctx)
	if err != nil || len(infos) != 1 || infos[0].Dead {
		t.Fatalf("supervisor must stay usable after server shutdown: %+v, %v", infos, err)
	}
}

func TestIPCStaleSocketRecovery(t *testing.T) {
	supervisor := testSupervisor(t)
	runDir, err := os.MkdirTemp("/tmp", "nx-supervisor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	socketPath := filepath.Join(runDir, "supervisor.sock")
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socketPath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	server, err := NewServer(supervisor, socketPath)
	if err != nil {
		t.Fatalf("stale socket must be replaced: %v", err)
	}
	if _, err := NewServer(supervisor, socketPath); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second server on live socket = %v, want ErrAlreadyExists", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket file must be removed on shutdown: %v", err)
	}

	restarted, err := NewServer(supervisor, socketPath)
	if err != nil {
		t.Fatalf("server restart after shutdown = %v", err)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = restarted.Shutdown(shutdownCtx)
	}()
	if _, err := testClient(t, restarted).List(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPeerUIDMatchesCurrentUser(t *testing.T) {
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "peer.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan *net.UnixConn, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		accepted <- conn.(*net.UnixConn)
	}()
	conn, err := net.DialTimeout("unix", listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	var server *net.UnixConn
	select {
	case server = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("accept timed out")
	}
	defer func() { _ = server.Close() }()
	uid, err := peerUID(server)
	if err != nil {
		t.Fatal(err)
	}
	if uid != os.Geteuid() {
		t.Fatalf("peer uid = %d, want %d", uid, os.Geteuid())
	}
}

func handshakeConn(t *testing.T, conn net.Conn, stateDir string) {
	t.Helper()
	digest, err := stateDigestFor(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := marshalFrame(frameHello, helloMsg{Version: ProtocolVersion, StateDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(conn, frameHello, payload); err != nil {
		t.Fatal(err)
	}
	kind, _, err := readFrame(conn)
	if err != nil || kind != frameHelloAck {
		t.Fatalf("handshake = %d, %v", kind, err)
	}
}

func readErrorFrame(t *testing.T, conn net.Conn) (frameType, []byte) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, payload, err := readFrame(conn)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return kind, payload
}
