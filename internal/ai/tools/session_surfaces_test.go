package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/session"
)

// The session adapter consumes the merged M69 terminal primitives:
// WritePasteSubmit delegates to the manager's combined paste+delay+Enter
// write lock, and QuietGroupSnapshot delegates to the exact-roster group
// snapshot with per-tab versions and creation incarnations, so production
// send_keys never splits the submission and production multi-tab quiet
// waits never degrade to a serial scan.

func TestSessionAdapterPasteSubmitHoldsLockAcrossDelayAndEnter(t *testing.T) {
	fixture := newAtomicSessionFixture(t, false)
	adapter := SessionTerminal(fixture.manager)
	submitter, ok := adapter.(PasteSubmitTerminal)
	if !ok {
		t.Fatal("session adapter does not implement PasteSubmitTerminal")
	}
	ctx := trustedContext()

	type userWrite struct {
		err error
	}
	userDone := make(chan userWrite, 1)
	go func() {
		// A concurrent user write must queue behind the combined submission:
		// it can never interleave between the paste block and the Enter key.
		time.Sleep(20 * time.Millisecond)
		userDone <- userWrite{err: adapter.Write(ctx, fixture.tabID, []byte("USER"))}
	}()
	if err := submitter.WritePasteSubmit(ctx, fixture.tabID, "echo hi", true, 150*time.Millisecond); err != nil {
		t.Fatalf("WritePasteSubmit = %v", err)
	}
	if write := <-userDone; write.err != nil {
		t.Fatalf("concurrent user write = %v", write.err)
	}
	writes := fixture.channel.successfulWrites()
	if len(writes) != 3 {
		t.Fatalf("channel writes = %q, want paste + Enter + user", writes)
	}
	if !strings.Contains(string(writes[0]), "echo hi") {
		t.Fatalf("first write = %q, want the paste block", writes[0])
	}
	if string(writes[1]) != "\r" {
		t.Fatalf("second write = %q, want the Enter key", writes[1])
	}
	if string(writes[2]) != "USER" {
		t.Fatalf("user input interleaved into the submission: %q", writes)
	}
}

func TestSessionAdapterSendKeysNeverSplitsPasteSubmit(t *testing.T) {
	fixture := newAtomicSessionFixture(t, false)
	registry := fixture.registry()
	ctx := trustedContext()

	result := registry.Execute(ctx, "job", fixture.scope(), Call{ID: "p", Name: "send_keys",
		Args: json.RawMessage(`{"keys":"echo hi","paste":true,"enter":true,"submit_delay_ms":20}`)}, nil)
	if !result.OK {
		t.Fatalf("paste submit send_keys = %+v", result)
	}
	if strings.Contains(result.Notes, "separate writes") || strings.Contains(result.Text, "分次写入") {
		t.Fatalf("session-backed send_keys still splits the submission: %+v", result)
	}
	writes := fixture.channel.successfulWrites()
	if len(writes) != 2 || string(writes[1]) != "\r" {
		t.Fatalf("channel writes = %q, want paste block + Enter", writes)
	}
}

func TestSessionAdapterQuietGroupSnapshotExactRoster(t *testing.T) {
	fixture := newAtomicSessionFixture(t, false)
	adapter := SessionTerminal(fixture.manager)
	snapshotter, ok := adapter.(QuietGroupSnapshotTerminal)
	if !ok {
		t.Fatal("session adapter does not implement QuietGroupSnapshotTerminal")
	}
	ctx := trustedContext()

	second, err := fixture.manager.OpenTab(context.Background(), session.OpenTabOptions{
		SessionID: fixture.sessionID, ClientID: "client", ChannelID: "channel-2", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	states, err := snapshotter.QuietGroupSnapshot(ctx, []string{fixture.tabID, second.ID})
	if err != nil {
		t.Fatalf("group snapshot = %v", err)
	}
	if len(states) != 2 || states[0].TabID != fixture.tabID || states[1].TabID != second.ID {
		t.Fatalf("group snapshot roster = %+v", states)
	}
	if states[0].Incarnation == 0 || states[0].Incarnation == states[1].Incarnation {
		t.Fatalf("incarnations not distinct creation identities: %+v", states)
	}
	// A duplicated roster is rejected outright, and an unknown tab fails the
	// whole read instead of returning a partial snapshot.
	if _, err := snapshotter.QuietGroupSnapshot(ctx, []string{fixture.tabID, fixture.tabID}); err == nil {
		t.Fatal("duplicated roster accepted")
	}
	if _, err := snapshotter.QuietGroupSnapshot(ctx, []string{fixture.tabID, "tab-gone"}); err == nil {
		t.Fatal("unknown tab accepted")
	}
}

func TestSessionAdapterQuietGroupSkipsIncarnationAfterRecreate(t *testing.T) {
	fixture := newAtomicSessionFixture(t, false)
	adapter := SessionTerminal(fixture.manager)
	snapshotter, ok := adapter.(QuietGroupSnapshotTerminal)
	if !ok {
		t.Fatal("session adapter does not implement QuietGroupSnapshotTerminal")
	}
	ctx := trustedContext()

	before, err := snapshotter.QuietGroupSnapshot(ctx, []string{fixture.tabID})
	if err != nil || len(before) != 1 {
		t.Fatalf("initial snapshot = %+v err=%v", before, err)
	}
	// Recreate the tab under the same ID: the new creation carries a new
	// incarnation, so a same-ID same-version reading from before the recreate
	// is never mistaken for the same tab.
	if err := fixture.manager.CloseTab(fixture.tabID); err != nil {
		t.Fatal(err)
	}
	reopened, err := fixture.manager.OpenTab(context.Background(), session.OpenTabOptions{
		SessionID: fixture.sessionID, ClientID: "client", ChannelID: "channel", Cols: 80, Rows: 24, TabID: fixture.tabID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ID != fixture.tabID {
		t.Fatalf("reopened tab ID = %s", reopened.ID)
	}
	after, err := snapshotter.QuietGroupSnapshot(ctx, []string{fixture.tabID})
	if err != nil || len(after) != 1 {
		t.Fatalf("post-recreate snapshot = %+v err=%v", after, err)
	}
	if after[0].Incarnation == 0 || after[0].Incarnation == before[0].Incarnation {
		t.Fatalf("recreated tab kept the original incarnation: before=%+v after=%+v", before, after)
	}
}

// multi-tab quiet waits over the session adapter succeed from the consistent
// group snapshot, never from the degraded serial fallback.
func TestSessionAdapterMultiTabQuietWaitNotDegraded(t *testing.T) {
	fixture := newAtomicSessionFixture(t, true)
	registry := fixture.registry()
	ctx := trustedContext()

	second, err := fixture.manager.OpenTab(context.Background(), session.OpenTabOptions{
		SessionID: fixture.sessionID, ClientID: "client", ChannelID: "channel-2", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.clock.Add(100)
	handle := func(tabID string) string { return TerminalHandle{TabID: tabID, SessionID: fixture.sessionID}.String() }
	args, _ := json.Marshal(map[string]any{
		"tab_ids": []string{handle(fixture.tabID), handle(second.ID)}, "quiet_ms": 50, "mode": "all", "timeout_ms": 5000,
	})
	result := registry.Execute(ctx, "job", fixture.scope(), Call{ID: "w", Name: "wait_for", Args: args}, nil)
	if !result.OK || result.Wait == nil || !result.Wait.Satisfied {
		t.Fatalf("multi-tab quiet wait = %+v", result)
	}
	if strings.Contains(result.Notes, "best-effort serial snapshot") || strings.Contains(result.Text, "非一致版本") {
		t.Fatalf("session-backed quiet wait degraded to the serial fallback: %+v", result)
	}
	for _, tab := range result.Wait.Tabs {
		if !tab.Matched {
			t.Fatalf("tab %s not satisfied in the consistent group read: %+v", tab.TabID, result.Wait.Tabs)
		}
	}
}

// A tab recreated between the verification reads — same ID, a version
// coincidence, a different incarnation every read — is never one consistent
// state: a version-only comparison would accept two reads from different
// incarnations and fake a success, so the incarnation rides with the
// version and the wait keeps waiting instead.
type incarnationFlipTerminal struct {
	*tabFakeTerminal
	mu          sync.Mutex
	versions    map[string]uint64
	incarnation map[string]uint64
}

func (f *incarnationFlipTerminal) QuietGroupSnapshot(_ context.Context, tabIDs []string) ([]TabQuietState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// The recreate storm keeps the version pinned while the creation
	// identity moves every read.
	f.incarnation["t1"]++
	states := make([]TabQuietState, 0, len(tabIDs))
	for _, tabID := range tabIDs {
		tab := f.tabs[tabID]
		states = append(states, TabQuietState{TabID: tabID, IdleMS: tab.screen.IdleMS, Version: f.versions[tabID], Incarnation: f.incarnation[tabID]})
	}
	return states, nil
}

func TestQuietConsistentGroupRejectsIncarnationFlip(t *testing.T) {
	t1 := fakeTab("t1", "s1", "$ ", 0)
	t2 := fakeTab("t2", "s1", "$ ", 0)
	t1.screen.IdleMS = 100
	t2.screen.IdleMS = 100
	fake := &incarnationFlipTerminal{
		tabFakeTerminal: newTabFake(t1, t2),
		versions:        map[string]uint64{"t1": 5, "t2": 5},
		incarnation:     map[string]uint64{"t1": 1, "t2": 1},
	}
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"quiet_ms":50,"mode":"all","timeout_ms":200}`), nil)

	if result.OK || result.Wait == nil || !result.Wait.TimedOut {
		t.Fatalf("cross-incarnation reads faked a consistent success: %+v", result)
	}
}

// A closed tab in the agreed group snapshot can never count as quietly
// satisfied, even when its idle reading clears the threshold.
type closedTabTerminal struct {
	*tabFakeTerminal
	versions map[string]uint64
}

func (f *closedTabTerminal) QuietGroupSnapshot(_ context.Context, tabIDs []string) ([]TabQuietState, error) {
	states := make([]TabQuietState, 0, len(tabIDs))
	for _, tabID := range tabIDs {
		tab := f.tabs[tabID]
		states = append(states, TabQuietState{
			TabID: tabID, IdleMS: tab.screen.IdleMS, Version: f.versions[tabID],
			Closed: tabID == "t1", Incarnation: 1,
		})
	}
	return states, nil
}

func TestQuietConsistentGroupClosedTabNeverSatisfied(t *testing.T) {
	t1 := fakeTab("t1", "s1", "$ ", 0)
	t2 := fakeTab("t2", "s1", "$ ", 0)
	t1.screen.IdleMS = 100
	t2.screen.IdleMS = 100
	fake := &closedTabTerminal{tabFakeTerminal: newTabFake(t1, t2), versions: map[string]uint64{"t1": 1, "t2": 1}}
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"quiet_ms":50,"mode":"any","timeout_ms":200}`), nil)

	if !result.OK || result.Wait == nil || !result.Wait.Satisfied {
		t.Fatalf("any-mode wait with one live quiet tab = %+v", result)
	}
	for _, tab := range result.Wait.Tabs {
		if tab.TabID == "t1" {
			if tab.Matched {
				t.Fatalf("closed tab counted as satisfied: %+v", result.Wait.Tabs)
			}
			if !strings.Contains(tab.Note, "已关闭") {
				t.Fatalf("closed tab note = %q", tab.Note)
			}
		}
	}
	if strings.Contains(result.Text, "标签页 t1 ") || strings.Contains(result.Text, "标签页 t1 已满足") {
		t.Fatalf("headline claimed the closed tab: %q", result.Text)
	}
}
