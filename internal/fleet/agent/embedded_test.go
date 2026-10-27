package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
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
		Bridge:        func(context.Context, string) (io.ReadWriteCloser, error) { return nil, ErrServiceUnsupported },
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
