package session

import (
	"context"
	"sync"
	"testing"
	"time"
)

// durableControlLog keeps every terminal control event a manager emits.
type durableControlLog struct {
	mu     sync.Mutex
	events []ControlEvent
}

func (r *durableControlLog) EmitSessionEvent(_ context.Context, event Event) error {
	if event.Topic != TopicTerminalControl {
		return nil
	}
	if control, ok := event.Payload.(ControlEvent); ok {
		r.mu.Lock()
		r.events = append(r.events, control)
		r.mu.Unlock()
	}
	return nil
}

func (r *durableControlLog) reset() {
	r.mu.Lock()
	r.events = nil
	r.mu.Unlock()
}

func (r *durableControlLog) has(tabID string, match func(ControlEvent) bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, event := range r.events {
		if event.TabID == tabID && match(event) {
			return true
		}
	}
	return false
}

func newRecoverIdentityManager(t *testing.T, provider *fakeDurableProvider, emitter Emitter) (*Manager, *fakeTerminalFactory) {
	t.Helper()
	terminals := newFakeTerminalFactory()
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: terminals,
		DurableResolver: &countingResolver{provider: provider},
		OpenAccess:      true,
		Emitter:         emitter,
	})
	t.Cleanup(func() { _ = manager.Close() })
	return manager, terminals
}

// startHelperRecord opens a durable terminal in one manager and closes that
// manager, leaving only the helper's record, as after an app restart.
func startHelperRecord(t *testing.T, provider *fakeDurableProvider, tabID string) {
	t.Helper()
	manager, _ := newRecoverIdentityManager(t, provider, nil)
	connected, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.OpenTab(context.Background(), OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "a-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func (p *fakeDurableProvider) setHelperObservation(id, cwd, windowTitle string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records[id].cwd = cwd
	p.records[id].windowTitle = windowTitle
}

func (p *fakeDurableProvider) lastAttachment(id string) *fakeDurableAttachment {
	p.mu.Lock()
	defer p.mu.Unlock()
	attachments := p.records[id].attachments
	return attachments[len(attachments)-1]
}

func waitForTabState(t *testing.T, tab *Tab, check func(TabInfo) bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !check(tab.Info()) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: tab info = %+v", what, tab.Info())
		}
		time.Sleep(time.Millisecond)
	}
}

// A recovered terminal starts in the directory and with the window title the
// helper last saw, although the replayed tail reports neither.
func TestRecoverRemoteDurableSeedsHelperCwdAndWindowTitle(t *testing.T) {
	provider := newFakeDurableProvider()
	const tabID = "recover-identity"
	startHelperRecord(t, provider, tabID)
	provider.setHelperObservation(tabID, "/srv/app", "vim notes.txt")

	recorder := &durableControlLog{}
	manager, terminals := newRecoverIdentityManager(t, provider, recorder)
	recoveredSession, err := manager.Connect(context.Background(), Asset{ID: "asset-recovered", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.RecoverRemoteDurable(context.Background(), tabID, OpenTabOptions{
		SessionID: recoveredSession.ID, ClientID: "client-b", ChannelID: "b-1", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Cwd != "/srv/app" {
		t.Fatalf("recovered cwd = %q, want the helper's /srv/app", info.Cwd)
	}
	if info.Title != "vim notes.txt" || info.TitleCustom {
		t.Fatalf("recovered title = %q custom=%v, want the helper's window title, not custom", info.Title, info.TitleCustom)
	}
	if !recorder.has(tabID, func(event ControlEvent) bool { return event.Cwd == "/srv/app" }) {
		t.Fatal("no control event carried the recovered cwd")
	}

	tab, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	attachment := provider.lastAttachment(tabID)
	// Output without a title keeps the seeded one; a later directory report
	// marks the moment that output was fed.
	if err := attachment.emit([]byte("plain output\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := attachment.emit([]byte("\x1b]7;file://host/srv/app/docs\x07")); err != nil {
		t.Fatal(err)
	}
	waitForTabState(t, tab, func(got TabInfo) bool { return got.Cwd == "/srv/app/docs" }, "directory report")
	if got := tab.Info(); got.Title != "vim notes.txt" {
		t.Fatalf("after title-less output: title = %q, want the seeded window title", got.Title)
	}
	// The program's next title and directory replace the seeded ones.
	terminals.terminal(tabID).setTitle("htop")
	if err := attachment.emit([]byte("\x1b]7;file://host/var/log\x07")); err != nil {
		t.Fatal(err)
	}
	waitForTabState(t, tab, func(got TabInfo) bool { return got.Title == "htop" && got.Cwd == "/var/log" }, "program reports")
	if tab.Info().TitleCustom {
		t.Fatal("a program title must not become a custom title")
	}
}

// A custom title still wins over the helper's window title.
func TestRecoverRemoteDurableCustomTitleWinsOverWindowTitle(t *testing.T) {
	provider := newFakeDurableProvider()
	const tabID = "recover-identity-custom"
	startHelperRecord(t, provider, tabID)
	provider.setHelperObservation(tabID, "", "vim notes.txt")
	provider.mu.Lock()
	provider.records[tabID].title = "deploy"
	provider.mu.Unlock()

	manager, _ := newRecoverIdentityManager(t, provider, nil)
	recoveredSession, err := manager.Connect(context.Background(), Asset{ID: "asset-recovered", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.RecoverRemoteDurable(context.Background(), tabID, OpenTabOptions{
		SessionID: recoveredSession.ID, ClientID: "client-b", ChannelID: "b-1", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "deploy" || !info.TitleCustom {
		t.Fatalf("recovered title = %q custom=%v, want the custom deploy", info.Title, info.TitleCustom)
	}
	if info.Cwd != "" {
		t.Fatalf("recovered cwd = %q, want unknown when the helper saw none", info.Cwd)
	}
}

// A terminal this backend still holds keeps its directory across a
// background and takeover: the attach reports it again.
func TestBackgroundTakeoverReportsKnownCwd(t *testing.T) {
	provider := newFakeDurableProvider()
	recorder := &durableControlLog{}
	manager, _, _, connected := newDurableTestManagerWithEmitter(t, provider, recorder)
	ctx := context.Background()
	const tabID = "takeover-cwd"
	if _, err := manager.OpenTab(ctx, OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "a-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(tabID)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.attachment(tabID, 0).emit([]byte("\x1b]7;file://host/home/u/src\x07$ ")); err != nil {
		t.Fatal(err)
	}
	waitForTabState(t, tab, func(got TabInfo) bool { return got.Cwd == "/home/u/src" }, "initial cwd")
	if err := manager.BackgroundTab(ctx, tabID, "client-a"); err != nil {
		t.Fatal(err)
	}
	recorder.reset()
	info, err := manager.AttachTab(ctx, tabID, AttachOptions{ChannelID: "a-2", ClientID: "client-a", ReplayBytes: -1})
	if err != nil {
		t.Fatal(err)
	}
	if info.Cwd != "/home/u/src" {
		t.Fatalf("takeover cwd = %q", info.Cwd)
	}
	if !recorder.has(tabID, func(event ControlEvent) bool { return event.Cwd == "/home/u/src" }) {
		t.Fatal("the takeover's control event did not carry the cwd")
	}
}
