package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReconnectReattachesSSHDurableTab(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	resolver := &countingResolver{provider: provider}
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector:        connector,
		Terminals:        newFakeTerminalFactory(),
		DurableResolver:  resolver,
		ReconnectBackoff: []time.Duration{10 * time.Millisecond},
	})
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-reconnect", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "rc-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}
	tabID := info.ID
	provider.mu.Lock()
	record := provider.records[tabID]
	oldAttachment := record.attachments[0]
	provider.mu.Unlock()
	if err := oldAttachment.emit([]byte("daemon-live-output")); err != nil {
		t.Fatal(err)
	}
	waitForTabMarker(t, manager, tabID, "daemon-live-output")
	provider.mu.Lock()
	record.output = []byte("daemon-live-output daemon-replay-marker")
	provider.mu.Unlock()

	if err := manager.Reconnect(ctx, connected.ID); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	attachCalls := provider.attachCalls
	provider.mu.Unlock()
	if attachCalls != 1 {
		t.Fatalf("provider attach calls = %d; want 1 reattach on the new transport", attachCalls)
	}
	if resolver.callCount() != 2 {
		t.Fatalf("resolver calls = %d; want 2 (re-resolved for the new transport)", resolver.callCount())
	}
	if count := connector.transport(1).channelCount(); count != 0 {
		t.Fatalf("reconnect opened %d direct PTY channels on the new transport; want 0", count)
	}
	if oldAttachment.closeCount.Load() == 0 {
		t.Fatal("old durable attachment was not closed")
	}
	tab, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	if got := tab.Info(); !got.Durable || got.Exited {
		t.Fatalf("tab after reconnect = %+v; want durable and live", got)
	}
	waitForTabMarker(t, manager, tabID, "daemon-replay-marker")
	raw, err := manager.RawDump(tabID, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(raw), "daemon-live-output"); count != 1 {
		t.Fatalf("consumed prefix replayed %d times; want 1: %q", count, raw)
	}
	if count := strings.Count(string(raw), "daemon-replay-marker"); count != 1 {
		t.Fatalf("replay marker appears %d times; want 1: %q", count, raw)
	}

	provider.mu.Lock()
	attachment := record.attachments[len(record.attachments)-1]
	provider.mu.Unlock()
	if attachment == oldAttachment {
		t.Fatal("tab kept the old attachment after reconnect")
	}
	if err := manager.Write(ctx, tabID, "client-a", []byte("input-after-reconnect\n")); err != nil {
		t.Fatal(err)
	}
	if written := string(attachment.written()); !strings.Contains(written, "input-after-reconnect") {
		t.Fatalf("new attachment received input %q", written)
	}
	if err := manager.Resize(ctx, tabID, "client-a", 100, 30); err != nil {
		t.Fatal(err)
	}
	if err := waitForChannelResize(t, attachment.fakeChannel, 100, 30); err != nil {
		t.Fatal(err)
	}
	if err := manager.CloseTab(tabID); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	killed := record.killed
	provider.mu.Unlock()
	if killed != 1 {
		t.Fatalf("daemon session killed = %d times; want 1", killed)
	}
}

func TestReconnectMarksMissingSSHDurableTabExited(t *testing.T) {
	provider := &listingDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	resolver := &countingResolver{provider: provider}
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector:        connector,
		Terminals:        newFakeTerminalFactory(),
		DurableResolver:  resolver,
		ReconnectBackoff: []time.Duration{10 * time.Millisecond},
	})
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-gone", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "gone-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.records[info.ID].killed = 1
	provider.mu.Unlock()

	if err := manager.Reconnect(ctx, connected.ID); err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := tab.Info(); !got.Exited || got.Durable {
		t.Fatalf("tab after reconnect = %+v; want exited and no longer durable", got)
	}
}

func waitForTabMarker(t *testing.T, manager *Manager, tabID, marker string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		raw, err := manager.RawDump(tabID, 1<<20)
		if err != nil {
			t.Fatalf("RawDump: %v", err)
		}
		if strings.Contains(string(raw), marker) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in tab output: %q", marker, raw)
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-manager.ctx.Done():
			t.Fatal("manager closed while waiting for tab output")
		}
	}
}

func waitForChannelResize(t *testing.T, channel *fakeChannel, cols, rows uint32) error {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		channel.mu.Lock()
		matches := channel.resizes > 0 && channel.cols == cols && channel.rows == rows
		channel.mu.Unlock()
		if matches {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for the replacement channel resize")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
