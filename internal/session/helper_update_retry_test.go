package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type updateWaitingDurableProvider struct {
	*listingDurableProvider
	waiting bool
}

func (p *updateWaitingDurableProvider) UpdateWaiting() bool { return p.waiting }

// sequenceResolver hands out its providers in order and repeats the last one.
type sequenceResolver struct {
	mu        sync.Mutex
	providers []base.DurableProvider
	calls     int
	called    chan struct{}
}

func (r *sequenceResolver) ResolveDurable(context.Context, base.Transport) (base.DurableProvider, error) {
	r.mu.Lock()
	index := min(r.calls, len(r.providers)-1)
	r.calls++
	r.mu.Unlock()
	r.called <- struct{}{}
	return r.providers[index], nil
}

func (r *sequenceResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func openSSHDurableTab(t *testing.T, manager *Manager, sessionID, channel string) TabInfo {
	t.Helper()
	tab, err := manager.OpenTab(t.Context(), OpenTabOptions{
		SessionID: sessionID, ClientID: "client-a", ChannelID: channel, Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !tab.Durable {
		t.Fatalf("tab = %+v; want durable", tab)
	}
	return tab
}

func TestClosingTheLastDurableTabRetriesAWaitingHelperUpdate(t *testing.T) {
	old := &updateWaitingDurableProvider{listingDurableProvider: &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}, waiting: true}
	updated := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	resolver := &sequenceResolver{providers: []base.DurableProvider{old, updated}, called: make(chan struct{}, 4)}
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-update", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	first := openSSHDurableTab(t, manager, connected.ID, "remote-1")
	<-resolver.called
	second := openSSHDurableTab(t, manager, connected.ID, "remote-2")

	if err := manager.CloseTab(first.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-resolver.called:
		t.Fatal("the helper was resolved again while a durable terminal still ran")
	case <-time.After(50 * time.Millisecond):
	}

	if err := manager.CloseTab(second.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-resolver.called:
	case <-time.After(10 * time.Second):
		t.Fatal("closing the last durable terminal did not retry the waiting helper update")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		openSSHDurableTab(t, manager, connected.ID, "remote-next")
		if updated.createCalls > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new durable terminals did not move to the updated helper")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := resolver.callCount(); got != 2 {
		t.Fatalf("resolver calls = %d, want 2", got)
	}
}

func TestClosingTheLastDurableTabLeavesACurrentHelperAlone(t *testing.T) {
	current := &updateWaitingDurableProvider{listingDurableProvider: &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}}
	resolver := &sequenceResolver{providers: []base.DurableProvider{current}, called: make(chan struct{}, 4)}
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), DurableResolver: resolver})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), Asset{ID: "ssh-current", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openSSHDurableTab(t, manager, connected.ID, "remote-1")
	<-resolver.called
	if err := manager.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-resolver.called:
		t.Fatal("a helper with no waiting update was resolved again")
	case <-time.After(50 * time.Millisecond):
	}
}
