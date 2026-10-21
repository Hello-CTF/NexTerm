package session

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func openRosterTestManager(t *testing.T, provider base.DurableProvider, userID string) (*Manager, *Session) {
	t.Helper()
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Durable: provider})
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Connect(requestTestUser(userID), Asset{ID: ids.New(), Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	return manager, session
}

func openRosterDurableTab(t *testing.T, manager *Manager, session *Session, userID, tabID, channel string, recover bool) TabInfo {
	t.Helper()
	info, err := manager.OpenTab(requestTestUser(userID), OpenTabOptions{
		TabID: tabID, SessionID: session.ID, ClientID: "client-a", ChannelID: channel, Cols: 80, Rows: 24,
		Durable: &DurableTabOptions{Command: []string{"/bin/sh", "-c", "exit 99"}, Recover: recover},
	})
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// rosterIdentityDurableProvider is a fake durable provider whose attachments
// carry a supervisor identity: a create after a kill mints a fresh daemon
// incarnation while an attach keeps the existing one — the real daemon
// semantics the roster runtime episode evidence relies on.
type rosterIdentityDurableProvider struct {
	*fakeDurableProvider
}

func (p *rosterIdentityDurableProvider) Create(ctx context.Context, options base.DurableCreateOptions) (base.DurableAttachment, error) {
	attachment, err := p.fakeDurableProvider.Create(ctx, options)
	if err != nil {
		return nil, err
	}
	return &rosterIdentityAttachment{fakeDurableAttachment: attachment.(*fakeDurableAttachment)}, nil
}

func (p *rosterIdentityDurableProvider) Attach(ctx context.Context, id string) (base.DurableAttachment, error) {
	attachment, err := p.fakeDurableProvider.Attach(ctx, id)
	if err != nil {
		return nil, err
	}
	return &rosterIdentityAttachment{fakeDurableAttachment: attachment.(*fakeDurableAttachment)}, nil
}

// AttachWithIdentity mirrors the daemon contract: only the surviving daemon's
// own incarnation may reattach, so a reconnect after daemon replacement fails
// instead of silently aliasing the new runtime.
func (p *rosterIdentityDurableProvider) AttachWithIdentity(ctx context.Context, id string, identity base.DurableIdentity) (base.DurableAttachment, error) {
	p.mu.Lock()
	record := p.records[id]
	want := ""
	if record != nil {
		want = fmt.Sprintf("daemon-%d", record.identity)
	}
	p.mu.Unlock()
	if record == nil || identity.Incarnation != want {
		return nil, durable.ErrNotFound
	}
	return p.Attach(ctx, id)
}

func (p *rosterIdentityDurableProvider) AttachFromWithIdentity(ctx context.Context, id string, identity base.DurableIdentity, _ uint64) (base.DurableAttachment, error) {
	return p.AttachWithIdentity(ctx, id, identity)
}

type rosterIdentityAttachment struct {
	*fakeDurableAttachment
}

func (a *rosterIdentityAttachment) Identity() supervisor.Identity {
	return supervisor.Identity{CreatedAt: time.Unix(1, 0), Incarnation: fmt.Sprintf("daemon-%d", a.identity)}
}

func TestRosterRuntimeIncarnationLocalEpisode(t *testing.T) {
	manager, connected := openRosterTestManager(t, nil, "user-a")
	alice := requestTestUser("user-a")
	info, err := manager.OpenTab(alice, OpenTabOptions{
		TabID: "roster-local", SessionID: connected.ID, ClientID: "client-a", ChannelID: "roster-1", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}

	episode, ok := manager.RosterRuntimeIncarnation(alice, info.ID)
	if !ok || episode == "" {
		t.Fatalf("local tab episode = %q, %v; want a minted episode", episode, ok)
	}
	if again, ok := manager.RosterRuntimeIncarnation(alice, info.ID); !ok || again != episode {
		t.Fatalf("second lookup = %q, %v; want %q, true", again, ok, episode)
	}

	other, err := manager.OpenTab(alice, OpenTabOptions{
		TabID: "roster-local-2", SessionID: connected.ID, ClientID: "client-a", ChannelID: "roster-2", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if otherEpisode, ok := manager.RosterRuntimeIncarnation(alice, other.ID); !ok || otherEpisode == episode {
		t.Fatalf("second tab episode = %q, %v; want a distinct episode", otherEpisode, ok)
	}

	if err := manager.CloseTab(info.ID); err != nil {
		t.Fatal(err)
	}
	recreated, err := manager.OpenTab(alice, OpenTabOptions{
		TabID: info.ID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "roster-3", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	fresh, ok := manager.RosterRuntimeIncarnation(alice, recreated.ID)
	if !ok || fresh == "" {
		t.Fatalf("recreated tab episode = %q, %v; want a fresh episode", fresh, ok)
	}
	if fresh == episode {
		t.Fatal("recreated tab reused the removed tab's runtime episode")
	}
}

func TestRosterRuntimeIncarnationDurableEpisode(t *testing.T) {
	provider := &rosterIdentityDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	manager, connected := openRosterTestManager(t, provider, "user-a")
	alice := requestTestUser("user-a")
	info := openRosterDurableTab(t, manager, connected, "user-a", "roster-durable", "d-1", false)

	episode, ok := manager.RosterRuntimeIncarnation(alice, info.ID)
	if !ok || episode != "daemon-1" {
		t.Fatalf("durable tab episode = %q, %v; want the daemon incarnation %q", episode, ok, "daemon-1")
	}

	// The daemon terminal outlives the manager: a restart plus recover must
	// report the same runtime episode, not a fresh one.
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, reconnected := openRosterTestManager(t, provider, "user-a")
	recovered := openRosterDurableTab(t, restarted, reconnected, "user-a", info.ID, "d-2", true)
	if afterRestart, ok := restarted.RosterRuntimeIncarnation(alice, recovered.ID); !ok || afterRestart != episode {
		t.Fatalf("episode after manager restart = %q, %v; want %q, true", afterRestart, ok, episode)
	}

	// An explicit close kills the daemon: recreating the tab attaches to a
	// new daemon process, which is a new runtime episode.
	if err := restarted.CloseTab(info.ID); err != nil {
		t.Fatal(err)
	}
	recreated := openRosterDurableTab(t, restarted, reconnected, "user-a", info.ID, "d-3", false)
	fresh, ok := restarted.RosterRuntimeIncarnation(alice, recreated.ID)
	if !ok || fresh == "" {
		t.Fatalf("recreated durable tab episode = %q, %v; want a fresh episode", fresh, ok)
	}
	if fresh == episode {
		t.Fatal("recreated durable tab reused the killed daemon's runtime episode")
	}
}

func TestRosterRuntimeIncarnationDurableWithoutIdentity(t *testing.T) {
	// The plain fake provider hands out attachments with no supervisor
	// identity: the manager must report the episode as unverifiable rather
	// than mint a stand-in for the daemon.
	manager, connected := openRosterTestManager(t, newFakeDurableProvider(), "user-a")
	info := openRosterDurableTab(t, manager, connected, "user-a", "roster-durable-anon", "d-1", false)
	if episode, ok := manager.RosterRuntimeIncarnation(requestTestUser("user-a"), info.ID); ok || episode != "" {
		t.Fatalf("identity-less durable tab episode = %q, %v; want \"\", false", episode, ok)
	}
}

func TestRosterRuntimeIncarnationOwnerScoped(t *testing.T) {
	manager, connected := openRosterTestManager(t, nil, "user-a")
	alice := requestTestUser("user-a")
	bob := requestTestUser("user-b")
	info, err := manager.OpenTab(alice, OpenTabOptions{
		TabID: "roster-owned", SessionID: connected.ID, ClientID: "client-a", ChannelID: "roster-1", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	if episode, ok := manager.RosterRuntimeIncarnation(alice, info.ID); !ok || episode == "" {
		t.Fatalf("owner lookup = %q, %v; want a minted episode", episode, ok)
	}
	if episode, ok := manager.RosterRuntimeIncarnation(bob, info.ID); ok || episode != "" {
		t.Fatalf("cross-owner lookup = %q, %v; want \"\", false", episode, ok)
	}
	if episode, ok := manager.RosterRuntimeIncarnation(alice, "roster-missing"); ok || episode != "" {
		t.Fatalf("missing tab lookup = %q, %v; want \"\", false", episode, ok)
	}
}

// The snapshot reads every roster-relevant field from one critical section:
// identity, episode, exited, connectivity and asset agree with each other and
// with the derived incarnation, and owner scoping still applies.
func TestRosterRuntimeSnapshotSameSource(t *testing.T) {
	manager, connected := openRosterTestManager(t, nil, "user-a")
	alice := requestTestUser("user-a")
	bob := requestTestUser("user-b")
	info, err := manager.OpenTab(alice, OpenTabOptions{
		TabID: "roster-snapshot", SessionID: connected.ID, ClientID: "client-a", ChannelID: "roster-1", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	episode, ok := manager.RosterRuntimeIncarnation(alice, info.ID)
	if !ok {
		t.Fatal("incarnation lookup failed")
	}

	snapshot, ok := manager.RosterRuntimeSnapshot(alice, info.ID)
	if !ok {
		t.Fatal("snapshot lookup failed")
	}
	if snapshot.TabID != info.ID || snapshot.SessionID != connected.ID {
		t.Fatalf("snapshot identity = %q/%q, want %q/%q", snapshot.TabID, snapshot.SessionID, info.ID, connected.ID)
	}
	if !snapshot.EpisodeOK || snapshot.Episode != episode {
		t.Fatalf("snapshot episode = %q, %v; want %q, true", snapshot.Episode, snapshot.EpisodeOK, episode)
	}
	if snapshot.Exited {
		t.Fatal("snapshot reported exited on a live tab")
	}
	if snapshot.Status != StatusConnected {
		t.Fatalf("snapshot status = %v, want %v", snapshot.Status, StatusConnected)
	}
	if snapshot.Asset.ID != connected.Asset().ID {
		t.Fatalf("snapshot asset = %q, want %q", snapshot.Asset.ID, connected.Asset().ID)
	}

	if _, ok := manager.RosterRuntimeSnapshot(bob, info.ID); ok {
		t.Fatal("cross-owner snapshot succeeded")
	}
	if _, ok := manager.RosterRuntimeSnapshot(alice, "roster-missing"); ok {
		t.Fatal("missing-tab snapshot succeeded")
	}
}

// A durable tab whose daemon supplied no incarnation keeps every other
// snapshot field but fails closed on the episode evidence; a daemon-backed
// tab reports the daemon incarnation.
func TestRosterRuntimeSnapshotDurableEpisodeEvidence(t *testing.T) {
	manager, connected := openRosterTestManager(t, newFakeDurableProvider(), "user-a")
	alice := requestTestUser("user-a")
	anon := openRosterDurableTab(t, manager, connected, "user-a", "roster-snap-anon", "d-1", false)
	snapshot, ok := manager.RosterRuntimeSnapshot(alice, anon.ID)
	if !ok {
		t.Fatal("snapshot lookup failed")
	}
	if snapshot.EpisodeOK || snapshot.Episode != "" {
		t.Fatalf("identity-less durable snapshot episode = %q, %v; want \"\", false", snapshot.Episode, snapshot.EpisodeOK)
	}
	if snapshot.TabID != anon.ID || snapshot.SessionID != connected.ID {
		t.Fatalf("identity-less durable snapshot identity = %q/%q", snapshot.TabID, snapshot.SessionID)
	}

	provider := &rosterIdentityDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	identified, identifiedSession := openRosterTestManager(t, provider, "user-a")
	named := openRosterDurableTab(t, identified, identifiedSession, "user-a", "roster-snap-named", "d-1", false)
	namedSnapshot, ok := manager.RosterRuntimeSnapshot(alice, named.ID)
	if ok {
		t.Fatal("snapshot crossed managers")
	}
	namedSnapshot, ok = identified.RosterRuntimeSnapshot(alice, named.ID)
	if !ok {
		t.Fatal("identified durable snapshot lookup failed")
	}
	if !namedSnapshot.EpisodeOK || namedSnapshot.Episode != "daemon-1" {
		t.Fatalf("identified durable snapshot episode = %q, %v; want daemon-1, true", namedSnapshot.Episode, namedSnapshot.EpisodeOK)
	}
}

// A committed reconnect replaces the tab's channel and generation: the
// non-durable runtime episode must advance with it, or a roster row bound
// before the reconnect would keep reporting the replacement runtime as
// connected.
func TestRosterRuntimeEpisodeAdvancesOnReconnect(t *testing.T) {
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		ReconnectMax: 10, ReconnectBackoff: []time.Duration{0}, OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{SessionID: connected.ID, ClientID: "client-a", ChannelID: "ch-1", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	before, ok := manager.RosterRuntimeSnapshot(ctx, info.ID)
	if !ok || !before.EpisodeOK {
		t.Fatalf("pre-reconnect snapshot = %+v, %v", before, ok)
	}

	if err := manager.Reconnect(ctx, connected.ID); err != nil {
		t.Fatal(err)
	}
	after, ok := manager.RosterRuntimeSnapshot(ctx, info.ID)
	if !ok {
		t.Fatal("post-reconnect snapshot failed")
	}
	if !after.EpisodeOK || after.Episode == before.Episode {
		t.Fatalf("episode after reconnect = %q, %v; want a fresh episode (was %q)", after.Episode, after.EpisodeOK, before.Episode)
	}
	if after.Status != StatusConnected {
		t.Fatalf("status after reconnect = %v, want %v", after.Status, StatusConnected)
	}
	if episode, ok := manager.RosterRuntimeIncarnation(ctx, info.ID); !ok || episode != after.Episode {
		t.Fatalf("incarnation after reconnect = %q, %v; want %q, true", episode, ok, after.Episode)
	}
}

// The asset-refreshing reconnect path (the roster broadcast flow) replaces
// the runtime and the asset together: the episode must advance in the same
// commit, and the snapshot must report the replacement asset.
func TestRosterRuntimeEpisodeAdvancesOnAssetReconnect(t *testing.T) {
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		ReconnectMax: 10, ReconnectBackoff: []time.Duration{0}, OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-asset-a", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{SessionID: connected.ID, ClientID: "client-a", ChannelID: "ch-1", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	before, ok := manager.RosterRuntimeSnapshot(ctx, info.ID)
	if !ok || !before.EpisodeOK {
		t.Fatalf("pre-reconnect snapshot = %+v, %v", before, ok)
	}

	if err := manager.ReconnectOnceFor(ctx, connected.ID, Asset{ID: "ssh-asset-b", Kind: KindSSH}); err != nil {
		t.Fatal(err)
	}
	after, ok := manager.RosterRuntimeSnapshot(ctx, info.ID)
	if !ok {
		t.Fatal("post-reconnect snapshot failed")
	}
	if after.Asset.ID != "ssh-asset-b" {
		t.Fatalf("snapshot asset after asset reconnect = %q, want ssh-asset-b", after.Asset.ID)
	}
	if !after.EpisodeOK || after.Episode == before.Episode {
		t.Fatalf("episode after asset reconnect = %q, %v; want a fresh episode (was %q)", after.Episode, after.EpisodeOK, before.Episode)
	}
}

// A durable tab's daemon terminal survives the reconnect: the reattach keeps
// the daemon-assigned incarnation, so the roster episode stays stable.
func TestRosterRuntimeEpisodeStableOnDurableReconnect(t *testing.T) {
	provider := &rosterIdentityDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	resolver := &countingResolver{provider: provider}
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		DurableResolver: resolver, ReconnectBackoff: []time.Duration{0}, OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-durable", Kind: KindSSH})
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
	before, ok := manager.RosterRuntimeIncarnation(ctx, info.ID)
	if !ok || before != "daemon-1" {
		t.Fatalf("pre-reconnect durable episode = %q, %v; want daemon-1, true", before, ok)
	}

	if err := manager.Reconnect(ctx, connected.ID); err != nil {
		t.Fatal(err)
	}
	after, ok := manager.RosterRuntimeIncarnation(ctx, info.ID)
	if !ok || after != before {
		t.Fatalf("durable episode after reconnect = %q, %v; want stable %q", after, ok, before)
	}
}

// The snapshot carries the tab's OSC-7-reported working directory from the
// same critical section as the episode and asset: before any report the cwd
// is honestly empty, and a report through the real pump path lands in the
// snapshot without disturbing the other fields.
func TestRosterRuntimeSnapshotCarriesReportedCwd(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{Connector: connector, Terminals: newFakeTerminalFactory(), OpenAccess: true})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "roster-cwd-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "roster-cwd", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	before, ok := manager.RosterRuntimeSnapshot(ctx, info.ID)
	if !ok {
		t.Fatal("snapshot lookup failed")
	}
	if before.Cwd != "" {
		t.Fatalf("snapshot cwd before any report = %q, want empty (unknown)", before.Cwd)
	}

	if err := connector.transport(0).channel(0).emit([]byte("\x1b]7;file://buildhost/srv/app\x1b\\")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, ok := manager.RosterRuntimeSnapshot(ctx, info.ID)
		if ok && snapshot.Cwd == "/srv/app" {
			if !snapshot.EpisodeOK || snapshot.Episode != before.Episode {
				t.Fatalf("cwd report disturbed the episode: %q vs %q", snapshot.Episode, before.Episode)
			}
			if snapshot.Asset.ID != "roster-cwd-asset" || snapshot.Status != StatusConnected {
				t.Fatalf("cwd report disturbed the snapshot: %+v", snapshot)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("snapshot never carried the OSC 7 cwd")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitRosterSnapshotCwd(t *testing.T, manager *Manager, ctx context.Context, tabID, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, ok := manager.RosterRuntimeSnapshot(ctx, tabID)
		if ok && snapshot.Cwd == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot cwd never became %q (last %+v, %v)", want, snapshot, ok)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A non-durable replacement resets the cwd evidence with the episode: the
// pre-reconnect runtime's OSC 7 report never describes the replacement, a
// stale feed from the retired channel cannot repopulate it, and the
// replacement's own report re-establishes it.
func TestRosterRuntimeSnapshotCwdResetsOnReplacement(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(),
		ReconnectMax: 10, ReconnectBackoff: []time.Duration{0}, OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "ch-1", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	retired := connector.transport(0).channel(0)
	if err := retired.emit([]byte("\x1b]7;file://host/srv/app\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitRosterSnapshotCwd(t, manager, ctx, info.ID, "/srv/app")
	before, ok := manager.RosterRuntimeSnapshot(ctx, info.ID)
	if !ok {
		t.Fatal("pre-reconnect snapshot failed")
	}

	if err := manager.Reconnect(ctx, connected.ID); err != nil {
		t.Fatal(err)
	}
	after, ok := manager.RosterRuntimeSnapshot(ctx, info.ID)
	if !ok {
		t.Fatal("post-reconnect snapshot failed")
	}
	if !after.EpisodeOK || after.Episode == before.Episode {
		t.Fatalf("episode did not advance on the replacement: %q (was %q)", after.Episode, before.Episode)
	}
	if after.Cwd != "" {
		t.Fatalf("replacement inherited the old runtime's cwd %q, want unknown", after.Cwd)
	}

	// A stale feed from the retired channel never repopulates the new
	// episode. The manager may already have torn the channel down by the
	// time the feed lands — an even stronger guarantee, so both outcomes
	// are accepted and the assertion is on the snapshot alone.
	if err := retired.emit([]byte("\x1b]7;file://host/old/stale\x1b\\")); err != nil {
		t.Logf("retired channel already closed before the stale feed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	snapshot, ok := manager.RosterRuntimeSnapshot(ctx, info.ID)
	if !ok || snapshot.Cwd != "" {
		t.Fatalf("retired channel repopulated the new episode's cwd: %+v", snapshot)
	}

	// The replacement's own report re-establishes the directory.
	replacement := connector.transport(1).channel(0)
	if err := replacement.emit([]byte("\x1b]7;file://host/srv/new\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitRosterSnapshotCwd(t, manager, ctx, info.ID, "/srv/new")
}

// A durable tab's daemon terminal is the same runtime across the reconnect:
// the episode stays stable and so does the cwd evidence, while the reattached
// channel's own reports keep updating it.
func TestRosterRuntimeSnapshotCwdSurvivesDurableReconnect(t *testing.T) {
	provider := &rosterIdentityDurableProvider{fakeDurableProvider: newFakeDurableProvider()}
	resolver := &countingResolver{provider: provider}
	manager := NewManager(Config{
		Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(),
		DurableResolver: resolver, ReconnectBackoff: []time.Duration{0}, OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-durable", Kind: KindSSH})
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
	if err := provider.attachment(info.ID, 0).emit([]byte("\x1b]7;file://host/srv/durable\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitRosterSnapshotCwd(t, manager, ctx, info.ID, "/srv/durable")
	before, ok := manager.RosterRuntimeSnapshot(ctx, info.ID)
	if !ok || before.Episode != "daemon-1" {
		t.Fatalf("pre-reconnect durable snapshot = %+v, %v", before, ok)
	}

	if err := manager.Reconnect(ctx, connected.ID); err != nil {
		t.Fatal(err)
	}
	after, ok := manager.RosterRuntimeSnapshot(ctx, info.ID)
	if !ok {
		t.Fatal("post-reconnect durable snapshot failed")
	}
	if after.Episode != before.Episode {
		t.Fatalf("durable episode advanced to %q, want stable %q", after.Episode, before.Episode)
	}
	if after.Cwd != "/srv/durable" {
		t.Fatalf("durable reconnect dropped the daemon-reported cwd %q", after.Cwd)
	}

	// The reattached channel's own reports keep updating the directory.
	if err := provider.attachment(info.ID, 1).emit([]byte("\x1b]7;file://host/srv/durable-next\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitRosterSnapshotCwd(t, manager, ctx, info.ID, "/srv/durable-next")
}

// Feeds racing a reconnect can never leak the old runtime's directory into
// the replacement episode: after the commit the snapshot holds either the
// reset unknown or the replacement's own report.
func TestRosterRuntimeSnapshotCwdConcurrentFeedAndReconnect(t *testing.T) {
	connector := newFakeConnector()
	manager := NewManager(Config{
		Connector: connector, Terminals: newFakeTerminalFactory(),
		ReconnectMax: 10, ReconnectBackoff: []time.Duration{0}, OpenAccess: true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx := context.Background()
	connected, err := manager.Connect(ctx, Asset{ID: "ssh-asset", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ctx, OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "ch-1", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	retired := connector.transport(0).channel(0)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				if err := retired.emit([]byte("\x1b]7;file://host/srv/old\x1b\\")); err != nil {
					return
				}
				time.Sleep(time.Millisecond)
			}
		}
	}()
	if err := manager.Reconnect(ctx, connected.ID); err != nil {
		t.Fatal(err)
	}
	close(stop)
	<-done

	// The old runtime's report must never appear in the replacement episode.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		snapshot, ok := manager.RosterRuntimeSnapshot(ctx, info.ID)
		if ok && snapshot.Cwd == "/srv/old" {
			t.Fatalf("old runtime's cwd leaked into the replacement episode: %+v", snapshot)
		}
		time.Sleep(5 * time.Millisecond)
	}

	replacement := connector.transport(1).channel(0)
	if err := replacement.emit([]byte("\x1b]7;file://host/srv/new\x1b\\")); err != nil {
		t.Fatal(err)
	}
	waitRosterSnapshotCwd(t, manager, ctx, info.ID, "/srv/new")
}
