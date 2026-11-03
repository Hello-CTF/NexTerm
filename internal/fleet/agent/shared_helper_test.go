package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/coder/websocket"
)

func shareHelperDir(t *testing.T, standardDataDir, shared string) {
	t.Helper()
	previous := sharedHelperDir
	sharedHelperDir = func(dataDir string) (string, bool) {
		return shared, filepath.Clean(dataDir) == filepath.Clean(standardDataDir)
	}
	t.Cleanup(func() { sharedHelperDir = previous })
}

func newStateRuntime(t *testing.T, options Options) *Runtime {
	t.Helper()
	prober, err := NewProber([]BaseURLEntry{{URL: "http://127.0.0.1:1"}})
	if err != nil {
		t.Fatal(err)
	}
	options.Store = StoreAt(options.DataDir)
	options.Prober = prober
	options.Manager = &fakeManager{}
	options.Collector = fakeCollector{}
	options.EnsureHelper = func(context.Context) error { return nil }
	options.Bridge = func(context.Context, string) (io.ReadWriteCloser, error) { return nil, ErrServiceUnsupported }
	options.Logger = slog.New(slog.DiscardHandler)
	agentRuntime, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	return agentRuntime
}

func idleLegacy(sessions int, known bool) supervisor.StateDirSteps {
	return supervisor.StateDirSteps{
		Inspect: func(context.Context, string) (supervisor.RunningHelper, error) {
			return supervisor.RunningHelper{ActivityKnown: known, Activity: supervisor.ActivitySnapshot{PTY: sessions}}, nil
		},
		Stop: func(context.Context, string, supervisor.RunningHelper) error { return nil },
	}
}

func TestRuntimeOnTheDefaultDataDirSharesTheHelperDir(t *testing.T) {
	dataDir, shared := filepath.Join(t.TempDir(), "agent"), filepath.Join(t.TempDir(), "shared")
	shareHelperDir(t, dataDir, shared)
	agentRuntime := newStateRuntime(t, Options{DataDir: dataDir})
	legacy := filepath.Join(dataDir, "durable", "supervisor")
	if agentRuntime.sharedDir != shared || agentRuntime.legacyDir != legacy || agentRuntime.currentStateDir() != legacy {
		t.Fatalf("shared %q legacy %q current %q", agentRuntime.sharedDir, agentRuntime.legacyDir, agentRuntime.currentStateDir())
	}
	// No helper runs in the previous directory: the shared one is used.
	agentRuntime.settleStateDir(context.Background())
	if agentRuntime.currentStateDir() != shared {
		t.Fatalf("current = %q, want the shared dir", agentRuntime.currentStateDir())
	}
}

func TestRuntimeWithAnExplicitDataDirKeepsItsOwnHelperDir(t *testing.T) {
	standard, shared := filepath.Join(t.TempDir(), "agent"), filepath.Join(t.TempDir(), "shared")
	shareHelperDir(t, standard, shared)
	other := filepath.Join(t.TempDir(), "other")
	agentRuntime := newStateRuntime(t, Options{DataDir: other})
	if agentRuntime.sharedDir != "" || agentRuntime.currentStateDir() != filepath.Join(other, "durable", "supervisor") {
		t.Fatalf("shared %q current %q", agentRuntime.sharedDir, agentRuntime.currentStateDir())
	}
	agentRuntime.settleStateDir(context.Background())
	if agentRuntime.currentStateDir() != filepath.Join(other, "durable", "supervisor") {
		t.Fatal("an isolated runtime moved")
	}
}

func TestRuntimeStateDirOptionStaysAuthoritative(t *testing.T) {
	dataDir, shared := filepath.Join(t.TempDir(), "agent"), filepath.Join(t.TempDir(), "shared")
	shareHelperDir(t, dataDir, shared)
	explicit := filepath.Join(t.TempDir(), "mine")
	agentRuntime := newStateRuntime(t, Options{DataDir: dataDir, StateDir: explicit})
	agentRuntime.settleStateDir(context.Background())
	if agentRuntime.currentStateDir() != explicit {
		t.Fatalf("current = %q, want %q", agentRuntime.currentStateDir(), explicit)
	}
	moving := explicit
	agentRuntime = newStateRuntime(t, Options{DataDir: dataDir, StateDirSource: func() string { return moving }})
	moving = filepath.Join(t.TempDir(), "moved")
	if agentRuntime.currentStateDir() != moving {
		t.Fatalf("current = %q, want the source's directory", agentRuntime.currentStateDir())
	}
}

func TestRuntimeWaitsInThePreviousDirWhileItRunsSessionsThenMoves(t *testing.T) {
	dataDir, shared := filepath.Join(t.TempDir(), "agent"), filepath.Join(t.TempDir(), "shared")
	shareHelperDir(t, dataDir, shared)
	agentRuntime := newStateRuntime(t, Options{DataDir: dataDir})
	legacy := filepath.Join(dataDir, "durable", "supervisor")
	agentRuntime.settleSteps = idleLegacy(3, true)
	agentRuntime.settleStateDir(context.Background())
	agentRuntime.computeStateDigest() // Run does this once after the first settle
	if agentRuntime.currentStateDir() != legacy {
		t.Fatalf("current = %q, want the previous dir while sessions run", agentRuntime.currentStateDir())
	}
	legacyDigest := agentRuntime.digestSnapshot()
	identity, err := currentIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := supervisor.StateDigest(legacy, identity); legacyDigest != want {
		t.Fatalf("digest %q, want the previous dir's %q", legacyDigest, want)
	}
	agentRuntime.settleSteps = idleLegacy(0, true)
	agentRuntime.settleStateDir(context.Background())
	if agentRuntime.currentStateDir() != shared {
		t.Fatalf("current = %q, want the shared dir", agentRuntime.currentStateDir())
	}
	if agentRuntime.digestSnapshot() == legacyDigest {
		t.Fatal("the digest did not follow the directory")
	}
	// Settled: later checks never go back, even if an older helper shows up.
	agentRuntime.settleSteps = idleLegacy(5, true)
	agentRuntime.settleStateDir(context.Background())
	if agentRuntime.currentStateDir() != shared {
		t.Fatal("moved back to the previous dir")
	}
}

func TestRuntimeAndDesktopReportTheSameDigestForTheSharedHelper(t *testing.T) {
	dataDir, shared := filepath.Join(t.TempDir(), "agent"), filepath.Join(t.TempDir(), "shared")
	shareHelperDir(t, dataDir, shared)
	agentRuntime := newStateRuntime(t, Options{DataDir: dataDir})
	agentRuntime.settleStateDir(context.Background())
	// The desktop's exposure runtime is handed the shared directory.
	desktop := newStateRuntime(t, Options{DataDir: t.TempDir(), StateDir: shared})
	desktop.computeStateDigest()
	if agentRuntime.digestSnapshot() == "" || agentRuntime.digestSnapshot() != desktop.digestSnapshot() {
		t.Fatalf("agent digest %q, desktop digest %q", agentRuntime.digestSnapshot(), desktop.digestSnapshot())
	}
}

func TestRuntimeMovingTheStateDirEndsTheControlChannelToReannounceTheDigest(t *testing.T) {
	dataDir, shared := filepath.Join(t.TempDir(), "agent"), filepath.Join(t.TempDir(), "shared")
	shareHelperDir(t, dataDir, shared)
	agentRuntime := newStateRuntime(t, Options{DataDir: dataDir})
	agentRuntime.settleSteps = idleLegacy(1, true)
	agentRuntime.settleStateDir(context.Background())
	agentRuntime.computeStateDigest()
	ctx, cancel := context.WithCancel(context.Background())
	agentRuntime.mu.Lock()
	agentRuntime.channelCancel = cancel
	agentRuntime.mu.Unlock()
	agentRuntime.settleSteps = idleLegacy(0, true)
	agentRuntime.settleStateDir(context.Background())
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the control channel was not ended")
	}
}

func TestStateDirSourceMoveRedialsWithTheNewDigestAndKeepsBridges(t *testing.T) {
	fake := newFakeFleetServer(t)
	var mu sync.Mutex
	var digests []string
	fake.setWSControl(func(conn *websocket.Conn, hello HelloMessage) {
		mu.Lock()
		digests = append(digests, hello.StateDigest)
		mu.Unlock()
		_ = writeServerMessage(conn, serverMessage{Type: "hello_ok"})
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	})
	first, second := filepath.Join(t.TempDir(), "legacy"), filepath.Join(t.TempDir(), "shared")
	var dirMu sync.Mutex
	dir := first
	source := func() string { dirMu.Lock(); defer dirMu.Unlock(); return dir }
	agentRuntime := newStateRuntime(t, Options{DataDir: t.TempDir(), StateDirSource: source})
	prober, err := NewProber([]BaseURLEntry{{URL: fake.server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	agentRuntime.prober = prober
	agentRuntime.deviceID, agentRuntime.secret = "dev", "secret"
	agentRuntime.jitter = func() float64 { return 0.5 }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agentRuntime.runCtx = ctx
	agentRuntime.desired.TerminalEnabled = true
	agentRuntime.computeStateDigest()
	bridgeCtx := make(chan context.Context, 1)
	agentRuntime.RegisterPurposeHandler("test", func(ctx context.Context, _ string) error {
		bridgeCtx <- ctx
		<-ctx.Done()
		return nil
	})
	agentRuntime.handlePurposeBridge("b1", "test")
	running := <-bridgeCtx

	done := make(chan struct{})
	go func() { defer close(done); agentRuntime.channelLoop(ctx) }()
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(digests) }
	waitFor(t, 5*time.Second, "first hello", func() bool { return count() == 1 })

	dirMu.Lock()
	dir = second
	dirMu.Unlock()
	agentRuntime.reconcileDigest() // what the periodic tick does
	waitFor(t, 10*time.Second, "redial with the new digest", func() bool { return count() == 2 })

	identity, err := currentIdentity()
	if err != nil {
		t.Fatal(err)
	}
	wantFirst, _ := supervisor.StateDigest(first, identity)
	wantSecond, _ := supervisor.StateDigest(second, identity)
	mu.Lock()
	got := append([]string(nil), digests...)
	mu.Unlock()
	if got[0] != wantFirst || got[1] != wantSecond {
		t.Fatalf("hello digests %v, want [%s %s]", got, wantFirst, wantSecond)
	}
	if running.Err() != nil || ctx.Err() != nil {
		t.Fatal("a digest change must not end bridges running on the runtime context")
	}
	cancel()
	<-done
	agentRuntime.loopWg.Wait()
}

func TestTerminalBridgeSettlesTheStateDirBeforeEnsuringTheHelper(t *testing.T) {
	dataDir, shared := filepath.Join(t.TempDir(), "agent"), filepath.Join(t.TempDir(), "shared")
	shareHelperDir(t, dataDir, shared)
	agentRuntime := newStateRuntime(t, Options{DataDir: dataDir})
	agentRuntime.desired.TerminalEnabled = true
	agentRuntime.runCtx = context.Background()
	seen := make(chan string, 1)
	agentRuntime.ensureHelper = func(context.Context) error {
		seen <- agentRuntime.currentStateDir()
		return errors.New("stop here")
	}
	agentRuntime.handleTerminalBridge("b1", PurposeTerminal)
	agentRuntime.loopWg.Wait()
	if got := <-seen; got != shared {
		t.Fatalf("helper ensured in %q, want the shared dir %q", got, shared)
	}
}

func TestStatusStateDirSaysWhySharedOrIsolated(t *testing.T) {
	standard, shared := filepath.Join(t.TempDir(), "agent"), filepath.Join(t.TempDir(), "shared")
	shareHelperDir(t, standard, shared)
	if dir, note := statusStateDir(context.Background(), standard); dir != shared || note == "" {
		t.Fatalf("default data dir: %q (%q)", dir, note)
	}
	custom := filepath.Join(t.TempDir(), "custom")
	dir, note := statusStateDir(context.Background(), custom)
	if dir != filepath.Join(custom, "durable", "supervisor") || note == "" {
		t.Fatalf("custom data dir: %q (%q)", dir, note)
	}
}

// The digest moves after the control hello was built but before the channel's cancel is registered: only
// the re-check after registration can notice, so the channel must still redial with the new digest.
func TestDigestChangeBetweenHelloAndCancelRegistrationStillRedials(t *testing.T) {
	fake := newFakeFleetServer(t)
	hellos := make(chan string, 8)
	fake.setWSControl(func(conn *websocket.Conn, hello HelloMessage) {
		hellos <- hello.StateDigest
		_ = writeServerMessage(conn, serverMessage{Type: "hello_ok"})
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	})
	first, second := filepath.Join(t.TempDir(), "legacy"), filepath.Join(t.TempDir(), "shared")
	var dirMu sync.Mutex
	dir := first
	agentRuntime := newStateRuntime(t, Options{DataDir: t.TempDir(), StateDirSource: func() string {
		dirMu.Lock()
		defer dirMu.Unlock()
		return dir
	}})
	prober, err := NewProber([]BaseURLEntry{{URL: fake.server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	agentRuntime.prober = prober
	agentRuntime.deviceID, agentRuntime.secret = "dev", "secret"
	agentRuntime.jitter = func() float64 { return 0.5 }
	agentRuntime.computeStateDigest()
	var once sync.Once
	agentRuntime.beforeChannelRegister = func() {
		once.Do(func() {
			dirMu.Lock()
			dir = second
			dirMu.Unlock()
			agentRuntime.reconcileDigest() // no cancel is registered yet, so nothing ends the channel here
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); agentRuntime.channelLoop(ctx) }()
	defer func() { cancel(); <-done }()

	identity, err := currentIdentity()
	if err != nil {
		t.Fatal(err)
	}
	wantFirst, _ := supervisor.StateDigest(first, identity)
	wantSecond, _ := supervisor.StateDigest(second, identity)
	timeout := time.After(10 * time.Second)
	var got []string
	for len(got) < 2 {
		select {
		case digest := <-hellos:
			got = append(got, digest)
		case <-timeout:
			t.Fatalf("hellos %v: no redial with the new digest", got)
		}
	}
	if got[0] != wantFirst || got[1] != wantSecond {
		t.Fatalf("hello digests %v, want [%s %s]", got, wantFirst, wantSecond)
	}
}
