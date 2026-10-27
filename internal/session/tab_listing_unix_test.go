//go:build unix

package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// TestListTabsCarriesHelperAttentionState lists a durable tab served by a
// real helper and expects the state the helper pushes to the attachment.
func TestListTabsCarriesHelperAttentionState(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "supervisor")
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-session-state-")
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
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Durable: provider})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(ctx, Asset{ID: "state-local", Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tabID := ids.New()
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "state-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh", "-c", "printf ready; sleep 30"}},
	}); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, manager, tabID, "ready")
	deadline := time.Now().Add(30 * time.Second)
	for {
		state := listedTab(t, manager, tabID).State
		if state != "" {
			switch state {
			case base.SessionRunning, base.SessionNeedsMe, base.SessionJustFinished, base.SessionIdle, base.SessionExited:
			default:
				t.Fatalf("listed state %q is not a helper state", state)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the listing never carried the helper state")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
