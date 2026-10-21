package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// quiet_ms is part of every tab's fully-satisfied verdict: a tab whose
// condition matched but whose idle time has not reached quiet_ms is not
// satisfied, and a quiet snapshot failure must never reuse stale idle to
// fake success.

func TestWaitForTabsAnyQuietCountsOnlyFullySatisfied(t *testing.T) {
	t1 := fakeTab("t1", "s1", "$ ", 0)
	t2 := fakeTab("t2", "s1", "$ ", 0)
	t1.screen.IdleMS = 5
	t2.screen.IdleMS = 100
	fake := newTabFake(t1, t2)
	hook, anchored := waitAnchoredHook("t1", "t2")
	fake.outputSinceHook = hook
	registry := p1Registry(fake)
	resultCh := make(chan Output, 1)
	go func() {
		resultCh <- registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
			p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"pattern":"ready","mode":"any","quiet_ms":50,"timeout_ms":2000}`), nil)
	}()
	<-anchored["t1"]
	<-anchored["t2"]
	t1.ring.append("ready\r\n")
	t2.ring.append("ready\r\n")
	result := <-resultCh

	if !result.OK || result.Wait == nil || !result.Wait.Satisfied {
		t.Fatalf("any+quiet wait = %+v", result)
	}
	// Only t2 reached pattern+quiet; t1 matched the pattern but never got quiet.
	if !strings.Contains(result.Text, "t2") || strings.Contains(result.Text, "标签页 t1") {
		t.Fatalf("headline counted a not-quiet tab as satisfied: %q", result.Text)
	}
	byID := map[string]bool{}
	for _, tab := range result.Wait.Tabs {
		byID[tab.TabID] = tab.Matched
	}
	if byID["t1"] || !byID["t2"] {
		t.Fatalf("per-tab satisfied = %v", byID)
	}
}

func TestWaitForTabsQuietSnapshotFailureNotStaleSuccess(t *testing.T) {
	t1 := fakeTab("t1", "s1", "$ ", 0)
	t2 := fakeTab("t2", "s1", "$ ", 0)
	t1.screen.IdleMS = 100
	t2.screen.IdleMS = 100
	fake := newTabFake(t1, t2)
	registry := p1Registry(fake)
	// The anchor snapshots read idle=100; every later quiet snapshot fails.
	fake.snapshotErr = errors.New("安静快照不可用")
	fake.snapshotErrAfter = 2
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"quiet_ms":50,"mode":"all","timeout_ms":60}`), nil)

	if result.OK || result.Wait == nil || !result.Wait.TimedOut {
		t.Fatalf("stale idle faked success: %+v", result)
	}
	if !strings.Contains(result.Text, "安静快照") {
		t.Fatalf("snapshot failure not visible in timeout output: %q", result.Text)
	}
	for _, tab := range result.Wait.Tabs {
		if tab.Matched {
			t.Fatalf("tab %s reported satisfied from stale idle: %+v", tab.TabID, tab)
		}
	}
}
