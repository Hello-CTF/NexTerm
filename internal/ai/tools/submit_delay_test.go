package tools

import (
	"context"
	"strings"
	"testing"
)

// R10-F2: a non-zero submit_delay_ms is only meaningful for a paste that is
// actually submitted — paste=true with enter=false must be rejected instead
// of silently ignoring the delay.

func TestPasteSubmitDelayRequiresEnter(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", Cols: 80, Rows: 24}}
	registry := p1Registry(terminal)
	scope := Scope{SessionID: "s", TabID: "t"}

	result := registry.Execute(context.Background(), "job", scope,
		p1Call("k", "send_keys", `{"keys":"ls","paste":true,"enter":false,"submit_delay_ms":50}`), nil)
	if result.OK || !strings.Contains(result.Text, "submit_delay_ms") {
		t.Fatalf("submit_delay_ms with enter=false = %+v", result)
	}
	if len(terminal.writes) != 0 {
		t.Fatalf("rejected delay call wrote bytes: %q", terminal.writes)
	}
	if result.OperationID != "" {
		t.Fatalf("rejected call reported an operation ID: %+v", result)
	}

	// paste=false stays rejected as before.
	result = registry.Execute(context.Background(), "job", scope,
		p1Call("k2", "send_keys", `{"keys":"ls","submit_delay_ms":50}`), nil)
	if result.OK || !strings.Contains(result.Text, "submit_delay_ms") {
		t.Fatalf("submit_delay_ms without paste = %+v", result)
	}
	if len(terminal.writes) != 0 {
		t.Fatalf("rejected delay call wrote bytes: %q", terminal.writes)
	}
}
