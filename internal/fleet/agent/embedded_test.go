package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// memoryStore keeps the device config in memory, as a desktop keeps it in its vault.
type memoryStore struct {
	mu     sync.Mutex
	config *Config
}

func (s *memoryStore) Load() (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.config == nil {
		return nil, ErrNotEnrolled
	}
	copied := *s.config
	return &copied, nil
}

func (s *memoryStore) Save(config *Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := *config
	s.config = &copied
	return nil
}

func newEmbeddedRuntime(t *testing.T, server *fakeFleetServer, store ConfigStore, onChannel func(bool)) *Runtime {
	t.Helper()
	return newEmbeddedRuntimeWithBridge(t, server, store, onChannel, func(context.Context, string) (io.ReadWriteCloser, error) {
		return nil, ErrServiceUnsupported
	})
}

func newEmbeddedRuntimeWithBridge(t *testing.T, server *fakeFleetServer, store ConfigStore, onChannel func(bool), bridge BridgeSpawner) *Runtime {
	t.Helper()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	prober, err := NewProber(entries)
	if err != nil {
		t.Fatal(err)
	}
	prober.baseBackoff = 100 * time.Millisecond
	prober.maxBackoff = time.Second
	agentRuntime, err := New(Options{
		Store:         store,
		DataDir:       t.TempDir(),
		Prober:        prober,
		Manager:       EmbeddedServiceManager(),
		Collector:     fakeCollector{},
		EnsureHelper:  func(context.Context) error { return nil },
		Bridge:        bridge,
		NoSelfUpgrade: true,
		OnChannel:     onChannel,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return agentRuntime
}

func embeddedConfig(server *fakeFleetServer) *Config {
	config := testConfig()
	config.BaseURLs = []BaseURLEntry{{URL: server.server.URL}}
	config.DesiredAutostart = false
	config.MetricsIntervalMS = 5000
	return config
}

// An embedded runtime runs from a config store that is not a file and
// reports when its control channel comes up and goes away.
func TestEmbeddedRuntimeReportsControlChannel(t *testing.T) {
	server := newFakeFleetServer(t)
	held := make(chan *websocket.Conn, 1)
	server.setWSControl(func(conn *websocket.Conn, hello HelloMessage) {
		if err := writeServerMessage(conn, serverMessage{Type: "hello_ok"}); err != nil {
			t.Error(err)
			return
		}
		held <- conn
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	})
	store := &memoryStore{config: embeddedConfig(server)}
	states := make(chan bool, 8)
	agentRuntime := newEmbeddedRuntime(t, server, store, func(connected bool) { states <- connected })
	if agentRuntime.upgrader != nil {
		t.Fatal("an embedded runtime must not get a self upgrader")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agentRuntime.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run = %v, want clean stop", err)
		}
	}()

	select {
	case connected := <-states:
		if !connected {
			t.Fatal("first channel report = lost, want connected")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("control channel was not reported")
	}
	conn := <-held
	_ = conn.Close(websocket.StatusGoingAway, "server restart")
	select {
	case connected := <-states:
		if connected {
			t.Fatal("channel report after the server closed it = connected, want lost")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("lost control channel was not reported")
	}
	if saved, err := store.Load(); err != nil || saved.CurrentURL != server.server.URL {
		t.Fatalf("saved config = %+v %v, want the current URL saved through the config store", saved, err)
	}
}

func TestEmbeddedRuntimeProtocolMismatchStaysFatal(t *testing.T) {
	server := newFakeFleetServer(t)
	server.setWSControl(mismatchControl("9.9.9"))
	agentRuntime := newEmbeddedRuntime(t, server, &memoryStore{config: embeddedConfig(server)}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := agentRuntime.Run(ctx); !errors.Is(err, ErrProtocolMismatch) {
		t.Fatalf("Run = %v, want ErrProtocolMismatch without a self upgrade", err)
	}
}

func TestEmbeddedServiceManagerInstallsNothing(t *testing.T) {
	manager := EmbeddedServiceManager()
	if state := manager.Status(context.Background()); state != (ServiceState{}) {
		t.Fatalf("status = %+v, want a clean uninstalled service", state)
	}
	if err := manager.Install(context.Background(), "nexterm", t.TempDir()); !errors.Is(err, ErrServiceUnsupported) {
		t.Fatalf("install = %v, want ErrServiceUnsupported", err)
	}
	if err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("uninstall = %v, want nothing to remove", err)
	}
}

// embeddedServer answers the control channel with hello_ok and the given
// bridge requests, and hands every bridge dial (hello and connection) to the test.
type embeddedServer struct {
	controlHello chan HelloMessage
	bridges      chan embeddedBridge
}

type embeddedBridge struct {
	hello HelloMessage
	conn  *websocket.Conn
}

func newEmbeddedServer(t *testing.T, server *fakeFleetServer, requests ...serverMessage) *embeddedServer {
	probe := &embeddedServer{controlHello: make(chan HelloMessage, 4), bridges: make(chan embeddedBridge, 8)}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	server.setWSControl(func(conn *websocket.Conn, hello HelloMessage) {
		if err := writeServerMessage(conn, serverMessage{Type: "hello_ok"}); err != nil {
			t.Error(err)
			return
		}
		if hello.BridgeID != "" {
			probe.bridges <- embeddedBridge{hello: hello, conn: conn}
			// The test reads the connection; the handler only keeps it open.
			<-release
			return
		}
		probe.controlHello <- hello
		for _, request := range requests {
			if err := writeServerMessage(conn, request); err != nil {
				t.Error(err)
				return
			}
		}
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	})
	return probe
}

// startEmbedded runs the runtime; done closes when Run returned, and *runErr
// holds its result from then on.
func startEmbedded(t *testing.T, agentRuntime *Runtime) (cancel func(), done <-chan struct{}, runErr *error) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	finished := make(chan struct{})
	var err error
	go func() {
		defer close(finished)
		err = agentRuntime.Run(ctx)
	}()
	t.Cleanup(func() {
		stop()
		<-finished
	})
	return stop, finished, &err
}

// The exposed desktop serves terminals only: it advertises exactly the
// terminal purpose, and file, forward and listen requests are dropped without
// a dial while a terminal request on the same channel is served.
func TestEmbeddedRuntimeServesTerminalOnly(t *testing.T) {
	server := newFakeFleetServer(t)
	probe := newEmbeddedServer(t, server,
		serverMessage{Type: "bridge", BridgeID: "bridge-file", Purpose: "file"},
		serverMessage{Type: "bridge", BridgeID: "bridge-forward", Purpose: "forward"},
		serverMessage{Type: "bridge", BridgeID: "bridge-listen", Purpose: "listen"},
		serverMessage{Type: "bridge", BridgeID: "bridge-terminal", Purpose: PurposeTerminal},
	)
	bridge := func(context.Context, string) (io.ReadWriteCloser, error) {
		local, _ := net.Pipe()
		return local, nil
	}
	_, _, _ = startEmbedded(t, newEmbeddedRuntimeWithBridge(t, server, &memoryStore{config: embeddedConfig(server)}, nil, bridge))

	select {
	case hello := <-probe.controlHello:
		if len(hello.Capabilities) != 1 || hello.Capabilities[0] != PurposeTerminal {
			t.Fatalf("capabilities = %v, want exactly [terminal]", hello.Capabilities)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("control channel did not come up")
	}
	// Requests are handled in order, so once the terminal bridge is dialed the
	// earlier ones were already dropped.
	select {
	case dialed := <-probe.bridges:
		if dialed.hello.BridgeID != "bridge-terminal" {
			t.Fatalf("dialed bridge %+v, want only the terminal bridge", dialed.hello)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("terminal bridge was not dialed")
	}
	select {
	case dialed := <-probe.bridges:
		t.Fatalf("a non-terminal bridge was dialed: %+v", dialed.hello)
	default:
	}
}

// Turning exposure off cancels the runtime, which closes a terminal bridge
// that is attached at that moment, on both the server and the helper side.
func TestEmbeddedRuntimeStopClosesAttachedBridge(t *testing.T) {
	server := newFakeFleetServer(t)
	probe := newEmbeddedServer(t, server, serverMessage{Type: "bridge", BridgeID: "bridge-t", Purpose: PurposeTerminal})
	helperSide := make(chan net.Conn, 1)
	bridge := func(context.Context, string) (io.ReadWriteCloser, error) {
		local, remote := net.Pipe()
		helperSide <- remote
		return local, nil
	}
	cancel, done, runErr := startEmbedded(t, newEmbeddedRuntimeWithBridge(t, server, &memoryStore{config: embeddedConfig(server)}, nil, bridge))

	var attached embeddedBridge
	select {
	case attached = <-probe.bridges:
	case <-time.After(15 * time.Second):
		t.Fatal("terminal bridge was not dialed")
	}
	helper := <-helperSide
	// Bytes flow through the attached bridge before exposure is turned off.
	go func() { _, _ = helper.Write([]byte("prompt$ ")) }()
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	if _, data, err := attached.conn.Read(readCtx); err != nil || string(data) != "prompt$ " {
		t.Fatalf("bridge read = %q %v", data, err)
	}

	cancel()
	select {
	case <-done:
		if *runErr != nil {
			t.Fatalf("Run = %v, want a clean stop", *runErr)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("runtime did not stop")
	}
	if _, _, err := attached.conn.Read(readCtx); err == nil {
		t.Fatal("server side of the bridge still open after the runtime stopped")
	}
	_ = helper.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := helper.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("helper side of the bridge read = %v, want it closed", err)
	}
}
