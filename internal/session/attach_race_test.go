package session

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/hub"
)

func TestDetachDuringBlockedReplayDoesNotResurrectSubscriber(t *testing.T) {
	bus := hub.New(hub.Options{QueueFrames: 1})
	t.Cleanup(func() { _ = bus.Close() })
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), Hub: bus})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	tab.terminal.Feed(make([]byte, 256<<10))
	attached := make(chan error, 1)
	go func() {
		_, err := manager.AttachTab(context.Background(), tab.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1", ReplayBytes: -1})
		attached <- err
	}()
	waitFor(t, func() bool { return bus.Stats().QueuedFrames == 1 })
	if err := manager.DetachChannel("b-1"); err != nil {
		t.Fatal(err)
	}
	if err := <-attached; err == nil {
		t.Fatal("detach during replay allowed attach to succeed")
	}
	if got := tab.Info().Subscribers; got != 1 {
		t.Fatalf("detached replay left %d subscribers, want only the original", got)
	}
	manager.mu.Lock()
	owner := manager.channelTabs["b-1"]
	manager.mu.Unlock()
	if owner != "" {
		t.Fatalf("detached replay retained channel owner %q", owner)
	}
}
