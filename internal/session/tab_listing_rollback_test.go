package session

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/Hello-CTF/NexTerm/internal/ids"
)

// TestRolledBackAttachRecordsADetachThatHappenedMeanwhile: A is the only
// subscriber, B registers and blocks on its replay, A leaves (B still counts,
// so no detach time is recorded), then B's send fails and the rollback leaves
// no subscriber. The tab went to the background when A left, not before.
func TestRolledBackAttachRecordsADetachThatHappenedMeanwhile(t *testing.T) {
	provider := newFakeDurableProvider()
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	var manager *Manager
	// One queued frame per channel: B's anchor fits, its replay then waits for a reader that never comes.
	bus := hub.New(hub.Options{
		QueueFrames: 1,
		OnChannelClose: func(id string, incarnation uint64, closeChannel func()) {
			manager.onChannelClose(id, incarnation, closeChannel)
		},
		OnBackpressure: func(id string, queued int) { manager.onBackpressure(id, queued) },
		OnDrain:        func(id string, queued int) { manager.onDrain(id, queued) },
	})
	manager = NewManager(Config{Connector: connector, Terminals: terminals, Durable: provider, Hub: bus})
	t.Cleanup(func() {
		_ = manager.Close()
		_ = bus.Close()
	})
	connected, err := manager.Connect(context.Background(), Asset{ID: ids.New(), Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	bindTestReceiver(t, manager, "a-1")
	info := openDurableTestTab(t, manager, connected, "rollback-tab", "a-1", false)
	if err := provider.attachment(info.ID, 0).emit([]byte("boot")); err != nil {
		t.Fatal(err)
	}
	terminal := terminals.terminal(info.ID)
	waitFor(t, func() bool { return bytes.Equal(terminal.bytes(), []byte("boot")) })

	attachCtx, cancelAttach := context.WithCancel(context.Background())
	defer cancelAttach()
	attachErr := make(chan error, 1)
	go func() {
		_, err := manager.AttachTab(attachCtx, info.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1", ReplayBytes: -1})
		attachErr <- err
	}()
	// B is registered once the tab lists two subscribers; its replay is then blocked on the full queue.
	waitFor(t, func() bool { return listedTab(t, manager, info.ID).Subscribers == 2 })
	if err := manager.DetachChannel("a-1"); err != nil {
		t.Fatal(err)
	}
	if listed := listedTab(t, manager, info.ID); listed.Subscribers != 1 || listed.DetachedAt != 0 {
		t.Fatalf("with B registered the tab lists %d subscribers, detached at %d", listed.Subscribers, listed.DetachedAt)
	}
	before := time.Now().UnixMilli()
	cancelAttach()
	select {
	case err := <-attachErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked attach ended with %v, want cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the blocked attach did not end after cancellation")
	}
	listed := listedTab(t, manager, info.ID)
	if listed.Subscribers != 0 || listed.DetachedAt < before {
		t.Fatalf("after the rollback the tab lists %d subscribers, detached at %d (want >= %d)", listed.Subscribers, listed.DetachedAt, before)
	}
}

// TestRolledBackAttachKeepsTheEarlierDetachTime: B was the only subscriber
// ever registered since the last detach, so the rollback restores nothing new.
func TestRolledBackAttachKeepsTheEarlierDetachTime(t *testing.T) {
	provider := newFakeDurableProvider()
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	var manager *Manager
	bus := hub.New(hub.Options{
		QueueFrames: 1,
		OnChannelClose: func(id string, incarnation uint64, closeChannel func()) {
			manager.onChannelClose(id, incarnation, closeChannel)
		},
		OnBackpressure: func(id string, queued int) { manager.onBackpressure(id, queued) },
		OnDrain:        func(id string, queued int) { manager.onDrain(id, queued) },
	})
	manager = NewManager(Config{Connector: connector, Terminals: terminals, Durable: provider, Hub: bus})
	t.Cleanup(func() {
		_ = manager.Close()
		_ = bus.Close()
	})
	connected, err := manager.Connect(context.Background(), Asset{ID: ids.New(), Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	bindTestReceiver(t, manager, "a-1")
	info := openDurableTestTab(t, manager, connected, "rollback-keep", "a-1", false)
	if err := provider.attachment(info.ID, 0).emit([]byte("boot")); err != nil {
		t.Fatal(err)
	}
	terminal := terminals.terminal(info.ID)
	waitFor(t, func() bool { return bytes.Equal(terminal.bytes(), []byte("boot")) })
	if err := manager.DetachChannel("a-1"); err != nil {
		t.Fatal(err)
	}
	detachedAt := listedTab(t, manager, info.ID).DetachedAt
	if detachedAt == 0 {
		t.Fatal("no detach time after the only subscriber left")
	}

	attachCtx, cancelAttach := context.WithCancel(context.Background())
	defer cancelAttach()
	attachErr := make(chan error, 1)
	go func() {
		_, err := manager.AttachTab(attachCtx, info.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1", ReplayBytes: -1})
		attachErr <- err
	}()
	waitFor(t, func() bool { return listedTab(t, manager, info.ID).Subscribers == 1 })
	cancelAttach()
	select {
	case <-attachErr:
	case <-time.After(5 * time.Second):
		t.Fatal("the blocked attach did not end after cancellation")
	}
	if listed := listedTab(t, manager, info.ID); listed.Subscribers != 0 || listed.DetachedAt != detachedAt {
		t.Fatalf("after the rollback the tab lists %d subscribers, detached at %d, want the earlier %d", listed.Subscribers, listed.DetachedAt, detachedAt)
	}
}
