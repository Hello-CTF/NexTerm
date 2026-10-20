package tools

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// R7-F1: the group snapshot must cover the exact requested roster — every
// requested tab exactly once, no missing/duplicate/extra — and an incomplete
// roster or a failed read never produces a success. A tab that vanishes
// during the final group read is revoked with the failure visible.

// vanishQuietTerminal drops t1 from the roster and the tab set on a chosen
// group read.
type vanishQuietTerminal struct {
	*tabFakeTerminal
	mu       sync.Mutex
	versions map[string]uint64
	reads    int
	vanishOn int
}

func (f *vanishQuietTerminal) QuietGroupSnapshot(_ context.Context, tabIDs []string) ([]TabQuietState, error) {
	f.mu.Lock()
	f.reads++
	vanish := f.reads == f.vanishOn
	f.mu.Unlock()
	if vanish {
		f.tabFakeTerminal.mu.Lock()
		delete(f.tabFakeTerminal.tabs, "t1")
		f.tabFakeTerminal.mu.Unlock()
	}
	states := []TabQuietState{}
	for _, tabID := range tabIDs {
		f.tabFakeTerminal.mu.Lock()
		tab, ok := f.tabFakeTerminal.tabs[tabID]
		f.tabFakeTerminal.mu.Unlock()
		if !ok {
			continue
		}
		f.mu.Lock()
		version := f.versions[tabID]
		f.mu.Unlock()
		states = append(states, TabQuietState{TabID: tabID, IdleMS: tab.screen.IdleMS, Version: version})
	}
	return states, nil
}

func newVanishQuietFake(vanishOn int) *vanishQuietTerminal {
	t1 := fakeTab("t1", "s1", "$ ", 0)
	t2 := fakeTab("t2", "s1", "$ ", 0)
	t1.screen.IdleMS = 100
	t2.screen.IdleMS = 100
	return &vanishQuietTerminal{
		tabFakeTerminal: newTabFake(t1, t2),
		versions:        map[string]uint64{"t1": 1, "t2": 1},
		vanishOn:        vanishOn,
	}
}

func TestQuietGroupVanishedTabRevokesAllSuccess(t *testing.T) {
	fake := newVanishQuietFake(2)
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"quiet_ms":50,"mode":"all","timeout_ms":200}`), nil)

	if result.OK || result.Wait == nil || !result.Wait.TimedOut {
		t.Fatalf("vanished tab inherited a stale all-quiet success: %+v", result)
	}
	for _, tab := range result.Wait.Tabs {
		if tab.TabID == "t1" && tab.Matched {
			t.Fatalf("vanished t1 reported satisfied: %+v", result.Wait.Tabs)
		}
		if tab.TabID == "t1" && !strings.Contains(tab.Note, "安静快照失败") {
			t.Fatalf("vanished t1 failure not visible: %+v", result.Wait.Tabs)
		}
	}
}

func TestQuietGroupVanishedTabRevokesAnySuccess(t *testing.T) {
	fake := newVanishQuietFake(2)
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"quiet_ms":50,"mode":"any","timeout_ms":200}`), nil)

	if result.OK || result.Wait == nil || !result.Wait.TimedOut {
		t.Fatalf("vanished tab produced an any-mode success: %+v", result)
	}
	for _, tab := range result.Wait.Tabs {
		if tab.TabID == "t1" && tab.Matched {
			t.Fatalf("vanished t1 counted satisfied: %+v", result.Wait.Tabs)
		}
	}
}
