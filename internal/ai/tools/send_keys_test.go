package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

func TestR1ExecutionRejectsBufferedDangerousSubmission(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ rm -rf /tmp/victim"}}
	registry := NewRegistry(Dependencies{Terminal: terminal})
	execution := &Execution{
		Registry:   registry,
		Scope:      Scope{SessionID: "session", TabID: "tab"},
		Permission: guard.Config{Mode: guard.ReadOnly},
		Memory:     guard.NewMemory(),
	}
	if got, _ := TerminalInputCursor(terminal.screen); got != "rm -rf /tmp/victim" {
		t.Fatalf("terminal input buffer = %q", got)
	}
	result, err := execution.initial(context.Background(), Call{ID: "keys", Name: "send_keys", Args: json.RawMessage(`{"keys":"; ls","enter":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Fatalf("dangerous buffered submission succeeded: %+v", result)
	}
	if len(terminal.writes) != 0 {
		t.Fatalf("dangerous buffered submission wrote %q", terminal.writes)
	}
}

func TestR1ExecutionRejectsCommandSplitAcrossSafeCalls(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ "}}
	execution := &Execution{
		Registry:   NewRegistry(Dependencies{Terminal: terminal}),
		Scope:      Scope{SessionID: "session", TabID: "tab"},
		Permission: guard.Config{Mode: guard.ReadWrite},
		Memory:     guard.NewMemory(),
	}
	result, err := execution.initial(context.Background(), Call{ID: "first", Name: "send_keys", Args: json.RawMessage(`{"keys":"touch /tmp/x; "}`)})
	if err != nil || !result.OK || len(terminal.writes) != 1 {
		t.Fatalf("first safe key write failed: result=%+v err=%v writes=%q", result, err, terminal.writes)
	}
	terminal.screen.Text = "$ touch /tmp/x; "
	execution.Permission = guard.Config{Mode: guard.ReadOnly}
	result, err = execution.initial(context.Background(), Call{ID: "second", Name: "send_keys", Args: json.RawMessage(`{"keys":"ls<enter>"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || len(terminal.writes) != 1 {
		t.Fatalf("split command was submitted: result=%+v writes=%q", result, terminal.writes)
	}
}

func TestR1ShortControlAliasesDoNotPanic(t *testing.T) {
	encoded, err := EncodeKeys("ls<c-m>", false)
	if err != nil || string(encoded) != "ls\r" {
		t.Fatalf("c-m encoding = %q, %v", encoded, err)
	}
	encoded, err = EncodeKeys("<c-c>", false)
	if err != nil || len(encoded) != 1 || encoded[0] != 3 {
		t.Fatalf("c-c encoding = %v, %v", encoded, err)
	}
}
