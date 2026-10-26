package session

import (
	"context"
	"testing"
	"time"
)

func waitForControlCustom(t *testing.T, events chan ControlEvent, title string, custom bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Title == title && event.TitleCustom == custom {
				return
			}
		case <-deadline:
			t.Fatalf("no control event carried title %q custom=%v", title, custom)
		}
	}
}

// Clients must tell a custom title from the OSC program title the backend
// tracks otherwise, so tab info and every control event carry TitleCustom; a
// blank rename clears the custom title and the OSC title resumes.
func TestTitleCustomFlagAndBlankRenameClears(t *testing.T) {
	emitter, events := controlCollector()
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	manager := NewManager(Config{Connector: connector, Terminals: terminals, Emitter: emitter})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "asset-ssh", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	channel := connector.transport(0).channel(0)
	terminals.terminal(tab.ID).setTitle("bash")
	if err := channel.emit([]byte("x")); err != nil {
		t.Fatal(err)
	}
	waitForControlCustom(t, events, "bash", false)
	if info := tab.Info(); info.Title != "bash" || info.TitleCustom {
		t.Fatalf("OSC title info = %+v, want a non-custom program title", info)
	}

	renamed, err := manager.RenameTab(context.Background(), tab.ID, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != "deploy" || !renamed.TitleCustom {
		t.Fatalf("rename info = %+v, want a custom title", renamed)
	}
	waitForControlCustom(t, events, "deploy", true)

	terminals.terminal(tab.ID).setTitle("vim")
	if err := channel.emit([]byte("y")); err != nil {
		t.Fatal(err)
	}
	waitForTabFeed(t, tab, func(info TabInfo) bool { return info.Title == "deploy" && info.TitleCustom })

	cleared, err := manager.RenameTab(context.Background(), tab.ID, "  ")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Title != "vim" || cleared.TitleCustom {
		t.Fatalf("cleared info = %+v, want the current OSC title", cleared)
	}
	waitForControlCustom(t, events, "vim", false)

	terminals.terminal(tab.ID).setTitle("top")
	if err := channel.emit([]byte("z")); err != nil {
		t.Fatal(err)
	}
	waitForControlCustom(t, events, "top", false)
}

// waitForTabFeed lets a just-emitted chunk pass through the feed before the
// condition is checked; the custom title must still hold afterwards.
func waitForTabFeed(t *testing.T, tab *Tab, ok func(TabInfo) bool) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	if info := tab.Info(); !ok(info) {
		t.Fatalf("tab info = %+v", info)
	}
}

// Clearing a durable tab's custom title also drops the persisted title, so a
// restart does not resurrect it.
func TestDurableBlankRenameDropsPersistedTitle(t *testing.T) {
	provider := newFakeDurableProvider()
	manager, _, _, connected := newDurableTestManager(t, provider)
	info := openDurableTestTab(t, manager, connected, "clear-tab", "a-1", false)
	if _, err := manager.RenameTab(context.Background(), info.ID, "构建机"); err != nil {
		t.Fatal(err)
	}
	cleared, err := manager.RenameTab(context.Background(), info.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.TitleCustom {
		t.Fatalf("cleared info = %+v", cleared)
	}
	if got := provider.DurableTabTitleContext(context.Background(), info.ID); got != "" {
		t.Fatalf("persisted title after clear = %q", got)
	}
}

// A canonical (helper-owned) durable title clears through the helper and the
// OSC title resumes.
func TestDurableCanonicalBlankRenameResumesOSCTitle(t *testing.T) {
	provider := &unlatchMetadataProvider{fakeDurableProvider: newFakeDurableProvider(), incarnation: "inc-1"}
	manager, _, terminals, connected := newDurableTestManager(t, provider)
	ctx := context.Background()
	tabID := "durable-clear"
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "clear-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	renamed, err := manager.RenameTabAt(ctx, tabID, "deploy", 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if !renamed.TitleCustom || renamed.TitleChangedAt != 1_000 {
		t.Fatalf("rename info = %+v", renamed)
	}
	tab, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := manager.RenameTab(ctx, tabID, "")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.TitleCustom {
		t.Fatalf("cleared info = %+v", cleared)
	}
	provider.mu.Lock()
	canonical := provider.title
	provider.mu.Unlock()
	if canonical != "" {
		t.Fatalf("canonical title after clear = %q", canonical)
	}
	terminals.terminal(tabID).setTitle("osc-title")
	if err := provider.attachment(tabID, 0).emit([]byte("x")); err != nil {
		t.Fatal(err)
	}
	waitForTabTitle(t, tab, "osc-title")
}

// The client's decision time travels with every title decision so clients can
// order renames and clears across devices; a rename without one uses now.
func TestRenameTabCarriesDecisionTime(t *testing.T) {
	emitter, events := controlCollector()
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	manager := NewManager(Config{Connector: connector, Terminals: terminals, Emitter: emitter})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(context.Background(), Asset{ID: "asset-ssh", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	renamed, err := manager.RenameTabAt(context.Background(), tab.ID, "deploy", 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.TitleChangedAt != 1_000 {
		t.Fatalf("rename info = %+v", renamed)
	}
	cleared, err := manager.RenameTabAt(context.Background(), tab.ID, "", 2_000)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.TitleChangedAt != 2_000 || cleared.TitleCustom {
		t.Fatalf("clear info = %+v", cleared)
	}
	sawClear := false
	for _, event := range drainControl(events) {
		if !event.TitleCustom && event.TitleChangedAt == 2_000 {
			sawClear = true
		}
	}
	if !sawClear {
		t.Fatal("no control event carried the clear decision time")
	}
	before := time.Now().UnixMilli()
	now, err := manager.RenameTab(context.Background(), tab.ID, "ops")
	if err != nil {
		t.Fatal(err)
	}
	if now.TitleChangedAt < before {
		t.Fatalf("default decision time = %d, want >= %d", now.TitleChangedAt, before)
	}
}
