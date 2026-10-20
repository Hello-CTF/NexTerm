package tools

import (
	"context"
	"sync"
	"testing"
	"time"
)

// all+quiet must not latch a stale satisfied verdict: pattern/lines latch,
// but quiet is re-evaluated for every tab each round, so a previously
// satisfied tab that becomes busy or whose snapshot fails is revoked.

func TestWaitForTabsAllQuietRechecksEarlierSatisfiedTab(t *testing.T) {
	t1 := fakeTab("t1", "s1", "$ ", 0)
	t2 := fakeTab("t2", "s1", "$ ", 0)
	t1.screen.IdleMS = 100
	t2.screen.IdleMS = 5
	fake := newTabFake(t1, t2)

	// After t1's first satisfied round, t1 becomes busy and t2 becomes
	// quiet: a latched verdict would report all satisfied with t1's stale
	// idle, the re-evaluated one must revoke t1. Round one completes within
	// a few polls; the flip fires well before the timeout budget.
	flipped := make(chan struct{})
	var flipOnce sync.Once
	go func() {
		time.Sleep(30 * time.Millisecond)
		flipOnce.Do(func() {
			fake.mu.Lock()
			t1.screen.IdleMS = 0
			t2.screen.IdleMS = 100
			fake.mu.Unlock()
			t1.ring.append("busy\r\n")
			close(flipped)
		})
	}()
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"quiet_ms":50,"mode":"all","timeout_ms":200}`), nil)
	<-flipped

	if result.OK || result.Wait == nil || !result.Wait.TimedOut {
		t.Fatalf("stale satisfied faked an all-quiet success: %+v", result)
	}
	byID := map[string]bool{}
	for _, tab := range result.Wait.Tabs {
		byID[tab.TabID] = tab.Matched
	}
	if byID["t1"] {
		t.Fatalf("busy t1 reported satisfied from a latched verdict: %+v", result.Wait.Tabs)
	}
	if !byID["t2"] {
		t.Fatalf("quiet t2 not satisfied: %+v", result.Wait.Tabs)
	}
}
