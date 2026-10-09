//go:build unix

package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
)

func fakeStreamServer(t *testing.T, stateDir string, handle func(conn net.Conn, kind frameType, payload []byte) bool) string {
	t.Helper()
	runDir, err := os.MkdirTemp("/tmp", "nx-sup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	path := filepath.Join(runDir, "supervisor.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	digest, err := stateDigestFor(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		kind, payload, err := readFrame(conn)
		if err != nil || kind != frameHello {
			return
		}
		var hello helloMsg
		if err := unmarshalFrame(payload, &hello); err != nil {
			return
		}
		if hello.Version != ProtocolVersion || hello.StateDigest != digest {
			_ = writeFrame(conn, frameError, []byte(`{"code":"state_mismatch","message":"wrong state"}`))
			return
		}
		if err := writeFrame(conn, frameHelloAck, []byte(`{"version":2}`)); err != nil {
			return
		}
		kind, payload, err = readFrame(conn)
		if err != nil || kind != frameAttach {
			return
		}
		attached, err := marshalFrame(frameAttached, Info{ID: ids.New(), CreatedAt: time.Now(), Incarnation: ids.New(), Cols: 80, Rows: 24})
		if err != nil {
			return
		}
		if err := writeFrame(conn, frameAttached, attached); err != nil {
			return
		}
		for {
			kind, payload, err = readFrame(conn)
			if err != nil {
				return
			}
			if !handle(conn, kind, payload) {
				return
			}
		}
	}()
	return path
}

func TestStreamCloseReturnsWhenServerStalls(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	path := fakeStreamServer(t, stateDir, func(conn net.Conn, kind frameType, payload []byte) bool {
		return true
	})
	client := NewClient(path, stateDir)
	stream, err := client.Attach(context.Background(), ids.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	writeErr := make(chan error, 1)
	go func() {
		_, err := stream.Write([]byte("stalled"))
		writeErr <- err
	}()
	time.Sleep(100 * time.Millisecond)
	original := streamCloseDrainTimeout
	streamCloseDrainTimeout = 500 * time.Millisecond
	defer func() { streamCloseDrainTimeout = original }()
	closed := make(chan struct{})
	go func() {
		_ = stream.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close hung on a stalling server")
	}
	select {
	case err := <-writeErr:
		if err == nil {
			t.Fatal("Write succeeded against a stalling server")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight Write never woke after Close")
	}
}

func TestStreamCloseDrainsInflightKill(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	path := fakeStreamServer(t, stateDir, func(conn net.Conn, kind frameType, payload []byte) bool {
		if kind == frameKill {
			time.Sleep(150 * time.Millisecond)
			_ = writeFrame(conn, frameOK, []byte("{}"))
		}
		return true
	})
	client := NewClient(path, stateDir)
	stream, err := client.Attach(context.Background(), ids.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	killErr := make(chan error, 1)
	go func() {
		killErr <- stream.Kill(context.Background())
	}()
	time.Sleep(10 * time.Millisecond)
	closeErr := make(chan error, 1)
	go func() {
		closeErr <- stream.Close()
	}()
	select {
	case err := <-killErr:
		if err != nil {
			t.Fatalf("Kill lost its reply to a concurrent Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Kill never returned")
	}
	select {
	case err := <-closeErr:
		if err != nil {
			t.Fatalf("Close = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close never returned")
	}
}

func TestServerRejectsWrongStateDigest(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	conn, err := net.DialTimeout("unix", server.SocketPath(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	payload, err := marshalFrame(frameHello, helloMsg{Version: ProtocolVersion})
	if err != nil {
		t.Fatal(err)
	}
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
	if msg.Code != codeStateMismatch {
		t.Fatalf("error code = %q, want %q", msg.Code, codeStateMismatch)
	}
}

func TestConnectHelperRejectsStateCollision(t *testing.T) {
	stateDirA := filepath.Join(t.TempDir(), "state-a")
	stateDirB := filepath.Join(t.TempDir(), "state-b")
	supervisorA, err := New(Config{StateDir: stateDirA, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisorA.Close() })
	endpointB, err := helperEndpoint(stateDirB)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(endpointB), 0o700); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(supervisorA, endpointB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	_, err = ConnectHelper(context.Background(), HelperConfig{StateDir: stateDirB, SpawnTimeout: 5 * time.Second})
	if err == nil {
		t.Fatal("connect to a helper owning a different state directory succeeded")
	}
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("connect error = %v, want ErrStateMismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(stateDirB, "helper.log")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state collision spawned a helper (log present): %v", statErr)
	}
}

func TestConnectHelperDistinctStateDirsStayIsolated(t *testing.T) {
	stateDirA := filepath.Join(t.TempDir(), "state-a")
	stateDirB := filepath.Join(t.TempDir(), "state-b")
	t.Cleanup(func() {
		killHelperProcesses(t, stateDirA)
		killHelperProcesses(t, stateDirB)
	})
	ctx := context.Background()
	helperA, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDirA, SpawnTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	helperB, err := ConnectHelper(ctx, HelperConfig{StateDir: stateDirB, SpawnTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if helperA.Endpoint() == helperB.Endpoint() {
		t.Fatalf("distinct state dirs share endpoint %s", helperA.Endpoint())
	}
	id := ids.New()
	if _, err := helperA.Client().Create(ctx, CreateOptions{
		ID:      id,
		Command: []string{"/bin/sh", "-c", "sleep 60"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
	}); err != nil {
		t.Fatal(err)
	}
	infosA, err := helperA.Client().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	infosB, err := helperB.Client().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infosA) != 1 || infosA[0].ID != id {
		t.Fatalf("helper A sessions = %+v", infosA)
	}
	if len(infosB) != 0 {
		t.Fatalf("helper B sees helper A sessions: %+v", infosB)
	}
	if err := helperA.Client().Kill(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
}
