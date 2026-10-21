package session

import (
	"context"
	"fmt"
	"testing"
	"time"

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
