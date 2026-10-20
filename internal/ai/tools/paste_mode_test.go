package tools

import (
	"context"
	"strings"
	"testing"
)

// R6-F2: with bracketed-paste mode off, terminal.EncodePaste maps LF to CR,
// so a paste containing newlines submits even with enter=false. The guard
// must match the real terminal mode: unbracketed newlines are rejected,
// bracketed literal paste is preserved, and allow_control keeps its boundary.

func TestUnbracketedPasteNewlineCannotSubmitWithoutEnter(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", Cols: 80, Rows: 24, BracketedPaste: false}}
	registry := p1Registry(terminal)
	scope := Scope{SessionID: "s", TabID: "t"}

	result := registry.Execute(context.Background(), "job", scope,
		p1Call("k", "send_keys", `{"keys":"echo one\necho two","paste":true}`), nil)
	if result.OK || !strings.Contains(result.Text, "bracketed-paste") {
		t.Fatalf("unbracketed newline paste = %+v", result)
	}
	for _, write := range terminal.writes {
		if strings.Contains(string(write), "\r") {
			t.Fatalf("paste produced a carriage return without enter: %q", write)
		}
	}

	// enter=true does not legitimize embedded newlines either: they would
	// submit before the intended final Enter.
	result = registry.Execute(context.Background(), "job", scope,
		p1Call("k2", "send_keys", `{"keys":"echo one\necho two","paste":true,"enter":true}`), nil)
	if result.OK || !strings.Contains(result.Text, "bracketed-paste") {
		t.Fatalf("unbracketed newline paste with enter = %+v", result)
	}
}

func TestBracketedPasteNewlineStaysLiteral(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", Cols: 80, Rows: 24, BracketedPaste: true}}
	registry := p1Registry(terminal)
	scope := Scope{SessionID: "s", TabID: "t"}

	result := registry.Execute(context.Background(), "job", scope,
		p1Call("k", "send_keys", `{"keys":"echo one\necho two","paste":true}`), nil)
	if !result.OK {
		t.Fatalf("bracketed newline paste = %+v", result)
	}
	if len(terminal.writes) != 1 {
		t.Fatalf("writes = %q", terminal.writes)
	}
	encoded := string(terminal.writes[0])
	if !strings.Contains(encoded, "echo one\necho two") {
		t.Fatalf("bracketed paste did not preserve the literal newline: %q", encoded)
	}
	if strings.Contains(encoded, "echo one\r") {
		t.Fatalf("bracketed paste mapped LF to CR: %q", encoded)
	}
	if !strings.HasPrefix(encoded, "\x1b[200~") || !strings.HasSuffix(encoded, "\x1b[201~") {
		t.Fatalf("bracketed paste markers missing: %q", encoded)
	}
}
