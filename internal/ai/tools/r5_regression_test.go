package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// R5-F2: a job-created tab read by direct ID classifies an empty legacy
// identity as Gone before comparing the creating session — the documented
// create-then-read path never reports a vanished tab as cross-scope.

func TestLegacyCreatedTabEmptyIdentityIsGone(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", Cols: 80, Rows: 24}}
	registry := NewRegistry(Dependencies{
		Terminal:   terminal,
		TabSession: func(tabID string) string { return "" },
	})
	scope := Scope{SessionID: "s1", TabID: "t1"}
	created := registry.Execute(context.Background(), "job", scope,
		p1Call("c", "terminal_create", `{"title":"build"}`), nil)
	if !created.OK || created.Terminal == nil {
		t.Fatalf("create = %+v", created)
	}
	result := registry.Execute(context.Background(), "job", scope,
		p1Call("r", "read_screen", fmt.Sprintf(`{"tab_id":%q}`, created.Terminal.TabID)), nil)
	if result.OK || !strings.Contains(result.Text, ErrTerminalGone.Error()) {
		t.Fatalf("created tab with empty identity = %+v, want Gone", result)
	}
	if strings.Contains(result.Text, ErrCrossScope.Error()) {
		t.Fatalf("created tab empty identity misclassified as cross-scope: %q", result.Text)
	}
}

// R5-F1: a success return must re-verify the whole group's quiet state, so a
// tab that became busy during a later tab's snapshot in the same round is
// revoked instead of resting the return on its stale idle.
func TestQuietFinalVerificationRevokesSameRoundBusy(t *testing.T) {
	t1 := fakeTab("t1", "s1", "$ ", 0)
	t2 := fakeTab("t2", "s1", "$ ", 0)
	t1.screen.IdleMS = 100
	t2.screen.IdleMS = 5
	fake := newTabFake(t1, t2)

	// t2's second snapshot is round one's quiet evaluation: flip t1 busy in
	// it, after t1's own snapshot for the round already returned idle=100.
	var mu sync.Mutex
	t2Snapshots := 0
	fake.snapshotHook = func(tabID string) {
		if tabID != "t2" {
			return
		}
		mu.Lock()
		t2Snapshots++
		fire := t2Snapshots == 2
		mu.Unlock()
		if fire {
			fake.mu.Lock()
			t1.screen.IdleMS = 0
			t2.screen.IdleMS = 100
			fake.mu.Unlock()
		}
	}
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"quiet_ms":50,"mode":"all","timeout_ms":200}`), nil)

	if result.OK || result.Wait == nil || !result.Wait.TimedOut {
		t.Fatalf("same-round stale idle faked an all-quiet success: %+v", result)
	}
	byID := map[string]bool{}
	for _, tab := range result.Wait.Tabs {
		byID[tab.TabID] = tab.Matched
	}
	if byID["t1"] {
		t.Fatalf("t1 reported satisfied from a stale snapshot: %+v", result.Wait.Tabs)
	}
	if !byID["t2"] {
		t.Fatalf("t2 not satisfied: %+v", result.Wait.Tabs)
	}
}

// The same interleave must not produce an any-mode success on the stale tab.
func TestQuietFinalVerificationRevokesSameRoundBusyAny(t *testing.T) {
	t1 := fakeTab("t1", "s1", "$ ", 0)
	t2 := fakeTab("t2", "s1", "$ ", 0)
	t1.screen.IdleMS = 100
	t2.screen.IdleMS = 5
	fake := newTabFake(t1, t2)

	var mu sync.Mutex
	t2Snapshots := 0
	fake.snapshotHook = func(tabID string) {
		if tabID != "t2" {
			return
		}
		mu.Lock()
		t2Snapshots++
		fire := t2Snapshots == 2
		mu.Unlock()
		if fire {
			fake.mu.Lock()
			t1.screen.IdleMS = 0
			fake.mu.Unlock()
		}
	}
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"quiet_ms":50,"mode":"any","timeout_ms":200}`), nil)

	if result.OK {
		t.Fatalf("any-mode returned a stale-tab success: %+v", result)
	}
	for _, tab := range result.Wait.Tabs {
		if tab.Matched && tab.TabID == "t1" {
			t.Fatalf("t1 counted satisfied from a stale snapshot: %+v", result.Wait.Tabs)
		}
	}
}
