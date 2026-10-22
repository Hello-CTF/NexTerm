package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// R4-F1: an empty legacy identity is a verified absence — the unified
// resolve path classifies it as Gone like the handle contract does, never as
// cross-scope.

func TestLegacyResolverCursorEmptyIdentityIsGone(t *testing.T) {
	hook := &legacyTabSessionHook{values: []string{"s1", "s1", ""}}
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
	// the resolver now returns an empty identity: the tab is gone.
	result := registry.Execute(context.Background(), "job", scope,
		p1Call("r2", "read_screen", fmt.Sprintf(`{"cursor":%q}`, match[1])), nil)
	if result.OK || !strings.Contains(result.Text, ErrTerminalGone.Error()) {
		t.Fatalf("empty legacy identity = %+v, want Gone", result)
	}
	if strings.Contains(result.Text, ErrCrossScope.Error()) {
		t.Fatalf("empty legacy identity misclassified as cross-scope: %q", result.Text)
	}
}

func TestLegacyResolverEmptyIdentityDirectToolIsGone(t *testing.T) {
	hook := &legacyTabSessionHook{values: []string{""}}
	registry := legacyCursorRegistry(hook, newFakeRing(0, "hello world"))
	scope := Scope{SessionID: "s1", TabID: "tab"}
	result := registry.Execute(context.Background(), "job", scope,
		p1Call("r", "read_screen", `{}`), nil)
	if result.OK || !strings.Contains(result.Text, ErrTerminalGone.Error()) {
		t.Fatalf("plain read with empty legacy identity = %+v, want Gone", result)
	}
	if strings.Contains(result.Text, ErrCrossScope.Error()) {
		t.Fatalf("plain read empty identity misclassified as cross-scope: %q", result.Text)
	}
}

// R4-F3: the Create capability is checked before any connection is
// initiated, so a missing capability never burns a connector call or leaves
// an unreported residual session.
func TestTerminalCreateCapabilityCheckedBeforeConnect(t *testing.T) {
	connector := &residualConnector{sessionID: "s-residual"}
	registry := p1Registry(legacyTerminal{})
	registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) {
		return "", fmt.Errorf("资产未连接: %w", ErrNoLiveSession)
	}
	registry.deps.AssetConnector = connector
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`), nil)
	if result.OK || !strings.Contains(result.Text, "终端服务未配置") {
		t.Fatalf("capability check = %+v", result)
	}
	if connector.calls != 0 {
		t.Fatalf("connect initiated without Create capability: %d calls", connector.calls)
	}
	if result.OperationID != "" {
		t.Fatalf("no mutation was initiated, yet operation ID present: %+v", result)
	}
	if strings.Contains(result.Text, "s-residual") {
		t.Fatalf("residual session reported without a connect: %q", result.Text)
	}
}
