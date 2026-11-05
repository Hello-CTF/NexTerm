//go:build darwin || linux

package production

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

func newSharedHelperManager(t *testing.T, fixture *remoteDaemonFixture, name string) (*session.Manager, *session.Session) {
	t.Helper()
	transport := local.NewWithConfig(local.Config{Shell: "/bin/sh"})
	manager := session.NewManager(session.Config{
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return transport, nil
		}),
		DurableResolver: &recordingDurableResolver{provider: fixture.provider},
		OpenAccess:      true,
	})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(t.Context(), session.Asset{ID: name, Kind: session.KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	return manager, connected
}

func waitTabSize(t *testing.T, manager *session.Manager, tabID, owner string, cols, rows uint32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var info session.TabInfo
	for time.Now().Before(deadline) {
		tab, err := manager.Tab(tabID)
		if err != nil {
			t.Fatal(err)
		}
		info = tab.Info()
		if info.SizeOwner == owner && info.Cols == cols && info.Rows == rows {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("tab = owner %q %dx%d, want owner %q %dx%d", info.SizeOwner, info.Cols, info.Rows, owner, cols, rows)
}

// Two desktops (each its own session manager and helper connection) show one
// durable terminal. Whichever sized the PTY last, the other renders that size
// as a follower instead of drawing the output at its own window size.
func TestTwoDesktopsOnOneHelperFollowEachOthersSize(t *testing.T) {
	fixture := newRemoteDaemonFixture(t)
	large, largeSession := newSharedHelperManager(t, fixture, "large-desktop")
	small, smallSession := newSharedHelperManager(t, fixture, "small-desktop")
	opened, err := large.OpenTab(t.Context(), session.OpenTabOptions{
		SessionID: largeSession.ID, ClientID: "large", ChannelID: "large-1", Cols: 160, Rows: 50,
		Durable: &session.DurableTabOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := small.RecoverRemoteDurable(t.Context(), opened.ID, session.OpenTabOptions{
		SessionID: smallSession.ID, ClientID: "small", ChannelID: "small-1", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatal(err)
	}

	if err := small.Resize(t.Context(), opened.ID, "small", 90, 30); err != nil {
		t.Fatal(err)
	}
	waitTabSize(t, large, opened.ID, "other-device", 90, 30)

	if err := large.TakeSize(opened.ID, "large"); err != nil {
		t.Fatal(err)
	}
	if err := large.Resize(t.Context(), opened.ID, "large", 150, 45); err != nil {
		t.Fatal(err)
	}
	waitTabSize(t, small, opened.ID, "other-device", 150, 45)
	waitTabSize(t, large, opened.ID, "large", 150, 45)
}
