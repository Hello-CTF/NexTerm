package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type fakeFleetServer struct {
	t      *testing.T
	server *httptest.Server

	mu                sync.Mutex
	syncRequests      []SyncRequest
	currentURLReports []currentURLRequest
	syncResponse      SyncResponse
	syncForbidden     bool
	wsControl         func(conn *websocket.Conn, hello HelloMessage)
}

func newFakeFleetServer(t *testing.T) *fakeFleetServer {
	t.Helper()
	fake := &fakeFleetServer{
		t:            t,
		syncResponse: SyncResponse{DesiredAutostart: true, MetricsIntervalMS: 5000, TerminalEnabled: true},
		wsControl: func(conn *websocket.Conn, hello HelloMessage) {
			if err := writeServerMessage(conn, serverMessage{Type: "hello_ok"}); err != nil {
				t.Error(err)
				return
			}
			for {
				if _, _, err := conn.Read(context.Background()); err != nil {
					return
				}
			}
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc(PathHealth, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "service": "nexterm-server"})
	})
	mux.HandleFunc("POST "+PathEnroll, func(w http.ResponseWriter, r *http.Request) {
		var request EnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(EnrollResponse{
			DeviceID: "01J5DEVICE0000000000000000", Secret: "device-secret",
			BaseURLs:          []BaseURLEntry{{URL: fake.server.URL}},
			MetricsIntervalMS: 5000, DesiredAutostart: true, TerminalEnabled: true,
		})
	})
	mux.HandleFunc("POST "+PathSync, func(w http.ResponseWriter, r *http.Request) {
		var request SyncRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		fake.mu.Lock()
		fake.syncRequests = append(fake.syncRequests, request)
		forbidden := fake.syncForbidden
		response := fake.syncResponse
		fake.mu.Unlock()
		if forbidden {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"ok":false,"error":{"code":"forbidden","message":"设备已吊销"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	})
	mux.HandleFunc("POST "+PathCurrentURL, func(w http.ResponseWriter, r *http.Request) {
		var request currentURLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		fake.mu.Lock()
		fake.currentURLReports = append(fake.currentURLReports, request)
		fake.mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc(PathDeviceWS, func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		helloCtx, helloCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer helloCancel()
		_, payload, err := conn.Read(helloCtx)
		if err != nil {
			t.Error(err)
			return
		}
		var hello HelloMessage
		if err := json.Unmarshal(payload, &hello); err != nil {
			t.Error(err)
			return
		}
		fake.mu.Lock()
		control := fake.wsControl
		fake.mu.Unlock()
		control(conn, hello)
	})
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeFleetServer) setSyncResponse(response SyncResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncResponse = response
}

func (f *fakeFleetServer) forbidSync() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncForbidden = true
}

func (f *fakeFleetServer) setWSControl(control func(conn *websocket.Conn, hello HelloMessage)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wsControl = control
}

func (f *fakeFleetServer) syncs() []SyncRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SyncRequest(nil), f.syncRequests...)
}

func (f *fakeFleetServer) urlReports() []currentURLRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]currentURLRequest(nil), f.currentURLReports...)
}

type fakeManager struct {
	mu              sync.Mutex
	installCalls    int
	uninstallCalls  int
	state           ServiceState
	installFailures int
}

func (m *fakeManager) Install(context.Context, string, string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.installCalls++
	if m.installFailures > 0 {
		m.installFailures--
		return errors.New("user service manager temporarily offline")
	}
	m.state = ServiceState{Installed: true, Enabled: true, Active: true}
	return nil
}

func (m *fakeManager) Uninstall(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uninstallCalls++
	m.state = ServiceState{}
	return nil
}

func (m *fakeManager) Status(context.Context) ServiceState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *fakeManager) counts() (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.installCalls, m.uninstallCalls
}

func (m *fakeManager) current() ServiceState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *fakeManager) drift(state ServiceState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = state
}

type fakeCollector struct{ err error }

func (c fakeCollector) Collect() (Sample, error) {
	if c.err != nil {
		return Sample{}, c.err
	}
	return Sample{TS: 1700000000000, CPUPct: 1.5, MemUsed: 1, MemTotal: 2, DiskUsed: 3, DiskTotal: 4, UptimeS: 5}, nil
}

func writeRuntimeConfig(t *testing.T, dataDir string, entries []BaseURLEntry, desiredAutostart bool) *Config {
	t.Helper()
	config := testConfig()
	config.BaseURLs = entries
	config.DesiredAutostart = desiredAutostart
	config.MetricsIntervalMS = 5000
	store := StoreAt(dataDir)
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}
	return config
}

func newTestRuntime(t *testing.T, dataDir string, entries []BaseURLEntry, manager ServiceManager) *Runtime {
	t.Helper()
	prober, err := NewProber(entries)
	if err != nil {
		t.Fatal(err)
	}
	prober.baseBackoff = 100 * time.Millisecond
	prober.maxBackoff = time.Second
	agentRuntime, err := New(Options{
		Store:        StoreAt(dataDir),
		DataDir:      dataDir,
		Prober:       prober,
		Manager:      manager,
		Collector:    fakeCollector{},
		EnsureHelper: func(context.Context) error { return nil },
		Bridge:       func(context.Context, string) (io.ReadWriteCloser, error) { return nil, ErrServiceUnsupported },
		Logger:       slog.New(slog.NewTextHandler(os.Stdout, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return agentRuntime
}

func waitFor(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRuntimeReconcilesAutostartFromServer(t *testing.T) {
	server := newFakeFleetServer(t)
	server.setSyncResponse(SyncResponse{DesiredAutostart: false, MetricsIntervalMS: 5000, TerminalEnabled: true})
	dataDir := t.TempDir()
	writeRuntimeConfig(t, dataDir, []BaseURLEntry{{URL: server.server.URL}}, true)
	manager := &fakeManager{}
	agentRuntime := newTestRuntime(t, dataDir, []BaseURLEntry{{URL: server.server.URL}}, manager)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agentRuntime.Run(ctx) }()

	waitFor(t, 5*time.Second, "initial autostart install", func() bool {
		installs, _ := manager.counts()
		return installs >= 1
	})
	waitFor(t, 15*time.Second, "first sync with service state", func() bool {
		for _, request := range server.syncs() {
			if request.ServiceState != nil && request.ServiceState.Installed && request.Sample != nil && request.Sample.CPUPct == 1.5 {
				return true
			}
		}
		return false
	})
	waitFor(t, 10*time.Second, "uninstall after server desired_autostart=false", func() bool {
		_, uninstalls := manager.counts()
		return uninstalls >= 1
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want clean stop", err)
	}
}

func TestRuntimeRetriesAutostartInstallOnLaterSyncs(t *testing.T) {
	server := newFakeFleetServer(t)
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	manager := &fakeManager{installFailures: 1}
	agentRuntime := newTestRuntime(t, dataDir, entries, manager)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agentRuntime.Run(ctx) }()

	waitFor(t, 20*time.Second, "install retried and repaired with unchanged desired state", func() bool {
		installs, _ := manager.counts()
		return installs >= 2 && manager.current().Installed
	})
	waitFor(t, 10*time.Second, "sync carrying repaired service state", func() bool {
		for _, request := range server.syncs() {
			if request.ServiceState != nil && request.ServiceState.Installed {
				return true
			}
		}
		return false
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want clean stop", err)
	}
}

func TestRuntimeRepairsExternalServiceDrift(t *testing.T) {
	server := newFakeFleetServer(t)
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	manager := &fakeManager{}
	agentRuntime := newTestRuntime(t, dataDir, entries, manager)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agentRuntime.Run(ctx) }()

	waitFor(t, 10*time.Second, "initial install", func() bool {
		installs, _ := manager.counts()
		return installs >= 1
	})
	manager.drift(ServiceState{})
	waitFor(t, 20*time.Second, "reconcile repairs external drift", func() bool {
		installs, _ := manager.counts()
		return installs >= 2 && manager.current().Installed
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want clean stop", err)
	}
}

func TestRuntimeFailoverAtReconnectBoundary(t *testing.T) {
	first := newFakeFleetServer(t)
	second := newFakeFleetServer(t)
	entries := []BaseURLEntry{{URL: first.server.URL}, {URL: second.server.URL}}
	dataDir := t.TempDir()
	writeRuntimeConfig(t, dataDir, entries, true)
	manager := &fakeManager{}
	agentRuntime := newTestRuntime(t, dataDir, entries, manager)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agentRuntime.Run(ctx) }()

	waitFor(t, 15*time.Second, "initial sync on first endpoint", func() bool {
		return len(first.syncs()) >= 1
	})
	first.server.Close()
	waitFor(t, 20*time.Second, "failover sync on second endpoint", func() bool {
		return len(second.syncs()) >= 1
	})
	waitFor(t, 10*time.Second, "failover report on second endpoint", func() bool {
		for _, report := range second.urlReports() {
			if report.URL == second.server.URL && report.Reason == "failover" {
				return true
			}
		}
		return false
	})
	config, err := StoreAt(dataDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.CurrentURL != second.server.URL {
		t.Fatalf("persisted current URL = %q, want %q", config.CurrentURL, second.server.URL)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want clean stop", err)
	}
}

func TestRuntimeStopsOnSyncRevocation(t *testing.T) {
	server := newFakeFleetServer(t)
	server.forbidSync()
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	agentRuntime := newTestRuntime(t, dataDir, entries, &fakeManager{})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := agentRuntime.Run(ctx)
	if err != ErrRevoked {
		t.Fatalf("Run = %v, want ErrRevoked", err)
	}
}

func TestRuntimeStopsOnChannelRevocation(t *testing.T) {
	server := newFakeFleetServer(t)
	server.setWSControl(func(conn *websocket.Conn, hello HelloMessage) {
		if err := writeServerMessage(conn, serverMessage{Type: "hello_ok"}); err != nil {
			t.Error(err)
			return
		}
		if err := writeServerMessage(conn, serverMessage{Type: "revoke"}); err != nil {
			t.Error(err)
			return
		}
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	})
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	agentRuntime := newTestRuntime(t, dataDir, entries, &fakeManager{})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := agentRuntime.Run(ctx)
	if err != ErrRevoked {
		t.Fatalf("Run = %v, want ErrRevoked", err)
	}
}

func TestRuntimeProtocolMismatchIsFatal(t *testing.T) {
	server := newFakeFleetServer(t)
	server.setWSControl(func(conn *websocket.Conn, hello HelloMessage) {
		if err := writeServerMessage(conn, serverMessage{Type: "error", Code: "version_mismatch", Message: "protocol 2 required"}); err != nil {
			t.Error(err)
		}
	})
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	agentRuntime := newTestRuntime(t, dataDir, entries, &fakeManager{})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := agentRuntime.Run(ctx)
	if err != ErrProtocolMismatch {
		t.Fatalf("Run = %v, want ErrProtocolMismatch", err)
	}
}

func TestRuntimeSyncContinuesWithoutMetrics(t *testing.T) {
	server := newFakeFleetServer(t)
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
		Prober:       prober,
		Manager:      &fakeManager{},
		Collector:    fakeCollector{err: ErrMetricsUnsupported},
		EnsureHelper: func(context.Context) error { return nil },
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agentRuntime.Run(ctx) }()
	waitFor(t, 15*time.Second, "sync without metrics sample", func() bool {
		for _, request := range server.syncs() {
			if request.Sample == nil && request.ServiceState != nil {
				return true
			}
		}
		return false
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want clean stop", err)
	}
}
