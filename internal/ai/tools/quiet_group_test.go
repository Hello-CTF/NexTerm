package tools

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// R6-F1: a success return must come from one consistent group state. With
// the consistent-snapshot capability, a tab that becomes busy during the
// final verification's own group read is detected by version disagreement
// and revoked — the serial final pass cannot see it.

// groupQuietTerminal adds per-tab quiet versions and a flippable group read.
type groupQuietTerminal struct {
	*tabFakeTerminal
	mu       sync.Mutex
	versions map[string]uint64
	reads    int
	flipOn   int
}

func (f *groupQuietTerminal) QuietGroupSnapshot(_ context.Context, tabIDs []string) ([]TabQuietState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.reads == f.flipOn {
		f.tabs["t1"].screen.IdleMS = 0
		f.versions["t1"]++
	}
	states := make([]TabQuietState, 0, len(tabIDs))
	for _, tabID := range tabIDs {
		tab := f.tabs[tabID]
		states = append(states, TabQuietState{TabID: tabID, IdleMS: tab.screen.IdleMS, Version: f.versions[tabID]})
	}
	return states, nil
}

func newGroupQuietFake(flipOn int) (*groupQuietTerminal, *tabFakeTab, *tabFakeTab) {
	t1 := fakeTab("t1", "s1", "$ ", 0)
	t2 := fakeTab("t2", "s1", "$ ", 0)
	t1.screen.IdleMS = 100
	t2.screen.IdleMS = 100
	fake := &groupQuietTerminal{tabFakeTerminal: newTabFake(t1, t2), versions: map[string]uint64{"t1": 1, "t2": 1}, flipOn: flipOn}
	return fake, t1, t2
}

func TestQuietConsistentGroupRevokesTabBusyDuringFinalRead(t *testing.T) {
	fake, _, _ := newGroupQuietFake(2)
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"quiet_ms":50,"mode":"all","timeout_ms":200}`), nil)

	if result.OK || result.Wait == nil || !result.Wait.TimedOut {
		t.Fatalf("busy-during-final-read faked an all-quiet success: %+v", result)
	}
	byID := map[string]bool{}
	for _, tab := range result.Wait.Tabs {
		byID[tab.TabID] = tab.Matched
	}
	if byID["t1"] {
		t.Fatalf("t1 reported satisfied after becoming busy during the final read: %+v", result.Wait.Tabs)
	}
	if !byID["t2"] {
		t.Fatalf("t2 not satisfied: %+v", result.Wait.Tabs)
	}
}

func TestQuietConsistentGroupAnyReturnsOnlyCurrentlySatisfied(t *testing.T) {
	fake, _, _ := newGroupQuietFake(2)
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"quiet_ms":50,"mode":"any","timeout_ms":200}`), nil)

	if !result.OK || result.Wait == nil || !result.Wait.Satisfied {
		t.Fatalf("any-mode consistent wait = %+v", result)
	}
	if strings.Contains(result.Text, "标签页 t1") {
		t.Fatalf("any headline carried the stale tab: %q", result.Text)
	}
	if !strings.Contains(result.Text, "t2") {
		t.Fatalf("any headline missing the satisfied tab: %q", result.Text)
	}
	for _, tab := range result.Wait.Tabs {
		if tab.TabID == "t1" && tab.Matched {
			t.Fatalf("t1 counted satisfied from a stale read: %+v", result.Wait.Tabs)
		}
	}
}

// Without the capability the wait still succeeds but marks the degradation
// explicitly instead of claiming a consistent group version.
func TestQuietSerialFallbackMarkedWhenCapabilityAbsent(t *testing.T) {
	t1 := fakeTab("t1", "s1", "$ ", 0)
	t2 := fakeTab("t2", "s1", "$ ", 0)
	t1.screen.IdleMS = 100
	t2.screen.IdleMS = 100
	fake := newTabFake(t1, t2)
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"quiet_ms":50,"mode":"all","timeout_ms":200}`), nil)

	if !result.OK || result.Wait == nil || !result.Wait.Satisfied {
		t.Fatalf("serial fallback wait = %+v", result)
	}
	if !strings.Contains(result.Notes, "best-effort serial snapshot") {
		t.Fatalf("serial fallback not marked: %+v", result)
	}
}
