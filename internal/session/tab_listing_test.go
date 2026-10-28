package session

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func TestDetachedSinceOpenOnlyUntilAClientLeaves(t *testing.T) {
	reopened := &Tab{reopened: true, subscribers: map[string]subscriber{}}
	if !reopened.detachedSinceOpenLocked() {
		t.Fatal("a reopened helper session nobody attached to is not marked")
	}
	reopened.subscribers["a-1"] = subscriber{}
	if reopened.detachedSinceOpenLocked() {
		t.Fatal("a subscribed tab is marked")
	}
	reopened.dropSubscriberLocked("a-1")
	if reopened.detachedSinceOpenLocked() {
		t.Fatal("a tab a client left is still marked: its detach time is real now")
	}
	created := &Tab{subscribers: map[string]subscriber{}}
	if created.detachedSinceOpenLocked() {
		t.Fatal("a tab this app run created is marked")
	}
}

func TestSpectatorsDoNotMoveTheDetachTime(t *testing.T) {
	tab := &Tab{subscribers: map[string]subscriber{
		"client":  {},
		"preview": {spectator: true},
	}}
	if tab.detachedAtLocked() != 0 {
		t.Fatal("a tab with an interactive subscriber reports a detach time")
	}
	tab.dropSubscriberLocked("client")
	left := tab.detachedAt
	// The spectator still watching does not keep the tab attached.
	if left.IsZero() || tab.detachedAtLocked() != left.UnixMilli() {
		t.Fatalf("after the last interactive client left: detached at %v, listed %d", left, tab.detachedAtLocked())
	}
	tab.dropSubscriberLocked("preview")
	if !tab.detachedAt.Equal(left) || tab.detaches != 1 {
		t.Fatalf("a spectator leaving moved the detach time to %v (detaches %d)", tab.detachedAt, tab.detaches)
	}
}

func listedTab(t *testing.T, manager *Manager, id string) TabInfo {
	t.Helper()
	for _, info := range manager.ListTabs() {
		if info.ID == id {
			return info
		}
	}
	t.Fatalf("tab %s not listed", id)
	return TabInfo{}
}

func TestListTabsReportsCreatedAndDetachedTimes(t *testing.T) {
	provider := newFakeDurableProvider()
	manager, _, _, connected := newDurableTestManager(t, provider)
	before := time.Now().UnixMilli()
	info := openDurableTestTab(t, manager, connected, "listed-tab", "a-1", false)

	attached := listedTab(t, manager, info.ID)
	if attached.CreatedAt < before || attached.CreatedAt > time.Now().UnixMilli() {
		t.Fatalf("created at %d, want between %d and now", attached.CreatedAt, before)
	}
	if attached.DetachedAt != 0 {
		t.Fatalf("a subscribed tab reports detached at %d", attached.DetachedAt)
	}
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1", ReplayBytes: -1}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DetachChannel("a-1"); err != nil {
		t.Fatal(err)
	}
	if still := listedTab(t, manager, info.ID); still.DetachedAt != 0 {
		t.Fatalf("with one subscriber left the tab reports detached at %d", still.DetachedAt)
	}
	beforeDetach := time.Now().UnixMilli()
	if err := manager.DetachChannel("b-1"); err != nil {
		t.Fatal(err)
	}
	detached := listedTab(t, manager, info.ID)
	if detached.DetachedAt < beforeDetach || detached.DetachedAt < detached.CreatedAt {
		t.Fatalf("detached at %d, want at or after %d", detached.DetachedAt, beforeDetach)
	}
	if age := detached.DetachedMSAgo; age < 0 || age > time.Now().UnixMilli()-beforeDetach {
		t.Fatalf("detached age %d, want within the time since the detach", age)
	}
	if detached.CreatedAt != attached.CreatedAt {
		t.Fatalf("created at moved from %d to %d", attached.CreatedAt, detached.CreatedAt)
	}
	// The fake helper reports no attention state.
	if detached.State != "" {
		t.Fatalf("state = %q, want none", detached.State)
	}
}

func TestListTabsRecordsDetachTimeWhenSentToBackground(t *testing.T) {
	provider := newFakeDurableProvider()
	manager, _, _, connected := newDurableTestManager(t, provider)
	info := openDurableTestTab(t, manager, connected, "background-tab", "a-1", false)
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-a", ChannelID: "a-2", ReplayBytes: -1}); err != nil {
		t.Fatal(err)
	}
	if attached := listedTab(t, manager, info.ID); attached.DetachedAt != 0 {
		t.Fatalf("an attached tab reports detached at %d", attached.DetachedAt)
	}
	before := time.Now().UnixMilli()
	// Both channels of the client leave at once.
	if err := manager.BackgroundTab(context.Background(), info.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	detached := listedTab(t, manager, info.ID)
	if detached.Subscribers != 0 || detached.DetachedAt < before {
		t.Fatalf("after going to the background the tab reports %d subscribers, detached at %d (want >= %d)", detached.Subscribers, detached.DetachedAt, before)
	}
}

func TestListTabsRecordsDetachTimeWhenTheLastClientDetaches(t *testing.T) {
	provider := newFakeDurableProvider()
	manager, _, _, connected := newDurableTestManager(t, provider)
	info := openDurableTestTab(t, manager, connected, "client-tab", "a-1", false)
	if _, err := manager.AttachTab(context.Background(), info.ID, AttachOptions{ClientID: "client-b", ChannelID: "b-1", ReplayBytes: -1}); err != nil {
		t.Fatal(err)
	}
	if err := manager.DetachClient(info.ID, "client-a"); err != nil {
		t.Fatal(err)
	}
	if still := listedTab(t, manager, info.ID); still.DetachedAt != 0 {
		t.Fatalf("with client-b attached the tab reports detached at %d", still.DetachedAt)
	}
	before := time.Now().UnixMilli()
	if err := manager.DetachClient(info.ID, "client-b"); err != nil {
		t.Fatal(err)
	}
	if detached := listedTab(t, manager, info.ID); detached.DetachedAt < before {
		t.Fatalf("detached at %d, want at or after %d", detached.DetachedAt, before)
	}
}

type namedStateAttachment struct {
	base.DurableAttachment
	state string
}

func (a namedStateAttachment) StateName() string { return a.state }

func TestDurableAttentionState(t *testing.T) {
	if got := durableAttentionState(nil); got != "" {
		t.Fatalf("a regular tab reports state %q", got)
	}
	if got := durableAttentionState(namedStateAttachment{}); got != "" {
		t.Fatalf("a helper without state detection reports %q", got)
	}
	if got := durableAttentionState(namedStateAttachment{state: base.SessionNeedsMe}); got != base.SessionNeedsMe {
		t.Fatalf("state = %q", got)
	}
}
