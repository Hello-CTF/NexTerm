package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

func feedWatchReconnectTab(t *testing.T, manager *Manager, tabID string, data []byte) {
	t.Helper()
	tab, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	tab.mu.Lock()
	generation := tab.generation
	tab.mu.Unlock()
	if err := manager.feed(context.Background(), tab, generation, data); err != nil {
		t.Fatal(err)
	}
}

// The in-place reconnect keeps the watch coordinate identity: the runtime
// episode advances for roster purposes, but the tab keeps its terminal and
// its ring, so absolute positions continue and a bound source never sees a
// replacement. Output produced after the reconnect is consumed at continuous
// positions.
func TestWatchSourceSurvivesInPlaceReconnect(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector, ReconnectMax: 10, ReconnectBackoff: []time.Duration{0}, OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.OpenTab(ctx, OpenTabOptions{SessionID: connected.ID, ClientID: "client-a", ChannelID: "ch-1", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	before, ok := manager.WatchStreamState(ctx, tab.ID)
	if !ok || !before.EpisodeOK || before.WatchIdentity() == "" {
		t.Fatalf("pre-reconnect watch state = %+v, %v", before, ok)
	}
	source, err := manager.OpenWatchSource(ctx, tab.ID, before.WatchIdentity(), WatchSourceOptions{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	feedWatchReconnectTab(t, manager, tab.ID, []byte("pre-"))
	chunk, next, err := source.Chunk(ctx, 0)
	if err != nil || string(chunk) != "pre-" || next != 4 {
		t.Fatalf("pre-reconnect Chunk = %q, %d, %v", chunk, next, err)
	}

	if err := manager.Reconnect(ctx, connected.ID); err != nil {
		t.Fatal(err)
	}
	after, ok := manager.WatchStreamState(ctx, tab.ID)
	if !ok {
		t.Fatal("post-reconnect watch state failed")
	}
	if after.Episode == before.Episode {
		t.Fatal("the roster runtime episode must advance on an in-place reconnect")
	}
	if after.WatchIdentity() != before.WatchIdentity() {
		t.Fatalf("watch identity moved %q -> %q on a ring-continuous reconnect", before.WatchIdentity(), after.WatchIdentity())
	}

	// The bound source keeps consuming the same ring at continuous positions:
	// no replacement error, no gap, no coordinate reset — the reconnect banner
	// itself lands in the same ring between the pre- and post-reconnect bytes.
	feedWatchReconnectTab(t, manager, tab.ID, []byte("post"))
	chunk, next, err = source.Chunk(ctx, 4)
	banner := string(reconnectBanner) + "post"
	if err != nil || string(chunk) != banner || next != 4+int64(len(banner)) {
		t.Fatalf("post-reconnect Chunk = %q, %d, %v; want the same ring continuing with %q", chunk, next, err, banner)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

// The complementary identity boundary: closing and recreating the tab mints
// a new stream epoch, so the watch identity advances and the old source is
// told the stream was replaced instead of resuming against reset coordinates.
func TestWatchIdentityAdvancesWhenTheRingIsRecreated(t *testing.T) {
	manager := newWatchTestManager(t)
	tab := openWatchTestTab(t, manager)
	ctx := context.Background()
	before, ok := manager.WatchStreamState(ctx, tab.ID)
	if !ok {
		t.Fatal("watch state failed")
	}
	source, err := manager.OpenWatchSource(ctx, tab.ID, before.WatchIdentity(), WatchSourceOptions{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	tabObject, err := manager.Tab(tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	tabObject.mu.Lock()
	tabObject.streamEpoch = "epoch-recreated"
	tabObject.mu.Unlock()
	after, ok := manager.WatchStreamState(ctx, tab.ID)
	if !ok {
		t.Fatal("watch state after recreation failed")
	}
	if after.WatchIdentity() == before.WatchIdentity() {
		t.Fatal("a recreated ring must advance the watch identity")
	}
	var replaced *WatchEpisodeReplacedError
	if _, _, err := source.Chunk(ctx, 0); !errors.As(err, &replaced) {
		t.Fatalf("Chunk after ring recreation = %v; want WatchEpisodeReplacedError", err)
	}
}
