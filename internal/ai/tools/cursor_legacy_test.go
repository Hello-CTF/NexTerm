package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// The legacy Dependencies.TabSession resolver counts as an identity resolver:
// it never yields an unbound cursor at issuance, and its unbound cursors are
// rejected at consumption.

// legacyTabSessionHook returns per-call identities, so tests flip the legacy
// resolver between issuance and consumption.
type legacyTabSessionHook struct {
	mu     sync.Mutex
	calls  int
	values []string
}

func (h *legacyTabSessionHook) hook(tabID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	index := h.calls
	h.calls++
	if index >= len(h.values) {
		return h.values[len(h.values)-1]
	}
	return h.values[index]
}

func legacyCursorRegistry(hook *legacyTabSessionHook, ring *fakeRing) *Registry {
	terminal := anchoredTerminal(ring)
	return NewRegistry(Dependencies{Terminal: terminal, TabSession: hook.hook, PollInterval: 0})
}

func TestLegacyResolverCursorNotIssuedWhenIdentityEmpty(t *testing.T) {
	hook := &legacyTabSessionHook{values: []string{"s1", ""}}
	registry := legacyCursorRegistry(hook, newFakeRing(0, "hello world"))
	scope := Scope{SessionID: "s1", TabID: "tab"}

	result := registry.Execute(context.Background(), "job", scope,
		p1Call("r", "read_screen", `{"since_seq":1,"max_bytes":4}`), nil)
	if !result.OK {
		t.Fatalf("issuance read = %+v", result)
	}
	if regexp.MustCompile(`cursor=[A-Za-z0-9_-]+`).MatchString(result.Text) {
		t.Fatalf("unbound cursor issued from empty legacy identity: %q", result.Text)
	}
	if !strings.Contains(result.Text, "身份解析失败") || !strings.Contains(result.Text, "since_seq=5") {
		t.Fatalf("issuance read missing visible degradation: %q", result.Text)
	}
}

func TestLegacyResolverCursorRejectsUnboundCursor(t *testing.T) {
	hook := &legacyTabSessionHook{values: []string{"s1"}}
	registry := legacyCursorRegistry(hook, newFakeRing(0, "hello world"))
	scope := Scope{SessionID: "s1", TabID: "tab"}

	unbound := encodeScreenCursor(screenCursor{Version: 1, Kind: "raw", Seq: 5, TabID: "tab", Cols: 80, Rows: 24})
	result := registry.Execute(context.Background(), "job", scope,
		p1Call("r", "read_screen", fmt.Sprintf(`{"cursor":%q}`, unbound)), nil)
	if result.OK || !strings.Contains(result.Text, "缺少会话绑定") || !strings.Contains(result.Text, "since_seq=5") {
		t.Fatalf("unbound cursor accepted with legacy resolver: %+v", result)
	}
}

func TestLegacyResolverCursorSessionReplacement(t *testing.T) {
	hook := &legacyTabSessionHook{values: []string{"s1", "s1", "s2"}}
	registry := legacyCursorRegistry(hook, newFakeRing(0, "hello world"))
	scope := Scope{SessionID: "s1", TabID: "tab"}

	issued := registry.Execute(context.Background(), "job", scope,
		p1Call("r1", "read_screen", `{"since_seq":1,"max_bytes":4}`), nil)
	if !issued.OK {
		t.Fatalf("issuance read = %+v", issued)
	}
	match := regexp.MustCompile(`cursor=([A-Za-z0-9_-]+)`).FindStringSubmatch(issued.Text)
	if match == nil {
		t.Fatalf("cursor missing: %q", issued.Text)
	}
	// resolveTab consumed "s1", issuance binding consumed the second "s1";
	// the next resolver call returns "s2": the cursor's session was replaced.
	// The caller now scopes the new session, so the tab resolves fine and the
	// cursor's own binding check must reject it.
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s2", TabID: "tab"},
		p1Call("r2", "read_screen", fmt.Sprintf(`{"cursor":%q}`, match[1])), nil)
	if result.OK || !strings.Contains(result.Text, "已变化") || !strings.Contains(result.Text, "since_seq=5") {
		t.Fatalf("legacy session replacement = %+v", result)
	}
	if strings.Contains(result.Text, ErrTerminalGone.Error()) {
		t.Fatalf("replacement misclassified as gone: %q", result.Text)
	}
}
