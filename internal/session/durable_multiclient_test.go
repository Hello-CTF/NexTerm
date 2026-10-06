//go:build unix

package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/supervisor"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

// TestRemoteDurableTwoManagersShareSession proves the multi-client contract at
// the session layer: two independent managers (two app instances on different
// machines, each with its own SSH transport) resolve their own remote durable
// provider against the same helper daemon and share one durable session -
// discovery by listing, replay for the late joiner, live fan-out to both,
// detach without kill, controller recovery after reattach, reconnect without
// replay duplication, and cross-client kill visibility.
func TestRemoteDurableTwoManagersShareSession(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "supervisor")
	instance, err := supervisor.New(supervisor.Config{StateDir: stateDir, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp("/tmp", "nx-session-shared-")
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
	providerA := supervisor.NewRemoteProvider(supervisor.NewClient(server.SocketPath(), stateDir))
	providerB := supervisor.NewRemoteProvider(supervisor.NewClient(server.SocketPath(), stateDir))
	var resolvesA, resolvesB atomic.Int32
	resolverA := DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) {
		resolvesA.Add(1)
		return providerA, nil
	})
	resolverB := DurableResolverFunc(func(context.Context, base.Transport) (base.DurableProvider, error) {
		resolvesB.Add(1)
		return providerB, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	managerA := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolverA})
	t.Cleanup(func() { _ = managerA.Close() })
	managerB := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolverB})
	t.Cleanup(func() { _ = managerB.Close() })

	tabID := ids.New()
	sessionA, err := managerA.Connect(ctx, Asset{ID: "ssh-shared-a", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managerA.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: sessionA.ID, ClientID: "client-a", ChannelID: "shared-a1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := managerA.Write(ctx, tabID, "client-a", []byte("echo shared-''one\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, managerA, tabID, "shared-one")

	if err := managerA.Reconnect(ctx, sessionA.ID); err != nil {
		t.Fatal(err)
	}
	waitForSessionStatus(t, managerA, sessionA.ID, StatusConnected)
	if resolvesA.Load() != 2 {
		t.Fatalf("resolver calls after reconnect = %d; want 2", resolvesA.Load())
	}
	if dump := waitForTabOutput(t, managerA, tabID, "shared-one"); strings.Count(dump, "shared-one") != 1 {
		t.Fatalf("reconnect duplicated the replay: %q", dump)
	}
	if err := managerA.Write(ctx, tabID, "client-a", []byte("echo shared-''two\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, managerA, tabID, "shared-two")

	if _, err := managerB.Connect(ctx, Asset{ID: "ssh-shared-b", Kind: KindSSH}); err != nil {
		t.Fatal(err)
	}
	recovered, err := managerB.RecoverRemoteDurable(ctx, tabID, OpenTabOptions{ClientID: "client-b", ChannelID: "shared-b1", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ID != tabID {
		t.Fatalf("recovered tab = %+v; want the shared session %s", recovered, tabID)
	}
	if resolvesB.Load() != 1 {
		t.Fatalf("second manager resolver calls = %d; want 1", resolvesB.Load())
	}
	if dump := waitForTabOutput(t, managerB, tabID, "shared-two"); strings.Count(dump, "shared-one") != 1 || strings.Count(dump, "shared-two") != 1 {
		t.Fatalf("late joiner replay = %q", dump)
	}

	if err := managerA.Write(ctx, tabID, "client-a", []byte("echo shared-''three\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, managerA, tabID, "shared-three")
	waitForTabOutput(t, managerB, tabID, "shared-three")

	if err := managerA.DetachAll(tabID); err != nil {
		t.Fatal(err)
	}
	listed, err := providerB.ListDurable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0] != tabID {
		t.Fatalf("detach killed the shared session: %v", listed)
	}
	if err := managerB.Write(ctx, tabID, "client-b", []byte("echo shared-''four\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, managerB, tabID, "shared-four")
	tabA, err := managerA.Tab(tabID)
	if err != nil {
		t.Fatalf("detached tab vanished: %v", err)
	}
	if tabA.Info().Exited {
		t.Fatal("detached tab reported exited")
	}

	if _, err := managerA.AttachTab(ctx, tabID, AttachOptions{ClientID: "client-a", ChannelID: "shared-a2"}); err != nil {
		t.Fatal(err)
	}
	if err := managerA.Write(ctx, tabID, "client-a", []byte("echo shared-''five\n")); err != nil {
		t.Fatal(err)
	}
	waitForTabOutput(t, managerA, tabID, "shared-five")
	waitForTabOutput(t, managerB, tabID, "shared-five")

	if err := managerB.CloseTab(tabID); err != nil {
		t.Fatal(err)
	}
	listed, err = providerA.ListDurable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("close on one manager did not kill the shared session: %v", listed)
	}
	waitForTabExit(t, managerA, tabID)
}

func waitForSessionStatus(t *testing.T, manager *Manager, id string, status Status) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, info := range manager.ListSessions() {
			if info.ID == id && info.Status == status {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s never reached status %s", id, status)
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-manager.ctx.Done():
			t.Fatal("manager closed while waiting for the session status")
		}
	}
}

func waitForTabExit(t *testing.T, manager *Manager, tabID string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		tab, err := manager.Tab(tabID)
		if err != nil {
			t.Fatalf("tab %s vanished before its exit was observed: %v", tabID, err)
		}
		if tab.Info().Exited {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("tab never observed the peer kill")
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-manager.ctx.Done():
			t.Fatal("manager closed while waiting for the tab exit")
		}
	}
}
