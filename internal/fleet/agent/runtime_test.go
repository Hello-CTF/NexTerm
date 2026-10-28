package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/version"
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
	agentRuntime.serviceRecheck = 1 // re-read the manager on every cycle so drift is seen at once

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
	// Always stop the runtime before TempDir cleanup, including on t.Fatal (same shape as runBridgeProbe).
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v, want clean stop", err)
		}
	})

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
	// The failover report reaches the server before notifyCurrentURL persists the new current URL, so poll the store.
	waitFor(t, 10*time.Second, "persisted current URL on second endpoint", func() bool {
		config, err := StoreAt(dataDir).Load()
		return err == nil && config.CurrentURL == second.server.URL
	})
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

func mismatchControl(serverVersion string) func(conn *websocket.Conn, hello HelloMessage) {
	return func(conn *websocket.Conn, hello HelloMessage) {
		if err := writeServerMessage(conn, serverMessage{Type: "error", Code: "version_mismatch", Message: "protocol 2 required", ServerVersion: serverVersion}); err != nil {
			_ = conn.Close(websocket.StatusProtocolError, "write failed")
		}
	}
}

func newUpgradeTestRuntime(t *testing.T, dataDir string, entries []BaseURLEntry, upgrader *Upgrader) *Runtime {
	t.Helper()
	prober, err := NewProber(entries)
	if err != nil {
		t.Fatal(err)
	}
	agentRuntime, err := New(Options{
		Store:        StoreAt(dataDir),
		DataDir:      dataDir,
		Prober:       prober,
		Manager:      &fakeManager{},
		Collector:    fakeCollector{},
		EnsureHelper: func(context.Context) error { return nil },
		Upgrader:     upgrader,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return agentRuntime
}

func TestRuntimeSelfUpgradeOnProtocolMismatch(t *testing.T) {
	version := "1.2.3"
	server := newFakeFleetServer(t)
	server.setWSControl(mismatchControl(version))
	asset := releaseAssetName(version, runtime.GOOS, runtime.GOARCH)
	binary := []byte("new-agent-binary")
	archive := buildReleaseArchive(t, runtime.GOOS, binary)
	release := newReleaseServer(t, map[string][]byte{
		"/v" + version + "/" + asset:             archive,
		"/v" + version + "/" + checksumsFileName: []byte(fmt.Sprintf("%s  %s\n", sha256Hex(archive), asset)),
	})
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	target := filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS))
	if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	var replaced []stagedReplace
	upgrader := &Upgrader{
		BaseURL: release.URL, Executable: target,
		MaxAttempts: 3, Backoff: time.Millisecond,
		sleep: instantSleep, scheduleReplace: captureReplace(&replaced),
	}
	agentRuntime := newUpgradeTestRuntime(t, dataDir, entries, upgrader)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := agentRuntime.Run(ctx)
	if !errors.Is(err, errUpgradeStaged) {
		t.Fatalf("Run = %v, want errUpgradeStaged", err)
	}
	if len(replaced) != 1 || replaced[0].targetPath != target {
		t.Fatalf("scheduleReplace calls = %+v", replaced)
	}
	content, readErr := os.ReadFile(replaced[0].newPath)
	if readErr != nil || !bytes.Equal(content, binary) {
		t.Fatalf("staged binary = %q, %v", content, readErr)
	}
	config, loadErr := StoreAt(dataDir).Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if config.DeviceID != testConfig().DeviceID || config.Secret != testConfig().Secret {
		t.Fatalf("enroll credentials must survive self-upgrade, got device_id=%s", config.DeviceID)
	}
}

func TestRuntimeSelfUpgradeExhaustsRetries(t *testing.T) {
	version := "9.9.9"
	server := newFakeFleetServer(t)
	server.setWSControl(mismatchControl(version))
	release := newReleaseServer(t, map[string][]byte{})
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	upgrader := &Upgrader{
		BaseURL: release.URL, Executable: filepath.Join(t.TempDir(), "nexterm-agent"),
		MaxAttempts: 3, Backoff: time.Millisecond,
		sleep: instantSleep, scheduleReplace: func(string, string) error { return nil },
	}
	agentRuntime := newUpgradeTestRuntime(t, dataDir, entries, upgrader)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := agentRuntime.Run(ctx); err != ErrProtocolMismatch {
		t.Fatalf("Run = %v, want ErrProtocolMismatch after retry exhaustion", err)
	}
	if got := release.requestCount("/v" + version + "/"); got != 3 {
		t.Fatalf("upgrade attempts = %d, want 3", got)
	}
}

func TestRuntimeSelfUpgradeCancelDuringDownloadStopsCleanly(t *testing.T) {
	server := newFakeFleetServer(t)
	server.setWSControl(mismatchControl("1.2.3"))
	downloadStarted := make(chan struct{})
	var startOnce sync.Once
	release := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startOnce.Do(func() { close(downloadStarted) })
		<-r.Context().Done()
	}))
	t.Cleanup(release.Close)
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	upgrader := &Upgrader{
		BaseURL: release.URL, Executable: filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS)),
		MaxAttempts: 1, scheduleReplace: func(string, string) error { return nil },
	}
	agentRuntime := newUpgradeTestRuntime(t, dataDir, entries, upgrader)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agentRuntime.Run(ctx) }()
	select {
	case <-downloadStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for the upgrade download to start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want clean stop on cancel during the self-upgrade download", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for Run to stop after cancel")
	}
}

func TestRuntimeEmbeddedDistributionSkipsSelfUpgrade(t *testing.T) {
	previous := cliServicePrefix
	cliServicePrefix = []string{"agent"}
	t.Cleanup(func() { cliServicePrefix = previous })

	server := newFakeFleetServer(t)
	server.setWSControl(mismatchControl("9.9.9"))
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	agentRuntime := newUpgradeTestRuntime(t, dataDir, entries, nil)
	if agentRuntime.upgrader != nil {
		t.Fatal("embedded distribution must not get a default upgrader")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := agentRuntime.Run(ctx); err != ErrProtocolMismatch {
		t.Fatalf("Run = %v, want ErrProtocolMismatch", err)
	}
}

func TestRuntimeSelfUpgradeSkipsCurrentVersion(t *testing.T) {
	server := newFakeFleetServer(t)
	server.setWSControl(mismatchControl(version.Version))
	release := newReleaseServer(t, map[string][]byte{})
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	upgrader := &Upgrader{
		BaseURL: release.URL, Executable: filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS)),
		MaxAttempts: 3, Backoff: time.Millisecond,
		sleep: instantSleep, scheduleReplace: func(string, string) error { return nil },
	}
	agentRuntime := newUpgradeTestRuntime(t, dataDir, entries, upgrader)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := agentRuntime.Run(ctx); err != ErrProtocolMismatch {
		t.Fatalf("Run = %v, want ErrProtocolMismatch", err)
	}
	if got := release.requestCount("/"); got != 0 {
		t.Fatalf("same-version target must skip the download, got %d requests", got)
	}
}

func TestRuntimeSelfUpgradeRefusesDowngrade(t *testing.T) {
	previous := version.Version
	version.Version = "1.2.0"
	t.Cleanup(func() { version.Version = previous })
	server := newFakeFleetServer(t)
	server.setWSControl(mismatchControl("1.1.0"))
	release := newReleaseServer(t, map[string][]byte{})
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	upgrader := &Upgrader{
		BaseURL: release.URL, Executable: filepath.Join(t.TempDir(), agentBinaryName(runtime.GOOS)),
		MaxAttempts: 3, Backoff: time.Millisecond,
		sleep: instantSleep, scheduleReplace: func(string, string) error { return nil },
	}
	agentRuntime := newUpgradeTestRuntime(t, dataDir, entries, upgrader)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := agentRuntime.Run(ctx); err != ErrProtocolMismatch {
		t.Fatalf("Run = %v, want ErrProtocolMismatch", err)
	}
	if got := release.requestCount("/"); got != 0 {
		t.Fatalf("server-driven downgrade must skip the download, got %d requests", got)
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

func TestEffectiveAppVersion(t *testing.T) {
	if got := effectiveAppVersion("1.0.7"); got != "1.0.7" {
		t.Fatalf("dev build must keep the config version, got %q", got)
	}
	old := version.Version
	version.Version = "1.0.10"
	defer func() { version.Version = old }()
	if got := effectiveAppVersion("1.0.7"); got != "1.0.10" {
		t.Fatalf("release build must report the build version, got %q", got)
	}
}

// bridgeProbe routes the fake fleet server's WS connections by hello: control channels get hello_ok plus
// the given bridge request, and bridge dials are captured (hello recorded) then held open.
type bridgeProbe struct {
	request      serverMessage
	controlHello chan HelloMessage
	bridgeHello  chan HelloMessage
}

func newBridgeProbe(request serverMessage) *bridgeProbe {
	return &bridgeProbe{
		request:      request,
		controlHello: make(chan HelloMessage, 4),
		bridgeHello:  make(chan HelloMessage, 4),
	}
}

func (p *bridgeProbe) control(t *testing.T) func(conn *websocket.Conn, hello HelloMessage) {
	return func(conn *websocket.Conn, hello HelloMessage) {
		if hello.BridgeID != "" {
			p.bridgeHello <- hello
		} else {
			p.controlHello <- hello
		}
		if err := writeServerMessage(conn, serverMessage{Type: "hello_ok"}); err != nil {
			t.Error(err)
			return
		}
		if hello.BridgeID == "" && p.request.Type != "" {
			if err := writeServerMessage(conn, p.request); err != nil {
				t.Error(err)
				return
			}
		}
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	}
}

// runBridgeProbe starts the runtime against the probe server and returns a stop function that always
// leaves a cleanly stopped runtime, including on t.Fatal.
func runBridgeProbe(t *testing.T, agentRuntime *Runtime) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agentRuntime.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v, want clean stop", err)
		}
	})
}

func newBridgeProbeRuntime(t *testing.T, server *fakeFleetServer) *Runtime {
	t.Helper()
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	writeRuntimeConfig(t, dataDir, entries, true)
	return newTestRuntime(t, dataDir, entries, &fakeManager{})
}

// The terminal path is byte-identical to before purposes existed: the bridge is dialed and the hello
// echoes the purpose, with capabilities advertising exactly the terminal service.
func TestRuntimeRoutesTerminalBridge(t *testing.T) {
	probe := newBridgeProbe(serverMessage{Type: "bridge", BridgeID: "bridge-t1", Purpose: PurposeTerminal})
	server := newFakeFleetServer(t)
	server.setWSControl(probe.control(t))
	runBridgeProbe(t, newBridgeProbeRuntime(t, server))

	select {
	case hello := <-probe.bridgeHello:
		if hello.BridgeID != "bridge-t1" || hello.Purpose != PurposeTerminal {
			t.Fatalf("bridge hello = %+v, want terminal purpose echo", hello)
		}
		if len(hello.Capabilities) != 1 || hello.Capabilities[0] != PurposeTerminal {
			t.Fatalf("bridge hello capabilities = %v, want [terminal]", hello.Capabilities)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("terminal bridge was not dialed")
	}
}

// When an older helper still owns the state directory, ensureHelper reports a
// helper upgrade but the bridge is still dialed: the helper bridge answers the
// server's hello with that reason instead of leaving the server waiting until
// it declares the device offline. Other helper failures still abort the bridge.
func TestRuntimeBridgesWhenHelperNeedsUpgrade(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		dialed bool
	}{
		{"legacy helper", fmt.Errorf("connect: %w", supervisor.ErrLegacyHelperRunning), true},
		{"helper unavailable", fmt.Errorf("connect: %w", supervisor.ErrUnavailable), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			probe := newBridgeProbe(serverMessage{Type: "bridge", BridgeID: "bridge-l1", Purpose: PurposeTerminal})
			server := newFakeFleetServer(t)
			server.setWSControl(probe.control(t))
			dataDir := t.TempDir()
			entries := []BaseURLEntry{{URL: server.server.URL}}
			writeRuntimeConfig(t, dataDir, entries, true)
			prober, err := NewProber(entries)
			if err != nil {
				t.Fatal(err)
			}
			prober.baseBackoff = 100 * time.Millisecond
			prober.maxBackoff = time.Second
			ensureErr := test.err
			agentRuntime, err := New(Options{
				Store:        StoreAt(dataDir),
				DataDir:      dataDir,
				Prober:       prober,
				Manager:      &fakeManager{},
				Collector:    fakeCollector{},
				EnsureHelper: func(context.Context) error { return ensureErr },
				Bridge:       func(context.Context, string) (io.ReadWriteCloser, error) { return nil, ErrServiceUnsupported },
				Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			runBridgeProbe(t, agentRuntime)
			select {
			case <-probe.controlHello:
			case <-time.After(15 * time.Second):
				t.Fatal("control channel did not come up")
			}
			wait := 500 * time.Millisecond
			if test.dialed {
				wait = 15 * time.Second
			}
			select {
			case hello := <-probe.bridgeHello:
				if !test.dialed {
					t.Fatalf("bridge dialed although the helper is unavailable: %+v", hello)
				}
			case <-time.After(wait):
				if test.dialed {
					t.Fatal("bridge was not dialed for a helper that needs upgrade")
				}
			}
		})
	}
}

// The terminal gate still applies to purpose-routed requests: with terminal access disabled the bridge
// request is dropped before any dial.
func TestRuntimeTerminalBridgeGateUnchanged(t *testing.T) {
	probe := newBridgeProbe(serverMessage{Type: "bridge", BridgeID: "bridge-g1", Purpose: PurposeTerminal})
	server := newFakeFleetServer(t)
	server.setWSControl(probe.control(t))
	dataDir := t.TempDir()
	entries := []BaseURLEntry{{URL: server.server.URL}}
	config := writeRuntimeConfig(t, dataDir, entries, true)
	config.TerminalEnabled = false
	if err := StoreAt(dataDir).Save(config); err != nil {
		t.Fatal(err)
	}
	agentRuntime := newTestRuntime(t, dataDir, entries, &fakeManager{})
	runBridgeProbe(t, agentRuntime)

	// The bridge request is written right after hello_ok on the same connection, so once the control hello
	// arrived the request is already on its way.
	select {
	case <-probe.controlHello:
	case <-time.After(15 * time.Second):
		t.Fatal("control channel did not come up")
	}
	select {
	case hello := <-probe.bridgeHello:
		t.Fatalf("terminal-disabled bridge was dialed: %+v", hello)
	case <-time.After(500 * time.Millisecond):
	}
}

// A bridge request for a purpose with no registered handler is logged and dropped before any dial,
// leaving the control channel and runtime untouched.
func TestRuntimeDropsUnhandledPurposeBridge(t *testing.T) {
	probe := newBridgeProbe(serverMessage{Type: "bridge", BridgeID: "bridge-u1", Purpose: "file"})
	server := newFakeFleetServer(t)
	server.setWSControl(probe.control(t))
	runBridgeProbe(t, newBridgeProbeRuntime(t, server))

	select {
	case <-probe.controlHello:
	case <-time.After(15 * time.Second):
		t.Fatal("control channel did not come up")
	}
	select {
	case hello := <-probe.bridgeHello:
		t.Fatalf("unhandled purpose bridge was dialed: %+v", hello)
	case <-time.After(500 * time.Millisecond):
	}
}

// A registered purpose handler is invoked under the runtime's loopWg bookkeeping, the runtime itself never
// dials for it, and the purpose joins the advertised capabilities.
func TestRuntimePurposeHandlerServesBridge(t *testing.T) {
	probe := newBridgeProbe(serverMessage{Type: "bridge", BridgeID: "bridge-f1", Purpose: "file"})
	server := newFakeFleetServer(t)
	server.setWSControl(probe.control(t))
	agentRuntime := newBridgeProbeRuntime(t, server)
	handled := make(chan string, 1)
	agentRuntime.RegisterPurposeHandler("file", func(ctx context.Context, bridgeID string) error {
		handled <- bridgeID
		return nil
	})
	runBridgeProbe(t, agentRuntime)

	select {
	case hello := <-probe.controlHello:
		var terminal, file bool
		for _, capability := range hello.Capabilities {
			terminal = terminal || capability == PurposeTerminal
			file = file || capability == "file"
		}
		if !terminal || !file {
			t.Fatalf("control hello capabilities = %v, want terminal and file", hello.Capabilities)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("control channel did not come up")
	}
	select {
	case bridgeID := <-handled:
		if bridgeID != "bridge-f1" {
			t.Fatalf("handled bridge = %q, want bridge-f1", bridgeID)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("purpose handler was not invoked")
	}
	select {
	case hello := <-probe.bridgeHello:
		t.Fatalf("runtime dialed a purpose bridge itself: %+v", hello)
	case <-time.After(500 * time.Millisecond):
	}
}
