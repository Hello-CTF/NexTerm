package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// Deep cursors are bound to the exact tab and session they were issued for,
// and a cursor read that cannot confirm the terminal geometry fails closed
// with a since_seq recovery path instead of rendering at defaults.

func issueTestCursor(t *testing.T, registry *Registry, scope Scope, tabID string) string {
	t.Helper()
	result := registry.Execute(context.Background(), "job", scope,
		p1Call("r1", "read_screen", fmt.Sprintf(`{"tab_id":%q,"since_seq":1,"max_bytes":4}`, tabID)), nil)
	if !result.OK {
		t.Fatalf("cursor issuance = %+v", result)
	}
	match := regexp.MustCompile(`cursor=([A-Za-z0-9_-]+)`).FindStringSubmatch(result.Text)
	if match == nil {
		t.Fatalf("cursor missing: %q", result.Text)
	}
	return match[1]
}

func cursorRingTab(tabID, sessionID string) *tabFakeTab {
	tab := fakeTab(tabID, sessionID, "$ ", 11)
	tab.ring = newFakeRing(0, "hello world")
	return tab
}

func TestReadScreenCursorRejectsOtherTab(t *testing.T) {
	fake := newTabFake(cursorRingTab("t1", "s1"), cursorRingTab("t2", "s1"))
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	cursor := issueTestCursor(t, registry, scope, "t1")

	// Same session, same geometry, different tab: the cursor must not read
	// the other terminal's stream.
	result := registry.Execute(context.Background(), "job", scope,
		p1Call("r2", "read_screen", fmt.Sprintf(`{"tab_id":"t/t2/s1","cursor":%q}`, cursor)), nil)
	if result.OK || !strings.Contains(result.Text, "游标属于终端") {
		t.Fatalf("cross-tab cursor reuse = %+v", result)
	}
}

func TestReadScreenCursorRejectsReplacedSession(t *testing.T) {
	fake := newTabFake(cursorRingTab("t1", "s1"))
	registry := p1Registry(fake)
	cursor := issueTestCursor(t, registry, Scope{SessionID: "s1", TabID: "t1"}, "t1")

	// The tab ID now belongs to a new session; a caller scoped to the new
	// session resolves the tab fine, but the cursor was issued for the old
	// session identity and must not read.
	fake.mu.Lock()
	fake.tabs["t1"].sessionID = "s2"
	fake.tabs["t1"].detail.SessionID = "s2"
	fake.mu.Unlock()

	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s2", TabID: "t1"},
		p1Call("r2", "read_screen", fmt.Sprintf(`{"cursor":%q}`, cursor)), nil)
	if result.OK || !strings.Contains(result.Text, "since_seq=5") {
		t.Fatalf("replaced-session cursor = %+v", result)
	}
}

func TestReadScreenCursorFailsClosedOnSnapshotError(t *testing.T) {
	fake := newTabFake(cursorRingTab("t1", "s1"))
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	cursor := issueTestCursor(t, registry, scope, "t1")

	fake.snapshotErr = errors.New("快照不可用")
	result := registry.Execute(context.Background(), "job", scope,
		p1Call("r2", "read_screen", fmt.Sprintf(`{"cursor":%q}`, cursor)), nil)
	if result.OK || !strings.Contains(result.Text, "since_seq=5") || !strings.Contains(result.Text, "快照失败") {
		t.Fatalf("snapshot-failure cursor read = %+v", result)
	}
}
