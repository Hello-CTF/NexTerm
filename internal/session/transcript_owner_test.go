package session

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// ListDurableDescriptors reports each live record with the incarnation a
// helper listing carries, so recovery can use checked attach.
func (p *fakeDurableProvider) ListDurableDescriptors(context.Context) ([]base.DurableDescriptor, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	descriptors := make([]base.DurableDescriptor, 0, len(p.records))
	for id, record := range p.records {
		if record.killed == 0 {
			descriptors = append(descriptors, base.DurableDescriptor{
				ID: id, Incarnation: fakeIncarnation(record.identity), Title: record.title, MetadataRevision: record.revision,
			})
		}
	}
	return descriptors, nil
}

// An open deployment recovers a durable tab with no owner and keeps its
// metadata, such as the title, across managers.
func TestEmptyOwnerDurableRecoveryKeepsTitle(t *testing.T) {
	provider := newFakeDurableProvider()
	newManager := func() *Manager {
		manager := NewManager(Config{
			Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
			DurableResolver: &countingResolver{provider: provider},
			OpenAccess:      true,
		})
		t.Cleanup(func() { _ = manager.Close() })
		return manager
	}
	manager := newManager()
	connected, err := manager.Connect(context.Background(), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	const tabID = "context-open-durable"
	if _, err := manager.OpenTab(context.Background(), OpenTabOptions{
		TabID: tabID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "a-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RenameTab(context.Background(), tabID, "open-title"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	recoveredManager := newManager()
	recoveredSession, err := recoveredManager.Connect(context.Background(), Asset{ID: "asset-recovered", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := recoveredManager.RecoverRemoteDurable(context.Background(), tabID, OpenTabOptions{
		SessionID: recoveredSession.ID, ClientID: "client-b", ChannelID: "b-1", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Title != "open-title" {
		t.Fatalf("empty-owner recovered title = %q, want open-title", recovered.Title)
	}
	if err := recoveredManager.CloseTab(tabID); err != nil {
		t.Fatal(err)
	}
}

func TestReconnectSecondPeriodCarriesOwner(t *testing.T) {
	provider := newFakeDurableProvider()
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		DurableResolver:  &countingResolver{provider: provider},
		ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(requestTestUser("owner-a"), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	const tabID = "reconnect-owner-tab"
	if _, err := manager.OpenTab(requestTestUser("owner-a"), OpenTabOptions{
		TabID: tabID, SessionID: session.ID, ClientID: "client-a", ChannelID: "a-1", Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh"}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := manager.StartReconnect(session.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		session.mu.Lock()
		reconnecting := session.reconnecting
		session.mu.Unlock()
		if !reconnecting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reconnect did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	session.mu.Lock()
	owner := userIDFromContext(session.ctx)
	session.mu.Unlock()
	if owner != "owner-a" {
		t.Fatalf("second period ctx owner = %q, want owner-a", owner)
	}
}

var _ base.DurableProvider = (*fakeDurableProvider)(nil)

// Every sink call carries the trusted session owner, whatever path makes it.
func TestTranscriptSinkCallsCarryTrustedSessionOwner(t *testing.T) {
	connector := newFakeConnector()
	sink := &transcriptRecorder{}
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(),
		Transcripts: sink, ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	ownerA := requestTestUser("owner-a")
	connected, err := manager.Connect(ownerA, Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ownerA, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "channel-a", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := manager.Tab(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	channel := connector.transport(0).channel(0)
	if err := channel.emit([]byte("owner-output\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.of(recOutput, "")) > 0 })
	if err := manager.Resize(ownerA, info.ID, "client-a", 120, 40); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.of(recResize, "")) > 0 })
	if err := manager.DetachChannel("channel-a"); err != nil {
		t.Fatal(err)
	}
	emitShown(t, tab, channel, "unwatched\r\n")
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "channel-b"}); err != nil {
		t.Fatal(err)
	}

	if err := manager.Disconnect(connected.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.of(recEnded, "")) == 1 })
	if err := manager.Reconnect(ownerA, connected.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.of(recStarted, "")) == 2 })
	if err := manager.CloseTab(info.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.of(recEnded, "")) == 2 })

	for _, kind := range []string{recStarted, recScreen, recOutput, recResize, recEnded} {
		sink.assertEveryCallCarries(t, kind, "owner-a")
	}
}

// The trusted owner comes from the session record, not the caller's context:
// an open deployment admits identity-less callers, and their transcript calls
// must still write under the session owner's identity. Ownerless sessions on
// open deployments keep the legacy ownerless behavior.
func TestTranscriptSinkOwnerComesFromSessionNotCaller(t *testing.T) {
	connector := newFakeConnector()
	sink := &transcriptRecorder{}
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(),
		Transcripts: sink, OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(requestTestUser("owner-a"), Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.OpenTab(context.Background(), OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "channel-a", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatal(err)
	}
	if err := connector.transport(0).channel(0).emit([]byte("open-output\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(sink.of(recOutput, "")) > 0 })
	for _, kind := range []string{recStarted, recOutput} {
		sink.assertEveryCallCarries(t, kind, "owner-a")
	}

	open, err := manager.Connect(context.Background(), Asset{ID: "asset-open", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	openTab := openTestTab(t, manager, open, "client-b", "channel-b")
	started := sink.of(recStarted, openTab.ID)
	if len(started) != 1 || started[0].owner != "" {
		t.Fatalf("ownerless open-deployment session recording start = %+v, want one legacy ownerless start", started)
	}
}

func TestReconnectOnceSecondPeriodCarriesOwner(t *testing.T) {
	connector := newFakeConnector()
	sink := &transcriptRecorder{}
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(),
		Transcripts: sink, ReconnectBackoff: []time.Duration{0},
	})
	t.Cleanup(func() { _ = manager.Close() })
	ownerA := requestTestUser("owner-a")
	connected, err := manager.Connect(ownerA, Asset{ID: "asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.OpenTab(ownerA, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "channel-a", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatal(err)
	}

	if err := manager.ReconnectOnceFor(ownerA, connected.ID, Asset{ID: "asset", Kind: KindSSH}); err != nil {
		t.Fatal(err)
	}
	connected.mu.Lock()
	owner := userIDFromContext(connected.ctx)
	connected.mu.Unlock()
	if owner != "owner-a" {
		t.Fatalf("reconnect-once second period ctx owner = %q, want owner-a", owner)
	}
	waitFor(t, func() bool { return len(sink.of(recStarted, "")) == 2 && len(sink.of(recEnded, "")) == 1 })
	for _, kind := range []string{recStarted, recEnded} {
		sink.assertEveryCallCarries(t, kind, "owner-a")
	}
}
