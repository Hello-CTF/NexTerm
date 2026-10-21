package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// The group snapshot returns exactly one state per requested tab, in request
// order, carrying each tab's own version and idle reading.
func TestQuietGroupSnapshotExactRoster(t *testing.T) {
	manager, _, terminals, session := openOpenTestSession(t, KindSSH)
	first := openTestTab(t, manager, session, "client-a", "a-1")
	second := openTestTab(t, manager, session, "client-a", "a-2")
	terminals.terminal(first.ID).setQuiet(1200, 7, false)
	terminals.terminal(second.ID).setQuiet(30, 42, false)

	states, err := manager.QuietGroupSnapshot(context.Background(), []string{first.ID, second.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("states = %+v, want one per requested tab", states)
	}
	if states[0].TabID != first.ID || states[0].IdleMS != 1200 || states[0].Version != 7 {
		t.Fatalf("first state = %+v", states[0])
	}
	if states[1].TabID != second.ID || states[1].IdleMS != 30 || states[1].Version != 42 {
		t.Fatalf("second state = %+v", states[1])
	}
}

// A roster listing the same tab twice is rejected instead of silently
// collapsing the duplicate.
func TestQuietGroupSnapshotRejectsDuplicateRoster(t *testing.T) {
	manager, _, _, session := openOpenTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if _, err := manager.QuietGroupSnapshot(context.Background(), []string{tab.ID, tab.ID}); !errors.Is(err, ErrQuietRoster) {
		t.Fatalf("duplicate roster = %v, want ErrQuietRoster", err)
	}
}

// A tab that vanished mid-read fails the whole snapshot: an incomplete
// roster never succeeds.
func TestQuietGroupSnapshotFailsOnVanishedTab(t *testing.T) {
	manager, _, _, session := openOpenTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	if _, err := manager.QuietGroupSnapshot(context.Background(), []string{tab.ID, "tab-missing"}); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("vanished tab = %v, want ErrTabNotFound", err)
	}
}

// A closed-and-removed tab is gone from the roster: the snapshot fails like
// any vanished tab instead of reporting stale idle as live.
func TestQuietGroupSnapshotFailsOnClosedTab(t *testing.T) {
	manager, _, terminals, session := openOpenTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	terminals.terminal(tab.ID).setQuiet(500, 3, true)
	if err := manager.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.QuietGroupSnapshot(context.Background(), []string{tab.ID}); !errors.Is(err, ErrTabNotFound) {
		t.Fatalf("closed tab = %v, want ErrTabNotFound", err)
	}
}

// A live tab whose terminal already reported closure stays in the roster
// with its closed fact visible.
func TestQuietGroupSnapshotReportsTerminalClosed(t *testing.T) {
	manager, _, terminals, session := openOpenTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	terminals.terminal(tab.ID).setQuiet(500, 3, true)
	states, err := manager.QuietGroupSnapshot(context.Background(), []string{tab.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || !states[0].Closed {
		t.Fatalf("states = %+v, want one closed tab state", states)
	}
}

// A close that blocks before the terminal's own close keeps the snapshot
// consistent: the pre-close snapshot and the mid-close snapshot carry the
// same version with the same open state, and only the terminal's close
// moves the version and the closed fact together.
func TestQuietGroupSnapshotCloseBeforeTerminalClose(t *testing.T) {
	manager, _, terminals, session := openOpenTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	term := terminals.terminal(tab.ID)
	term.setQuiet(10, 5, false)

	before, err := manager.QuietGroupSnapshot(context.Background(), []string{tab.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].Version != 5 || before[0].Closed {
		t.Fatalf("open snapshot = %+v, want version 5 open", before)
	}

	// Block the tab's close between the manager teardown and the terminal's
	// own Close: the manager-side closed/destroying flags are already set,
	// but they must not leak into the snapshot — only the terminal's
	// versioned state speaks.
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHook := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseHook()
	term.closeHook(func() {
		close(entered)
		<-release
	})
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.CloseTab(tab.ID) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("tab close never reached the terminal close")
	}
	mid, err := manager.QuietGroupSnapshot(context.Background(), []string{tab.ID})
	if err != nil {
		t.Fatalf("mid-close snapshot = %v", err)
	}
	if len(mid) != 1 || mid[0].Version != before[0].Version || mid[0].Closed {
		t.Fatalf("mid-close snapshot = %+v, want the same version %d still open", mid, before[0].Version)
	}
	releaseHook()
	if err := <-closeDone; err != nil {
		t.Fatalf("CloseTab = %v", err)
	}
}

// A tab removed between the consistency reads must never be reported with a
// stale state: the whole snapshot fails closed.
func TestQuietGroupSnapshotFailsOnInterleavedRemove(t *testing.T) {
	manager, _, terminals, session := openOpenTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	term := terminals.terminal(tab.ID)
	term.setQuiet(10, 5, false)
	var calls atomic.Int32
	term.quietHook(func() {
		if calls.Add(1) == 1 {
			if err := manager.CloseTab(tab.ID); err != nil {
				t.Errorf("CloseTab: %v", err)
			}
		}
	})
	if _, err := manager.QuietGroupSnapshot(context.Background(), []string{tab.ID}); !errors.Is(err, ErrQuietSnapshot) {
		t.Fatalf("snapshot = %v, want ErrQuietSnapshot after the interleaved remove", err)
	}
}

// A tab removed and recreated under the same ID between the consistency
// reads fails the whole snapshot instead of silently adopting the new
// incarnation — the caller never sees a same-ID state it cannot trust.
func TestQuietGroupSnapshotRechecksIncarnation(t *testing.T) {
	manager, _, terminals, session := openOpenTestSession(t, KindSSH)
	tab := openTestTab(t, manager, session, "client-a", "a-1")
	term := terminals.terminal(tab.ID)
	term.setQuiet(10, 5, false)
	var calls atomic.Int32
	term.quietHook(func() {
		if calls.Add(1) == 1 {
			if err := manager.CloseTab(tab.ID); err != nil {
				t.Errorf("CloseTab: %v", err)
			}
			if _, err := manager.OpenTab(context.Background(), OpenTabOptions{
				TabID: tab.ID, SessionID: session.ID, ClientID: "client-a", ChannelID: "a-2", Cols: 80, Rows: 24,
			}); err != nil {
				t.Errorf("recreate tab: %v", err)
			}
			terminals.terminal(tab.ID).setQuiet(20, 99, false)
		}
	})
	if _, err := manager.QuietGroupSnapshot(context.Background(), []string{tab.ID}); !errors.Is(err, ErrQuietSnapshot) {
		t.Fatalf("snapshot = %v, want ErrQuietSnapshot instead of the recreated incarnation's state", err)
	}
}

// Device sessions are owner-isolated: another account's quiet read is
// refused, the owner reads fine.
func TestQuietGroupSnapshotOwnerIsolation(t *testing.T) {
	connector := newFakeConnector()
	terminals := newFakeTerminalFactory()
	manager := NewManager(Config{Connector: connector, Terminals: terminals})
	t.Cleanup(func() { _ = manager.Close() })
	ownerCtx := ipc.WithUserID(context.Background(), "user-1")
	session, err := manager.Connect(ownerCtx, Asset{ID: "device-1", Name: "device", Kind: KindDevice})
	if err != nil {
		t.Fatal(err)
	}
	info, err := manager.OpenTab(ownerCtx, OpenTabOptions{SessionID: session.ID, Cols: 80, Rows: 24, Detached: true, Ephemeral: true})
	if err != nil {
		t.Fatal(err)
	}
	otherCtx := ipc.WithUserID(context.Background(), "user-2")
	if _, err := manager.QuietGroupSnapshot(otherCtx, []string{info.ID}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-owner quiet read = %v, want ErrForbidden", err)
	}
	if _, err := manager.QuietGroupSnapshot(ownerCtx, []string{info.ID}); err != nil {
		t.Fatalf("owner quiet read = %v", err)
	}
}
