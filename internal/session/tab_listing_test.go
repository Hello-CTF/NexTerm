package session

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

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

type pushedStateAttachment struct {
	base.DurableAttachment
	state *base.SessionState
}

func (a pushedStateAttachment) SessionState() *base.SessionState { return a.state }

type readStateAttachment struct {
	base.DurableAttachment
	state *base.SessionState
	after uint64
}

func (a *readStateAttachment) SessionState(after uint64) *base.SessionState {
	a.after = after
	return a.state
}

func TestDurableAttentionState(t *testing.T) {
	if got := durableAttentionState(nil); got != "" {
		t.Fatalf("a regular tab reports state %q", got)
	}
	if got := durableAttentionState(pushedStateAttachment{}); got != "" {
		t.Fatalf("a helper without state detection reports %q", got)
	}
	if got := durableAttentionState(pushedStateAttachment{state: &base.SessionState{State: base.SessionNeedsMe}}); got != base.SessionNeedsMe {
		t.Fatalf("pushed state = %q", got)
	}
	read := &readStateAttachment{state: &base.SessionState{State: base.SessionJustFinished}}
	if got := durableAttentionState(read); got != base.SessionJustFinished {
		t.Fatalf("read state = %q", got)
	}
	if read.after != ^uint64(0) {
		t.Fatalf("in-process read asked for events after %d, want none", read.after)
	}
}
