package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func newReleaseManager(t *testing.T) *Manager {
	t.Helper()
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func connectForRelease(t *testing.T, manager *Manager, id string) *Session {
	t.Helper()
	connected, err := manager.Connect(context.Background(), Asset{ID: id, Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	return connected
}

func giveHostPersistentTerminal(session *Session) {
	provider := newFakeDurableProvider()
	provider.records["pt-1"] = &fakeDurableRecord{identity: 1}
	session.mu.Lock()
	generation, transport := session.generation, session.transport
	session.mu.Unlock()
	session.durableMu.Lock()
	session.durableProvider, session.durableGen, session.durableTransport = provider, generation, transport
	session.durableMu.Unlock()
}

func assertReleased(t *testing.T, manager *Manager, connected *Session, keepDurable, want bool) {
	t.Helper()
	released, err := manager.ReleaseIdleFor(WithOpenAccess(context.Background()), connected.ID, keepDurable)
	if err != nil {
		t.Fatal(err)
	}
	if released != want {
		t.Fatalf("ReleaseIdleFor(keepDurable=%v) = %v, want %v", keepDurable, released, want)
	}
	_, lookupErr := manager.Session(connected.ID)
	if gone := errors.Is(lookupErr, ErrSessionNotFound); gone != want {
		t.Fatalf("session removed = %v (lookup error %v), want %v", gone, lookupErr, want)
	}
	if !want {
		if status := connected.Info().Status; status != StatusConnected {
			t.Fatalf("kept session status = %s, want connected", status)
		}
	}
}

func TestReleaseIdleRemovesAnUnusedSession(t *testing.T) {
	for _, keepDurable := range []bool{true, false} {
		manager := newReleaseManager(t)
		assertReleased(t, manager, connectForRelease(t, manager, "unused"), keepDurable, true)
	}
}

func TestReleaseIdleKeepsASessionAProbeUses(t *testing.T) {
	manager := newReleaseManager(t)
	connected := connectForRelease(t, manager, "forwarded")
	manager.AddSessionInUseProbe(func(id string) bool { return id == connected.ID })
	assertReleased(t, manager, connected, true, false)
	assertReleased(t, manager, connected, false, false)
}

func TestReleaseIdleKeepsMonitoringPersistentTerminalsUnlessAskedToDisconnect(t *testing.T) {
	manager := newReleaseManager(t)
	connected := connectForRelease(t, manager, "persistent")
	giveHostPersistentTerminal(connected)
	assertReleased(t, manager, connected, true, false)
	assertReleased(t, manager, connected, false, true)
}

func TestReleaseIdleKeepsAWatchedTerminalAndReleasesABackgroundOne(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	tabID := "0123456789abcdef0123456789abcdef"
	if _, err := provider.Create(t.Context(), base.DurableCreateOptions{ID: tabID, Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		DurableResolver: &countingResolver{provider: provider}, OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected := connectForRelease(t, manager, "watched")
	if _, err := manager.RecoverRemoteDurable(t.Context(), tabID, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "watching", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatal(err)
	}
	// Another client still shows the terminal, so even a requested disconnect keeps the session.
	assertReleased(t, manager, connected, false, false)
	if err := manager.BackgroundTab(t.Context(), tabID, "client-a"); err != nil {
		t.Fatal(err)
	}
	// In the background the terminal keeps the connection unless the user asked to disconnect.
	assertReleased(t, manager, connected, true, false)
	assertReleased(t, manager, connected, false, true)
	provider.mu.Lock()
	killed := provider.records[tabID].killed
	provider.mu.Unlock()
	if killed != 0 {
		t.Fatalf("releasing the session killed the background process %d times", killed)
	}
}

func TestIdleSweepKeepsASessionAProbeUses(t *testing.T) {
	clock := newSessionTestClock()
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Now: clock.Now, IdleTimeout: time.Minute,
	})
	t.Cleanup(func() { _ = manager.Close() })
	used := connectForRelease(t, manager, "used")
	unused := connectForRelease(t, manager, "unused")
	manager.AddSessionInUseProbe(func(id string) bool { return id == used.ID })
	clock.Advance(2 * time.Minute)
	if err := manager.SweepIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := used.Info().Status; status != StatusConnected {
		t.Fatalf("session in use: status = %s, want connected", status)
	}
	if status := unused.Info().Status; status != StatusDisconnected {
		t.Fatalf("unused session: status = %s, want disconnected", status)
	}
}
