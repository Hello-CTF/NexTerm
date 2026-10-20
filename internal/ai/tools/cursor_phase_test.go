package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/session"
)

// Two-phase cursor contract: issuance never emits an unbound cursor when the
// identity resolver fails, consumption uses the one validated geometry, and
// identity errors propagate distinctly from "replaced" and "gone".

// flakyTabSessionTerminal delegates TabSession and fails once the call count
// passes failAfter, so tests place identity errors at exact call sites
// (resolveTab first, cursor binding second).
type flakyTabSessionTerminal struct {
	*tabFakeTerminal
	mu        sync.Mutex
	calls     int
	failAfter int
	err       error
}

func (f *flakyTabSessionTerminal) TabSession(ctx context.Context, tabID string) (string, error) {
	f.mu.Lock()
	f.calls++
	fail := f.failAfter >= 0 && f.calls > f.failAfter
	err := f.err
	f.mu.Unlock()
	if fail && err != nil {
		return "", err
	}
	return f.tabFakeTerminal.TabSession(ctx, tabID)
}

func (f *flakyTabSessionTerminal) setFailFromNowOn(err error) {
	f.mu.Lock()
	f.failAfter = f.calls
	f.err = err
	f.mu.Unlock()
}

func (f *flakyTabSessionTerminal) setFailAfter(after int, err error) {
	f.mu.Lock()
	f.failAfter = after
	f.err = err
	f.mu.Unlock()
}

func TestReadScreenCursorUsesValidatedGeometry(t *testing.T) {
	tab := cursorRingTab("t1", "s1")
	tab.screen.Cols = 100
	tab.screen.Rows = 30
	longLine := strings.Repeat("x", 90)
	tab.ring = newFakeRing(0, longLine+"\r\n")
	fake := newTabFake(tab)
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	cursor := issueTestCursor(t, registry, scope, "t1")

	// Snapshots fail after the validation read: the cursor read must render
	// with the geometry validated at check time, never an 80x24 default.
	// Issuance and validation each take one snapshot; any re-snapshot inside
	// the paged read would hit the error and silently fall back to 80x24.
	fake.snapshotErr = errors.New("快照不可用")
	fake.snapshotErrAfter = 2
	result := registry.Execute(context.Background(), "job", scope,
		p1Call("r2", "read_screen", fmt.Sprintf(`{"cursor":%q,"max_bytes":4096}`, cursor)), nil)
	if !result.OK {
		t.Fatalf("validated-geometry read failed: %+v", result)
	}
	// Page two carries 85 x's: contiguous at 100 cols, split 80+5 at 80x24.
	if !strings.Contains(result.Text, strings.Repeat("x", 85)) {
		t.Fatalf("read did not use the validated 100-col geometry: %q", result.Text)
	}
}

func TestReadScreenCursorConsumptionGeometryChangeFails(t *testing.T) {
	tab := cursorRingTab("t1", "s1")
	tab.screen.Cols = 100
	tab.screen.Rows = 30
	fake := newTabFake(tab)
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	cursor := issueTestCursor(t, registry, scope, "t1")

	// The tab silently shrank to 80x24: the cursor must fail with the
	// recovery anchor, not render at the new size.
	fake.mu.Lock()
	fake.tabs["t1"].screen.Cols = 80
	fake.tabs["t1"].screen.Rows = 24
	fake.mu.Unlock()
	result := registry.Execute(context.Background(), "job", scope,
		p1Call("r2", "read_screen", fmt.Sprintf(`{"cursor":%q}`, cursor)), nil)
	if result.OK || !strings.Contains(result.Text, "resize") || !strings.Contains(result.Text, "since_seq=5") {
		t.Fatalf("geometry-change cursor read = %+v", result)
	}
}

func TestReadScreenCursorNotIssuedOnIdentityError(t *testing.T) {
	tab := cursorRingTab("t1", "s1")
	fake := &flakyTabSessionTerminal{tabFakeTerminal: newTabFake(tab), failAfter: -1}
	transient := errors.New("identity backend unavailable")
	// resolveTab (first call) succeeds; the cursor-binding call fails.
	fake.setFailAfter(1, transient)
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}

	result := registry.Execute(context.Background(), "job", scope,
		p1Call("r1", "read_screen", `{"since_seq":1,"max_bytes":4}`), nil)
	if !result.OK {
		t.Fatalf("issuance read = %+v", result)
	}
	if regexp.MustCompile(`cursor=[A-Za-z0-9_-]+`).MatchString(result.Text) {
		t.Fatalf("unbound cursor issued despite identity error: %q", result.Text)
	}
	if !strings.Contains(result.Text, "身份解析失败") {
		t.Fatalf("issuance read missing identity note: %q", result.Text)
	}

	// A hand-crafted unbound cursor is rejected while the resolver exists.
	unbound := encodeScreenCursor(screenCursor{Version: 1, Kind: "raw", Seq: 5, TabID: "t1", Cols: 80, Rows: 24})
	fake.setFailAfter(-1, nil)
	result = registry.Execute(context.Background(), "job", scope,
		p1Call("r2", "read_screen", fmt.Sprintf(`{"cursor":%q}`, unbound)), nil)
	if result.OK || !strings.Contains(result.Text, "缺少会话绑定") || !strings.Contains(result.Text, "since_seq=5") {
		t.Fatalf("unbound cursor accepted: %+v", result)
	}
}

func TestReadScreenCursorIdentityErrorPropagates(t *testing.T) {
	tab := cursorRingTab("t1", "s1")
	fake := &flakyTabSessionTerminal{tabFakeTerminal: newTabFake(tab), failAfter: -1}
	registry := p1Registry(fake)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	cursor := issueTestCursor(t, registry, scope, "t1")

	// A transient identity failure at read time propagates as itself —
	// neither "replaced" nor "gone".
	transient := errors.New("identity backend unavailable")
	fake.setFailFromNowOn(transient)
	result := registry.Execute(context.Background(), "job", scope,
		p1Call("r2", "read_screen", fmt.Sprintf(`{"cursor":%q}`, cursor)), nil)
	if result.OK || !strings.Contains(result.Text, transient.Error()) {
		t.Fatalf("transient identity error = %+v", result)
	}
	if strings.Contains(result.Text, "已变化") || strings.Contains(result.Text, ErrTerminalGone.Error()) {
		t.Fatalf("transient identity error misclassified: %q", result.Text)
	}

	// An explicit not-found at read time maps to gone, distinct from replaced.
	fake.setFailFromNowOn(session.ErrTabNotFound)
	result = registry.Execute(context.Background(), "job", scope,
		p1Call("r3", "read_screen", fmt.Sprintf(`{"cursor":%q}`, cursor)), nil)
	if result.OK || !strings.Contains(result.Text, ErrTerminalGone.Error()) {
		t.Fatalf("not-found identity error = %+v", result)
	}
}
