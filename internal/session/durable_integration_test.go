//go:build darwin || linux

package session

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/durable"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
)

func TestRealTmuxSessionRestartRecoveryLifecycleAndReplay(t *testing.T) {
	if os.Getenv("NEXTERM_TMUX_INTEGRATION") != "1" {
		t.Skip("real tmux session recovery skipped: set NEXTERM_TMUX_INTEGRATION=1 (requires tmux on PATH)")
	}
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("real tmux test opted in but tmux is unavailable: %v", err)
	}
	config := realSessionDurableConfig(t, binary, "session-recovery")
	backend, err := durable.New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	tabID := ids.New()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = backend.Kill(cleanupCtx, tabID)
	})

	first, firstSession := newRealDurableManager(t, backend, "first")
	script := `printf 'boot\n'
while IFS= read -r line; do
  case "$line" in
    later) sleep 0.2; printf 'detached\n' ;;
    *) printf 'in:%s\n' "$line" ;;
  esac
done
`
	info, err := first.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: firstSession.ID, ClientID: "client-a", ChannelID: "first-channel", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh", "-c", script}},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDurableFor(t, func() bool {
		raw, err := first.RawDump(info.ID, 1<<20)
		return err == nil && bytes.Contains(raw, []byte("boot\r\n"))
	})
	if err := first.Write(ctx, info.ID, "client-a", []byte("later\r")); err != nil {
		t.Fatal(err)
	}
	initial := singleRealDurableInfo(ctx, t, backend, tabID)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)

	backend, err = durable.New(config)
	if err != nil {
		t.Fatal(err)
	}
	assertRealDurableIdentity(t, initial, singleRealDurableInfo(ctx, t, backend, tabID), false)
	second, secondSession := newRealDurableManager(t, backend, "second")
	recovered, err := second.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: secondSession.ID, ClientID: "client-b", ChannelID: "second-channel", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh", "-c", "exit 99"}, Recover: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitDurableFor(t, func() bool {
		raw, err := second.RawDump(tabID, 1<<20)
		return err == nil && bytes.Contains(raw, []byte("detached\r\n"))
	})
	recoveredRaw, err := second.RawDump(tabID, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(recoveredRaw, []byte("boot\r\n")) != 1 || bytes.Count(recoveredRaw, []byte("detached\r\n")) != 1 {
		t.Fatalf("recovered recording is not exactly once: %q", recoveredRaw)
	}
	if recovered.ID != initial.ID {
		t.Fatalf("recovered ID = %q, want %q", recovered.ID, initial.ID)
	}
	assertRealDurableIdentity(t, initial, singleRealDurableInfo(ctx, t, backend, tabID), false)

	beforeAttach := append([]byte(nil), recoveredRaw...)
	if _, err := second.AttachTab(ctx, tabID, AttachOptions{ClientID: "client-c", ChannelID: "replay-channel", ReplayBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	afterAttach, err := second.RawDump(tabID, 1<<20)
	if err != nil || !bytes.Equal(afterAttach, beforeAttach) {
		t.Fatalf("RPC attach fed the recording twice: before=%q after=%q err=%v", beforeAttach, afterAttach, err)
	}
	receiver := bindTestReceiver(t, second, "replay-channel")
	assertFrameData(t, receiver, replayClear)
	replay := receiveTestFrame(t, receiver)
	if bytes.Count(replay.Data, []byte("boot\r\n")) != 1 || bytes.Count(replay.Data, []byte("detached\r\n")) != 1 {
		t.Fatalf("hub replay is not exactly once: %q", replay.Data)
	}
	if err := second.DetachAll(tabID); err != nil {
		t.Fatal(err)
	}
	assertRealDurableIdentity(t, initial, singleRealDurableInfo(ctx, t, backend, tabID), false)
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	assertRealDurableIdentity(t, initial, singleRealDurableInfo(ctx, t, backend, tabID), false)

	backend, err = durable.New(config)
	if err != nil {
		t.Fatal(err)
	}
	third, thirdSession := newRealDurableManager(t, backend, "third")
	if _, err := third.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: thirdSession.ID, ClientID: "client-d", ChannelID: "third-channel", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Recover: true},
	}); err != nil {
		t.Fatal(err)
	}
	waitDurableFor(t, func() bool {
		raw, err := third.RawDump(tabID, 1<<20)
		return err == nil && bytes.Contains(raw, []byte("detached\r\n"))
	})
	if err := third.CloseTab(tabID); err != nil {
		t.Fatal(err)
	}
	infos, err := backend.List(ctx)
	if err != nil || len(infos) != 0 {
		t.Fatalf("explicit close left durable sessions: infos=%+v err=%v", infos, err)
	}
	t.Logf("recovered tab %s across two application restarts with unchanged pane PID %d and unique replay", tabID, initial.PID)
}

func TestRealTmuxSessionExitRecoveryUsesDiscovery(t *testing.T) {
	if os.Getenv("NEXTERM_TMUX_INTEGRATION") != "1" {
		t.Skip("real tmux exit recovery skipped: set NEXTERM_TMUX_INTEGRATION=1 (requires tmux on PATH)")
	}
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("real tmux test opted in but tmux is unavailable: %v", err)
	}
	config := realSessionDurableConfig(t, binary, "session-exit")
	backend, err := durable.New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tabID := ids.New()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = backend.Kill(cleanupCtx, tabID)
	})
	exits := make(chan ExitEvent, 4)
	emitter := EmitterFunc(func(_ context.Context, event Event) error {
		if event.Topic == TopicTerminalExit {
			exits <- event.Payload.(ExitEvent)
		}
		return nil
	})
	connector := newFakeConnector()
	first := NewManager(Config{Connector: connector, Durable: NewDurableProvider(backend), Emitter: emitter})
	t.Cleanup(func() { _ = first.Close() })
	firstSession, err := first.Connect(ctx, Asset{ID: "exit-first", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: firstSession.ID, ClientID: "client-a", ChannelID: "exit-first-channel", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh", "-c", "printf 'final-without-newline'; exit 7"}},
	}); err != nil {
		t.Fatal(err)
	}
	assertRealDurableExit(t, first, exits, tabID, 7)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	backend, err = durable.New(config)
	if err != nil {
		t.Fatal(err)
	}
	second := NewManager(Config{Connector: newFakeConnector(), Durable: NewDurableProvider(backend), Emitter: emitter})
	t.Cleanup(func() { _ = second.Close() })
	secondSession, err := second.Connect(ctx, Asset{ID: "exit-second", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: secondSession.ID, ClientID: "client-b", ChannelID: "exit-second-channel", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh", "-c", "exit 99"}, Recover: true},
	}); err != nil {
		t.Fatal(err)
	}
	assertRealDurableExit(t, second, exits, tabID, 7)
	raw, err := second.RawDump(tabID, 1<<20)
	if err != nil || bytes.Count(raw, []byte("final-without-newline")) != 1 {
		t.Fatalf("dead recovery replay = %q, err=%v", raw, err)
	}
	if err := second.CloseTab(tabID); err != nil {
		t.Fatal(err)
	}
}

func TestSessionDurableMissingTmuxIsExplicit(t *testing.T) {
	root := t.TempDir()
	_, err := durable.New(durable.Config{
		Binary: filepath.Join(root, "missing-tmux"), SocketPath: filepath.Join(root, "run", "tmux.sock"), StateDir: filepath.Join(root, "state"),
	})
	if !errors.Is(err, durable.ErrUnavailable) {
		t.Fatalf("missing tmux error = %v, want ErrUnavailable", err)
	}
}

func realSessionDurableConfig(t *testing.T, binary, name string) durable.Config {
	t.Helper()
	root := t.TempDir()
	runDir, err := os.MkdirTemp("/tmp", "nx-session-durable-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	return durable.Config{
		Binary: binary, SocketPath: filepath.Join(runDir, "tmux.sock"), StateDir: filepath.Join(root, "state"),
		Namespace: name, CommandTimeout: 3 * time.Second,
	}
}

func newRealDurableManager(t *testing.T, backend *durable.Backend, assetID string) (*Manager, *Session) {
	t.Helper()
	manager := NewManager(Config{Connector: newFakeConnector(), Durable: NewDurableProvider(backend)})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: assetID, Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	return manager, connected
}

func singleRealDurableInfo(ctx context.Context, t *testing.T, backend *durable.Backend, id string) durable.Info {
	t.Helper()
	infos, err := backend.List(ctx)
	if err != nil || len(infos) != 1 || infos[0].ID != id {
		t.Fatalf("durable discovery = %+v, err=%v", infos, err)
	}
	return infos[0]
}

func assertRealDurableIdentity(t *testing.T, expected, actual durable.Info, dead bool) {
	t.Helper()
	if !sameDurableIdentity(expected, actual) || actual.Dead != dead {
		t.Fatalf("durable identity changed:\nfirst  %+v\nsecond %+v", expected, actual)
	}
}

func assertRealDurableExit(t *testing.T, manager *Manager, exits <-chan ExitEvent, tabID string, code int) {
	t.Helper()
	waitDurableFor(t, func() bool { return infoExited(manager, tabID) })
	select {
	case event := <-exits:
		if event.TabID != tabID || event.ExitCode == nil || *event.ExitCode != code {
			t.Fatalf("exit event = %+v, want tab %s code %d", event, tabID, code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("missing durable exit event")
	}
}

func waitDurableFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("durable condition did not become true")
}
