package durable

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

func TestNewMissingTmuxIsUnavailable(t *testing.T) {
	root := t.TempDir()
	_, err := New(Config{
		Binary:     filepath.Join(root, "missing-tmux"),
		SocketPath: filepath.Join(root, "run", "tmux.sock"),
		StateDir:   filepath.Join(root, "state"),
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("New error = %v, want ErrUnavailable", err)
	}
}

func TestNewRejectsSharedDirectories(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "tmux-test-bin")
	if err := os.WriteFile(binary, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(root, "shared")
	if err := os.Mkdir(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := New(Config{Binary: binary, SocketPath: filepath.Join(shared, "tmux.sock"), StateDir: shared})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("New error = %v, want ErrInvalidInput", err)
	}
}

func TestNewCreatesPrivateSocketDirectoryUnderWorldAccessibleTemp(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "tmp")
	if err := os.Mkdir(shared, 0o1777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o1777); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "tmux-test-bin")
	if err := os.WriteFile(binary, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	socketDir := filepath.Join(shared, "nexterm-durable-0123456789abcdef")
	if _, err := New(Config{
		Binary:     binary,
		SocketPath: filepath.Join(socketDir, "d.sock"),
		StateDir:   filepath.Join(root, "state"),
	}); err != nil {
		t.Fatalf("New under world-accessible temp parent: %v", err)
	}
	info, err := os.Lstat(socketDir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("socket directory is not a real directory: %v", info.Mode())
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("socket directory is accessible by group or other users: %o", info.Mode().Perm())
	}
}

func TestNewRejectsSymlinkedSocketDirectory(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "tmux-test-bin")
	if err := os.WriteFile(binary, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "nexterm-durable-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := New(Config{
		Binary:     binary,
		SocketPath: filepath.Join(link, "d.sock"),
		StateDir:   filepath.Join(root, "state"),
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("New error = %v, want ErrInvalidInput", err)
	}
}

func TestNewRejectsSymlinkedStateDirectory(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "tmux-test-bin")
	if err := os.WriteFile(binary, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "state-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := New(Config{
		Binary:     binary,
		SocketPath: filepath.Join(root, "run", "d.sock"),
		StateDir:   link,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("New error = %v, want ErrInvalidInput", err)
	}
}

func TestListDiscoversOnlyOwnedPrimaryPanes(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	first := unitRecord(backend, ids.New())
	second := unitRecord(backend, ids.New())
	second.info.SessionID = "$2"
	second.info.PaneID = "%2"
	second.windowID = "@2"
	second.paneOption = "%2"
	second.windowOption = "@2"
	second.info.Dead = true
	second.info.ExitCode = intPointer(17)
	second.deadStatus = "17"
	second.recordingLive = false
	extraPane := first
	extraPane.info.PaneID = "%99"
	foreign := unitRecord(backend, ids.New())
	foreign.owner = ""
	foreign.namespace = ""
	otherNamespace := unitRecord(backend, ids.New())
	otherNamespace.namespace = "another-namespace"
	installRecords(backend, runner, extraPane, foreign, second, first, otherNamespace)

	infos, err := backend.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("List returned %d infos, want 2: %+v", len(infos), infos)
	}
	byID := make(map[string]Info, len(infos))
	for _, info := range infos {
		byID[info.ID] = info
	}
	if _, ok := byID[first.info.ID]; !ok {
		t.Fatalf("first owned session missing from %+v", infos)
	}
	deadInfo, ok := byID[second.info.ID]
	if !ok || !deadInfo.Dead || deadInfo.ExitCode == nil || *deadInfo.ExitCode != 17 {
		t.Fatalf("dead info = %+v, present = %v", deadInfo, ok)
	}
	calls := runner.recordedCalls()
	if len(calls) != 1 || !reflect.DeepEqual(calls[0][:3], []string{"list-panes", "-s", "-F"}) {
		t.Fatalf("discovery calls = %+v", calls)
	}
}

func TestListRejectsMissingOwnedPrimaryPane(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	current := unitRecord(backend, ids.New())
	current.info.PaneID = "%99"
	installRecords(backend, runner, current)
	if _, err := backend.List(context.Background()); !errors.Is(err, ErrIdentity) {
		t.Fatalf("List error = %v, want ErrIdentity", err)
	}
}

func TestMalformedDiscoveryFailsClosed(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	runner.handler = func(context.Context, ...string) ([]byte, error) {
		return []byte("not-a-discovery-record\n"), nil
	}
	if _, err := backend.List(context.Background()); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("List error = %v, want malformed record error", err)
	}
}

func TestStaleSocketDiscoversNoLiveSessions(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	runner.handler = func(context.Context, ...string) ([]byte, error) {
		return nil, &commandError{op: "list-panes", stderr: "no server running on " + backend.socketPath, err: errors.New("exit status 1")}
	}
	infos, err := backend.List(context.Background())
	if err != nil || len(infos) != 0 {
		t.Fatalf("List on stale socket = %+v, %v", infos, err)
	}
	if _, err := backend.Attach(context.Background(), ids.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Attach on stale socket = %v, want ErrNotFound", err)
	}
}

func TestKillRefusesForeignSession(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	foreign := unitRecord(backend, ids.New())
	foreign.owner = ""
	foreign.namespace = ""
	installRecords(backend, runner, foreign)
	if err := backend.Kill(context.Background(), foreign.info.ID); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("Kill error = %v, want ErrNotOwned", err)
	}
	for _, call := range runner.recordedCalls() {
		if call[0] == "kill-session" {
			t.Fatalf("foreign kill issued: %+v", call)
		}
	}
}

func TestKillOwnedSessionAndArtifacts(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	current := unitRecord(backend, ids.New())
	installRecords(backend, runner, current)
	if err := os.Mkdir(backend.sessionDir(current.info.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backend.recordingPath(current.info.ID), []byte("raw"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backend.gatePath(current.info.ID), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backend.recorderDonePath(current.info.ID), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backend.Kill(context.Background(), current.info.ID); err != nil {
		t.Fatal(err)
	}
	calls := runner.recordedCalls()
	last := calls[len(calls)-1]
	if !reflect.DeepEqual(last, []string{"kill-session", "-t", current.info.SessionID}) {
		t.Fatalf("kill call = %+v", last)
	}
	for _, path := range []string{backend.recordingPath(current.info.ID), backend.gatePath(current.info.ID)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("artifact %s still exists: %v", path, err)
		}
	}
}

func TestAttachRequiresContinuousRecording(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	current := unitRecord(backend, ids.New())
	current.recordingLive = false
	installRecords(backend, runner, current)
	if _, err := backend.Attach(context.Background(), current.info.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Attach error = %v, want ErrUnavailable", err)
	}
}

func TestLaunchCommandQuotesArguments(t *testing.T) {
	backend, _ := newUnitBackend(t)
	id := ids.New()
	launch, err := backend.launchCommand(id, CreateOptions{Command: []string{"/bin/echo", "hello world", "a'b"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(launch, "exec /bin/sh -c ") {
		t.Fatalf("launch is not a supervised POSIX shell: %s", launch)
	}
	gate := shellQuote(backend.gatePath(id))
	rawCommand := "'/bin/echo' 'hello world' 'a'\"'\"'b'"
	script := backend.supervisorScript(id, rawCommand)
	closeRecording := shellQuote(backend.binary) + " -f " + shellQuote(os.DevNull) + " -S " + shellQuote(backend.socketPath) + " pipe-pane -t \"$TMUX_PANE\""
	for _, want := range []string{
		"while [ ! -f " + gate + " ]; do sleep 0.05; done; rm -f " + gate,
		rawCommand,
		"trap '_nexterm_durable_on_signal INT 2' INT",
		"trap '_nexterm_durable_on_signal TERM 15' TERM",
		closeRecording,
		"kill -s \"$1\" 0",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("supervisor script missing %q:\n%s", want, script)
		}
	}
}

func TestCreateValidatesBeforeStartingTmux(t *testing.T) {
	backend, runner := newUnitBackend(t)
	tests := []CreateOptions{
		{ID: "not-a-valid-id"},
		{Command: []string{""}},
		{Command: []string{"/bin/sh", "bad\x00arg"}},
		{Env: []string{"missing-equals"}},
		{Cols: maxDimension + 1},
		{Rows: maxDimension + 1},
	}
	for _, options := range tests {
		if _, err := backend.Create(context.Background(), options); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Create(%+v) error = %v, want ErrInvalidInput", options, err)
		}
	}
	if calls := runner.recordedCalls(); len(calls) != 0 {
		t.Fatalf("validation invoked tmux: %+v", calls)
	}
}

func TestCloneInfoExitCode(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	current := unitRecord(backend, ids.New())
	current.info.Dead = true
	current.info.ExitCode = intPointer(3)
	current.deadStatus = "3"
	current.recordingLive = false
	installRecords(backend, runner, current)
	infos, err := backend.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	*infos[0].ExitCode = 99
	if *current.info.ExitCode != 3 {
		t.Fatal("List aliased internal exit status")
	}
}

func TestListAcceptsSignaledPaneWithoutExitStatus(t *testing.T) {
	backend, runner := newUnitBackend(t)
	touchUnitSocket(t, backend)
	current := unitRecord(backend, ids.New())
	current.info.Dead = true
	current.info.ExitCode = nil
	current.info.Signal = "int"
	current.deadStatus = ""
	installRecords(backend, runner, current)
	infos, err := backend.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || !infos[0].Dead || infos[0].ExitCode != nil || infos[0].Signal != "int" {
		t.Fatalf("signaled discovery = %+v", infos)
	}
}

func intPointer(value int) *int { return &value }

func TestOwnedIdentityComparison(t *testing.T) {
	backend, _ := newUnitBackend(t)
	current := unitRecord(backend, ids.New())
	other := current
	other.info.CreatedAt = other.info.CreatedAt.Add(time.Second)
	if sameIdentity(current.info, other.info) {
		t.Fatal("different creation identities compare equal")
	}
}
