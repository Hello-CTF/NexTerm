package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

// R7-F2: the on→off mode flip between the pre-check snapshot and the write
// must be caught inside the write lock with the mode used for actual
// encoding — a pre-check snapshot alone cannot close the window.

// literalPasteTerminal implements the write-lock guarded literal contract:
// the mode is read at write time inside the lock, and LF is rejected when
// the mode is off. CommandState flips the mode off after the pre-check.
type literalPasteTerminal struct {
	fakeTerminal
	bracketed bool
}

func (f *literalPasteTerminal) WritePasteLiteral(_ context.Context, _ string, text string) error {
	if !f.bracketed && strings.Contains(text, "\n") {
		return errors.New("bracketed-paste 模式已关闭，paste 中的换行会被编码为回车并提前提交")
	}
	return f.Write(context.Background(), "t", terminal.EncodePaste(text, f.bracketed))
}

func (f *literalPasteTerminal) CommandState(ctx context.Context, tabID string) (CommandState, error) {
	f.bracketed = false
	return f.fakeTerminal.CommandState(ctx, tabID)
}

func TestPasteOnToOffFlipRejectedInsideWriteLock(t *testing.T) {
	terminal := &literalPasteTerminal{fakeTerminal: fakeTerminal{screen: Screen{Text: "$ ", Cols: 80, Rows: 24, BracketedPaste: true}}, bracketed: true}
	registry := p1Registry(terminal)
	scope := Scope{SessionID: "s", TabID: "t"}

	result := registry.Execute(context.Background(), "job", scope,
		p1Call("k", "send_keys", `{"keys":"echo one\necho two","paste":true}`), nil)
	if result.OK || !strings.Contains(result.Text, "回车") {
		t.Fatalf("on→off flip = %+v", result)
	}
	for _, write := range terminal.writes {
		if strings.Contains(string(write), "\r") {
			t.Fatalf("on→off flip produced a carriage return: %q", write)
		}
	}
}

// Without the write-lock guarded capability the window is visible instead of
// silently closed: the write lands, and the result marks the uncovered
// on→off window explicitly.
func TestPasteOnToOffFlipWithoutGuardIsMarked(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ", Cols: 80, Rows: 24, BracketedPaste: true}}
	terminal.commandState = CommandState{}
	registry := p1Registry(terminal)
	scope := Scope{SessionID: "s", TabID: "t"}

	// Flip the mode off after the pre-check snapshot via the command-state
	// read that precedes the write.
	terminal.onCommandState = func() { terminal.screen.BracketedPaste = false }
	result := registry.Execute(context.Background(), "job", scope,
		p1Call("k", "send_keys", `{"keys":"echo one\necho two","paste":true}`), nil)
	if !result.OK {
		t.Fatalf("unguarded flip = %+v", result)
	}
	if !strings.Contains(result.Notes, "on→off") && !strings.Contains(result.Text, "on→off") {
		t.Fatalf("unguarded on→off window not marked: %+v", result)
	}
}
