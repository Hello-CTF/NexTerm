package tools

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// R8: a persistent group-read failure or invalid roster must not leave stale
// satisfied metadata behind — the affected tabs are revoked with a visible
// error, and a later valid snapshot may re-evaluate success.

// badRosterTerminal answers group snapshots with a broken roster or an error
// while its per-tab snapshots stay healthy, so the serial scan satisfies and
// only the group verification fails.
type badRosterTerminal struct {
	*tabFakeTerminal
	mu           sync.Mutex
	mode         string
	snapshotFunc func(context.Context, []string) ([]TabQuietState, error)
}

func (f *badRosterTerminal) QuietGroupSnapshot(ctx context.Context, tabIDs []string) ([]TabQuietState, error) {
	f.mu.Lock()
	fn := f.snapshotFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, tabIDs)
	}
	return f.modeSnapshot(ctx, tabIDs)
}

func (f *badRosterTerminal) modeSnapshot(_ context.Context, tabIDs []string) ([]TabQuietState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch f.mode {
	case "error":
		return nil, errors.New("group snapshot backend unavailable")
	case "missing":
		return []TabQuietState{{TabID: "t1", IdleMS: 100, Version: 1}}, nil
	case "duplicate":
		return []TabQuietState{
			{TabID: "t1", IdleMS: 100, Version: 1},
			{TabID: "t1", IdleMS: 100, Version: 1},
			{TabID: "t2", IdleMS: 100, Version: 1},
		}, nil
	case "extra":
		return []TabQuietState{
			{TabID: "t1", IdleMS: 100, Version: 1},
			{TabID: "t2", IdleMS: 100, Version: 1},
			{TabID: "t3", IdleMS: 100, Version: 1},
		}, nil
	case "valid":
		states := make([]TabQuietState, 0, len(tabIDs))
		for _, tabID := range tabIDs {
			f.tabFakeTerminal.mu.Lock()
			tab := f.tabFakeTerminal.tabs[tabID]
			f.tabFakeTerminal.mu.Unlock()
			states = append(states, TabQuietState{TabID: tabID, IdleMS: tab.screen.IdleMS, Version: 1})
		}
		return states, nil
	default:
		return nil, errors.New("unknown mode")
	}
}

func TestQuietGroupInvalidRosterVisibility(t *testing.T) {
	for _, mode := range []string{"missing", "duplicate", "extra", "error"} {
		for _, waitMode := range []string{"any", "all"} {
			t.Run(mode+"/"+waitMode, func(t *testing.T) {
				t1 := fakeTab("t1", "s1", "$ ", 0)
				t2 := fakeTab("t2", "s1", "$ ", 0)
				t1.screen.IdleMS = 100
				t2.screen.IdleMS = 100
				fake := &badRosterTerminal{tabFakeTerminal: newTabFake(t1, t2), mode: mode}
				registry := p1Registry(fake)
				result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
					p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"quiet_ms":50,"mode":"`+waitMode+`","timeout_ms":120}`), nil)

				if result.OK || result.Wait == nil || !result.Wait.TimedOut {
					t.Fatalf("broken group read produced a success: %+v", result)
				}
				for _, tab := range result.Wait.Tabs {
					if tab.Matched {
						t.Fatalf("%s/%s: tab %s kept a stale satisfied verdict: %+v", mode, waitMode, tab.TabID, tab)
					}
					if tab.IdleMS >= 0 {
						t.Fatalf("%s/%s: tab %s kept stale idle %dms: %+v", mode, waitMode, tab.TabID, tab.IdleMS, tab)
					}
					if tab.Note == "" {
						t.Fatalf("%s/%s: tab %s has no visible failure note: %+v", mode, waitMode, tab.TabID, tab)
					}
				}
				if !strings.Contains(result.Text, "组快照") {
					t.Fatalf("%s/%s: group failure not visible in timeout text: %q", mode, waitMode, result.Text)
				}
			})
		}
	}
}

// A later valid group snapshot re-evaluates success after earlier failures:
// the revocation never latches permanently.
func TestQuietGroupFailureThenRecoverySucceeds(t *testing.T) {
	t1 := fakeTab("t1", "s1", "$ ", 0)
	t2 := fakeTab("t2", "s1", "$ ", 0)
	t1.screen.IdleMS = 100
	t2.screen.IdleMS = 100
	fake := &badRosterTerminal{tabFakeTerminal: newTabFake(t1, t2), mode: "error"}

	// The group read fails twice, then recovers with a valid exact roster.
	var mu sync.Mutex
	reads := 0
	fake.snapshotFunc = func(ctx context.Context, tabIDs []string) ([]TabQuietState, error) {
		mu.Lock()
		reads++
		recovered := reads > 2
		mu.Unlock()
		if recovered {
			fake.mu.Lock()
			fake.mode = "valid"
			fake.mu.Unlock()
		}
		return fake.modeSnapshot(ctx, tabIDs)
	}
	registry := p1Registry(fake)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("w", "wait_for", `{"tab_ids":["t/t1/s1","t/t2/s1"],"quiet_ms":50,"mode":"all","timeout_ms":400}`), nil)

	if !result.OK || result.Wait == nil || !result.Wait.Satisfied {
		t.Fatalf("recovery after group failures did not re-evaluate success: %+v", result)
	}
	for _, tab := range result.Wait.Tabs {
		if !tab.Matched || tab.Note != "" {
			t.Fatalf("recovered tab carries a stale failure note: %+v", tab)
		}
	}
}
