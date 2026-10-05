//go:build unix

package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/supervisor"
)

// TestSupervisorDurableRestartRecovery replays the production helper shape:
// the daemon outlives the app, and a restarted manager recovers the durable
// tab through the remote provider with replay, input, and kill intact.
func TestSupervisorDurableRestartRecovery(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "supervisor")
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-session-daemon-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	server, err := supervisor.NewServer(instance, filepath.Join(runDir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		_ = instance.Close()
	})
	provider := supervisor.NewRemoteProvider(supervisor.NewClient(server.SocketPath(), stateDir))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tabID := ids.New()
	first := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Durable: provider})
	connected, err := first.Connect(ctx, Asset{ID: "restart-local", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "restart-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Write(ctx, tabID, "client-a", []byte("echo before-restart-mark''er\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, first, tabID, "before-restart-marker")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Durable: provider})
	t.Cleanup(func() { _ = second.Close() })
	reconnected, err := second.Connect(ctx, Asset{ID: "restart-local-2", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: reconnected.ID, ClientID: "client-a", ChannelID: "restart-2", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Recover: true},
	}); err != nil {
		t.Fatal(err)
	}
	replayed := waitForTabOutput(t, second, tabID, "before-restart-marker")
	if count := strings.Count(replayed, "before-restart-marker"); count != 1 {
		t.Fatalf("replay contains the marker %d times: %q", count, replayed)
	}
	if err := second.Write(ctx, tabID, "client-a", []byte("echo after-restart-mark''er\n")); err != nil {
		t.Fatal(err)
	}
	output := waitForTabOutput(t, second, tabID, "after-restart-marker")
	if count := strings.Count(output, "before-restart-marker"); count != 1 {
		t.Fatalf("pre-restart output duplicated after new input %d times: %q", count, output)
	}
	if err := second.CloseTab(tabID); err != nil {
		t.Fatal(err)
	}
	ids, err := provider.ListDurable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == tabID {
			t.Fatal("killed durable tab is still enumerated by the daemon")
		}
	}
}

func waitForTabOutput(t *testing.T, manager *Manager, tabID, marker string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, err := manager.RawDump(tabID, 1<<20)
		if err != nil {
			t.Fatalf("RawDump: %v", err)
		}
		if strings.Contains(string(raw), marker) {
			return string(raw)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in tab output: %q", marker, raw)
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-manager.ctx.Done():
			t.Fatal("manager closed while waiting for tab output")
		}
	}
}
