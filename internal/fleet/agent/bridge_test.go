//go:build unix

package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/coder/websocket"
)

// TestBridgeHelperProcess re-executes the test binary as a real
// `supervisor-helper --bridge` subprocess when spawned by tests below.
func TestBridgeHelperProcess(t *testing.T) {
	if os.Getenv("GO_AGENT_BRIDGE_HELPER") != "1" {
		t.Skip("helper process")
	}
	directory := os.Getenv("GO_AGENT_BRIDGE_DIR")
	os.Exit(supervisor.RunHelperCLI([]string{"--bridge", "--state-dir", directory}))
}

func testBridgeSpawner(t *testing.T) BridgeSpawner {
	t.Helper()
	return func(ctx context.Context, stateDir string) (io.ReadWriteCloser, error) {
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=TestBridgeHelperProcess", "--")
		command.Env = append(os.Environ(),
			"GO_AGENT_BRIDGE_HELPER=1",
			"GO_AGENT_BRIDGE_DIR="+stateDir,
		)
		stdin, err := command.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := command.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := command.Start(); err != nil {
			return nil, err
		}
		return &subprocessPipe{stdin: stdin, stdout: stdout, command: command}, nil
	}
}

// wsStream adapts one server-side bridge websocket connection to the net.Conn
// the supervisor client expects. The supervisor client dials a fresh
// connection per RPC and closes it afterwards, mirroring how the SSH
// transport reaches remote helpers with one exec channel per RPC.
type wsStream struct {
	conn *websocket.Conn
	ctx  context.Context
	buf  []byte
}

func (s *wsStream) Read(buffer []byte) (int, error) {
	for len(s.buf) == 0 {
		_, message, err := s.conn.Read(s.ctx)
		if err != nil {
			return 0, err
		}
		s.buf = message
	}
	count := copy(buffer, s.buf)
	s.buf = s.buf[count:]
	return count, nil
}

func (s *wsStream) Write(buffer []byte) (int, error) {
	if err := s.conn.Write(s.ctx, websocket.MessageBinary, buffer); err != nil {
		return 0, err
	}
	return len(buffer), nil
}

func (s *wsStream) Close() error {
	return s.conn.Close(websocket.StatusNormalClosure, "")
}

func (s *wsStream) LocalAddr() net.Addr              { return wsAddr{} }
func (s *wsStream) RemoteAddr() net.Addr             { return wsAddr{} }
func (s *wsStream) SetDeadline(time.Time) error      { return nil }
func (s *wsStream) SetReadDeadline(time.Time) error  { return nil }
func (s *wsStream) SetWriteDeadline(time.Time) error { return nil }

type wsAddr struct{}

func (wsAddr) Network() string { return "ws" }
func (wsAddr) String() string  { return "ws" }

func testStateDir(t *testing.T) string {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return stateDir
}

func testSupervisorServer(t *testing.T, stateDir string) (*supervisor.Server, string) {
	t.Helper()
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := supervisor.HelperEndpoint(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	server, err := supervisor.NewServer(instance, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = instance.Close()
	})
	digest, err := supervisor.StateDigest(stateDir, strconv.Itoa(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	return server, digest
}

func readUntilMarker(t *testing.T, stream *supervisor.Stream, marker string) string {
	t.Helper()
	output, err := readUntilMarkerE(stream, marker, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func readUntilMarkerE(stream *supervisor.Stream, marker string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var output strings.Builder
	buffer := make([]byte, 4096)
	for time.Now().Before(deadline) {
		count, err := stream.Read(buffer)
		if count > 0 {
			output.Write(buffer[:count])
			if strings.Contains(output.String(), marker) {
				return output.String(), nil
			}
		}
		if err != nil {
			return "", fmt.Errorf("stream read: %w (output so far %q)", err, output.String())
		}
	}
	return "", fmt.Errorf("marker %q not seen, output %q", marker, output.String())
}

// bridgeHarness drives the agent-side bridge spawner against a real in-process
// supervisor server. Each opened pipe carries exactly one supervisor client
// connection, matching the one-bridge-per-RPC model of the SSH transport.
type bridgeHarness struct {
	t        *testing.T
	stateDir string
	digest   string
	accepted chan *websocket.Conn
	server   *wsFake
	counter  atomic.Int64
	ctx      context.Context
}

func newBridgeHarness(t *testing.T, ctx context.Context) *bridgeHarness {
	t.Helper()
	stateDir := testStateDir(t)
	_, digest := testSupervisorServer(t, stateDir)
	harness := &bridgeHarness{
		t:        t,
		stateDir: stateDir,
		digest:   digest,
		accepted: make(chan *websocket.Conn, 8),
		ctx:      ctx,
	}
	fake := newWSFake(t, func(conn *websocket.Conn, hello HelloMessage) error {
		if hello.BridgeID == "" {
			t.Errorf("bridge hello missing bridge id: %+v", hello)
		}
		if err := writeServerMessage(conn, serverMessage{Type: "hello_ok"}); err != nil {
			return err
		}
		harness.accepted <- conn
		return nil
	})
	harness.server = fake
	return harness
}

// openPipe dials one agent-side bridge connection and pipes it to a fresh
// supervisor-helper bridge subprocess; the returned stream is the server side.
func (h *bridgeHarness) openPipe() *wsStream {
	h.t.Helper()
	stream, _, _ := h.openPipeTracked()
	return stream
}

// openPipeTracked also returns a channel that closes when BridgePipe returns
// and the spawned subprocess pipe so tests can assert cleanup.
func (h *bridgeHarness) openPipeTracked() (*wsStream, chan struct{}, *subprocessPipe) {
	h.t.Helper()
	hello := testHello()
	hello.BridgeID = fmt.Sprintf("test-bridge-%d", h.counter.Add(1))
	agentConn, err := DialBridge(h.ctx, wsURL(h.server.server.URL), hello, false)
	if err != nil {
		h.t.Fatal(err)
	}
	spawned := make(chan *subprocessPipe, 1)
	spawner := func(ctx context.Context, stateDir string) (io.ReadWriteCloser, error) {
		pipe, err := testBridgeSpawner(h.t)(ctx, stateDir)
		if err != nil {
			return nil, err
		}
		spawned <- pipe.(*subprocessPipe)
		return pipe, nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = BridgePipe(h.ctx, agentConn, spawner, h.stateDir)
	}()
	return &wsStream{conn: <-h.accepted, ctx: h.ctx}, done, <-spawned
}

func (h *bridgeHarness) client() *supervisor.Client {
	return supervisor.NewRemoteClient(func(context.Context) (net.Conn, error) {
		return h.openPipe(), nil
	}, h.digest)
}

func TestBridgePipeCarriesSupervisorProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	harness := newBridgeHarness(t, ctx)

	created, err := harness.client().Create(ctx, supervisor.CreateOptions{
		Command: []string{"/bin/sh", "-c", "printf 'pipe-marker\\n'; sleep 30"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		Cols:    80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}

	attachment, err := harness.client().Attach(ctx, created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	readUntilMarker(t, attachment, "pipe-marker")
	if err := attachment.Close(); err != nil {
		t.Fatal(err)
	}
	killCtx, killCancel := context.WithTimeout(ctx, 10*time.Second)
	defer killCancel()
	if err := harness.client().Kill(killCtx, created.ID, nil); err != nil {
		t.Fatal(err)
	}
}

func TestBridgePipeCleansUpOnRemoteClose(t *testing.T) {
	stateDir := testStateDir(t)
	testSupervisorServer(t, stateDir)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	harness := newBridgeHarness(t, ctx)

	for round := 0; round < 3; round++ {
		stream, done, pipe := harness.openPipeTracked()
		client := supervisor.NewRemoteClient(func(context.Context) (net.Conn, error) { return stream, nil }, harness.digest)
		if _, err := client.List(ctx); err != nil {
			t.Fatalf("round %d: list over bridge: %v", round, err)
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("round %d: BridgePipe did not return after remote close while context is active", round)
		}
		if err := pipe.command.Process.Signal(syscall.Signal(0)); err == nil {
			t.Fatalf("round %d: bridge subprocess still running after cleanup", round)
		}
		if _, err := pipe.stdout.Read(make([]byte, 1)); err == nil {
			t.Fatalf("round %d: bridge pipe stdout still open", round)
		}
	}
}

func TestBridgeResumeKeepsSessionAcrossReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	harness := newBridgeHarness(t, ctx)

	created, err := harness.client().Create(ctx, supervisor.CreateOptions{
		Command: []string{"/bin/sh", "-c", "printf 'resume-marker\\n'; sleep 30"},
		Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
		Cols:    80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := &supervisor.Identity{CreatedAt: created.CreatedAt, Incarnation: created.Incarnation}

	attachment, err := harness.client().Attach(ctx, created.ID, identity)
	if err != nil {
		t.Fatalf("resume attach: %v", err)
	}
	readUntilMarker(t, attachment, "resume-marker")
	if err := attachment.Close(); err != nil {
		t.Fatal(err)
	}
	killCtx, killCancel := context.WithTimeout(ctx, 10*time.Second)
	defer killCancel()
	if err := harness.client().Kill(killCtx, created.ID, identity); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeBridgeEndToEnd(t *testing.T) {
	stateDir := testStateDir(t)
	_, digest := testSupervisorServer(t, stateDir)

	server := newFakeFleetServer(t)
	bridgeOutput := make(chan string, 1)
	var controlConn *websocket.Conn
	var created *supervisor.Info
	server.setWSControl(func(conn *websocket.Conn, hello HelloMessage) {
		switch hello.BridgeID {
		case "runtime-bridge-1":
			if err := writeServerMessage(conn, serverMessage{Type: "hello_ok"}); err != nil {
				t.Error(err)
				return
			}
			stream := &wsStream{conn: conn, ctx: context.Background()}
			client := supervisor.NewRemoteClient(func(context.Context) (net.Conn, error) { return stream, nil }, digest)
			info, err := client.Create(context.Background(), supervisor.CreateOptions{
				Command: []string{"/bin/sh", "-c", "printf 'runtime-bridge-marker\\n'; sleep 30"},
				Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
				Cols:    80, Rows: 24,
			})
			if err != nil {
				t.Error(err)
				return
			}
			created = &info
			if err := writeServerMessage(controlConn, serverMessage{Type: "bridge", BridgeID: "runtime-bridge-2"}); err != nil {
				t.Error(err)
			}
		case "runtime-bridge-2":
			if err := writeServerMessage(conn, serverMessage{Type: "hello_ok"}); err != nil {
				t.Error(err)
				return
			}
			stream := &wsStream{conn: conn, ctx: context.Background()}
			client := supervisor.NewRemoteClient(func(context.Context) (net.Conn, error) { return stream, nil }, digest)
			attachment, err := client.Attach(context.Background(), created.ID, nil)
			if err != nil {
				t.Error(err)
				return
			}
			output, err := readUntilMarkerE(attachment, "runtime-bridge-marker", 20*time.Second)
			if err != nil {
				t.Error(err)
			}
			bridgeOutput <- output
			_ = attachment.Close()
		default:
			if err := writeServerMessage(conn, serverMessage{Type: "hello_ok"}); err != nil {
				t.Error(err)
				return
			}
			controlConn = conn
			if err := writeServerMessage(conn, serverMessage{Type: "bridge", BridgeID: "runtime-bridge-1"}); err != nil {
				t.Error(err)
				return
			}
			for {
				if _, _, err := conn.Read(context.Background()); err != nil {
					return
				}
			}
		}
	})

	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	prober, err := NewProber(entries)
	if err != nil {
		t.Fatal(err)
	}
	agentRuntime, err := New(Options{
		Store:        StoreAt(dataDir),
		DataDir:      dataDir,
		StateDir:     stateDir,
		Prober:       prober,
		Manager:      &fakeManager{},
		Collector:    fakeCollector{},
		EnsureHelper: func(context.Context) error { return nil },
		Bridge:       testBridgeSpawner(t),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agentRuntime.Run(ctx) }()

	select {
	case output := <-bridgeOutput:
		if !strings.Contains(output, "runtime-bridge-marker") {
			t.Fatalf("bridge output = %q", output)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("runtime bridge did not deliver session output")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want clean stop", err)
	}
}
