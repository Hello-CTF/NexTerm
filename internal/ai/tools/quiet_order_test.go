package tools

import (
	"context"
	"sync"
	"testing"
)

// R10-F1: the group-snapshot contract guarantees each requested tab exactly
// once, not response order — a stable roster with alternating order and
// identical versions must not be misjudged as perpetual change.

// orderFlipTerminal returns the same two states in alternating order.
type orderFlipTerminal struct {
	*tabFakeTerminal
	mu    sync.Mutex
	reads int
}

func (f *orderFlipTerminal) QuietGroupSnapshot(_ context.Context, tabIDs []string) ([]TabQuietState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	states := []TabQuietState{
		{TabID: "t1", IdleMS: 100, Version: 1},
		{TabID: "t2", IdleMS: 100, Version: 1},
	}
	if f.reads%2 == 0 {
		states[0], states[1] = states[1], states[0]
	}
	return states, nil
}

func TestStableGroupSnapshotOrderDoesNotAffectSuccess(t *testing.T) {
	for _, mode := range []string{"any", "all"} {
		t.Run(mode, func(t *testing.T) {
			t1 := fakeTab("t1", "s1", "$ ", 0)
			t2 := fakeTab("t2", "s1", "$ ", 0)
			t1.screen.IdleMS = 100
			t2.screen.IdleMS = 100
			fake := &orderFlipTerminal{tabFakeTerminal: newTabFake(t1, t2)}
			registry := p1Registry(fake)
			result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
				p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"quiet_ms":50,"mode":"`+mode+`","timeout_ms":200}`), nil)
			if !result.OK || result.Wait == nil || !result.Wait.Satisfied {
				t.Fatalf("alternating-order stable snapshot misjudged as perpetual change: %+v", result)
			}
			for _, tab := range result.Wait.Tabs {
				if !tab.Matched || tab.Note != "" {
					t.Fatalf("satisfied tab carries a failure note: %+v", tab)
				}
			}
		})
	}
}
