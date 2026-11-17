package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

// rightAligned pads left so right ends at cell end (exclusive) of a row.
func rightAligned(left, right string, end int) string {
	gap := end - terminal.StringWidth(left) - terminal.StringWidth(right)
	return left + strings.Repeat(" ", gap) + right
}

func TestReadTerminalInputSeparatesPromptFromInput(t *testing.T) {
	tests := []struct {
		name     string
		screen   Screen
		text     string
		cursor   int
		boundary InputBoundary
	}{
		{
			name:     "two-line kali prompt with the trailing blank dropped",
			screen:   Screen{Text: "┌──(kali㉿kali)-[~]\n└─$", CursorRow: 1, CursorCol: 4, Cols: 80, Rows: 24},
			boundary: InputBoundaryPrompt,
		},
		{
			name:     "two-line kali prompt with an empty user and host",
			screen:   Screen{Text: "┌──()─[~]\n└─$", CursorRow: 1, CursorCol: 4, Cols: 80, Rows: 24},
			boundary: InputBoundaryPrompt,
		},
		{
			name:     "box-drawing root prompt with partial typed input",
			screen:   Screen{Text: "┌──(root㉿kali)-[/srv]\n└─# rm -rf", CursorRow: 1, CursorCol: 10, Cols: 80, Rows: 24},
			text:     "rm -rf",
			cursor:   6,
			boundary: InputBoundaryPrompt,
		},
		{
			name:     "rounded two-line prompt",
			screen:   Screen{Text: "╭─user at host in ~/src\n╰─❯ git status", CursorRow: 1, CursorCol: 14, Cols: 80, Rows: 24},
			text:     "git status",
			cursor:   10,
			boundary: InputBoundaryPrompt,
		},
		{
			name:     "starship single-line prompt",
			screen:   Screen{Text: "~/src on main ❯ ls", CursorRow: 0, CursorCol: 18, Cols: 80, Rows: 24},
			text:     "ls",
			cursor:   2,
			boundary: InputBoundaryPrompt,
		},
		{
			name:     "bash default prompt",
			screen:   Screen{Text: "bash-5.2$ uptime", CursorCol: 16, Cols: 80, Rows: 24},
			text:     "uptime",
			cursor:   6,
			boundary: InputBoundaryPrompt,
		},
		{
			name:     "bracketed prompt",
			screen:   Screen{Text: "[~]$ df -h", CursorCol: 10, Cols: 80, Rows: 24},
			text:     "df -h",
			cursor:   5,
			boundary: InputBoundaryPrompt,
		},
		{
			name:     "powershell prompt",
			screen:   Screen{Text: `PS C:\Users\me> Get-Process`, CursorCol: 27, Cols: 80, Rows: 24},
			text:     "Get-Process",
			cursor:   11,
			boundary: InputBoundaryPrompt,
		},
		{
			name:     "right prompt on the input row",
			screen:   Screen{Text: rightAligned("user@h:~$ ls -la", "[12:00:01]", 39), CursorCol: 16, Cols: 40, Rows: 24},
			text:     "ls -la",
			cursor:   6,
			boundary: InputBoundaryPrompt,
		},
		{
			name:     "right prompt after an empty input",
			screen:   Screen{Text: rightAligned("└─$", "✔ 0", 79), CursorCol: 4, Cols: 80, Rows: 24},
			boundary: InputBoundaryPrompt,
		},
		{
			name:     "shell mark wins over an unrecognized prompt",
			screen:   Screen{Text: "weird>>> brew outdated", CursorCol: 22, Cols: 80, Rows: 24, InputStart: terminal.ScreenMark{Row: 0, Col: 9, Set: true}},
			text:     "brew outdated",
			cursor:   13,
			boundary: InputBoundaryMarked,
		},
		{
			name:     "shell mark after a two-line prompt",
			screen:   Screen{Text: "┌──(u㉿k)-[~]\n└─$ echo hi", CursorRow: 1, CursorCol: 11, Cols: 80, Rows: 24, PromptStart: terminal.ScreenMark{Row: 0, Set: true}, InputStart: terminal.ScreenMark{Row: 1, Col: 4, Set: true}},
			text:     "echo hi",
			cursor:   7,
			boundary: InputBoundaryMarked,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ReadTerminalInput(test.screen)
			if got.Text != test.text || got.Cursor != test.cursor || got.Boundary != test.boundary {
				t.Fatalf("input = %+v, want text %q cursor %d boundary %d", got, test.text, test.cursor, test.boundary)
			}
		})
	}
}

func TestReadTerminalInputKeepsTypedTextThatLooksLikeARightPrompt(t *testing.T) {
	// Text after the cursor that carries shell syntax is input, however far
	// right it sits.
	screen := Screen{Text: rightAligned("user@h:~$ echo", "; rm -rf ~", 39), CursorCol: 14, Cols: 40, Rows: 24}
	got := ReadTerminalInput(screen)
	if !strings.Contains(got.Text, "rm -rf ~") {
		t.Fatalf("input = %+v", got)
	}
	if ruling := ClassifyTerminalKeys("<enter>", false, got, nil); ruling.Risk != guard.Forbidden {
		t.Fatalf("ruling = %+v", ruling)
	}
}

func TestReadTerminalInputNeverReachesAbovePromptStartMark(t *testing.T) {
	screen := Screen{Text: "$ rm -rf /tmp/x\noutput\nλ", CursorRow: 2, CursorCol: 2, Cols: 80, Rows: 24, PromptStart: terminal.ScreenMark{Row: 2, Set: true}}
	got := ReadTerminalInput(screen)
	if got.Boundary != InputBoundaryUnknown || strings.Contains(got.Text, "rm -rf") {
		t.Fatalf("input = %+v", got)
	}
}

func TestClassifyTerminalKeysUserScenario(t *testing.T) {
	kali := Screen{Text: "┌──(kali㉿kali)-[~]\n└─$", CursorRow: 1, CursorCol: 4, Cols: 80, Rows: 24}
	input := ReadTerminalInput(kali)
	ruling := ClassifyTerminalKeys("brew update && brew outdated", true, input, nil)
	if ruling.Risk != guard.NeedsConfirm || ruling.Kind != guard.KindPackage {
		t.Fatalf("brew update && brew outdated = %+v", ruling)
	}
	if ruling := ClassifyTerminalKeys("brew outdated", true, input, nil); ruling.Risk != guard.Safe {
		t.Fatalf("brew outdated = %+v", ruling)
	}
	call := Call{Name: "send_keys", Args: json.RawMessage(`{"keys":"brew update && brew outdated","enter":true}`)}
	pending, cursor := input.DisplayInput()
	if display := DisplaySendKeys(call, pending, cursor); display != "send_keys brew update && brew outdated <enter>" {
		t.Fatalf("display = %q", display)
	}
}

func TestClassifyTerminalKeysWithUnknownBoundary(t *testing.T) {
	unknown := ReadTerminalInput(Screen{Text: "last login\nλ", CursorRow: 1, CursorCol: 2, Cols: 80, Rows: 24})
	if unknown.Boundary != InputBoundaryUnknown {
		t.Fatalf("boundary = %+v", unknown)
	}
	ruling := ClassifyTerminalKeys("brew outdated", true, unknown, nil)
	if ruling.Risk != guard.Unknowable || ruling.Reason != UnknownInputBoundaryReason || ruling.Detail != "" {
		t.Fatalf("submission at an unrecognized prompt = %+v", ruling)
	}
	if ruling := ClassifyTerminalKeys("rm -rf /", true, unknown, nil); ruling.Risk != guard.Forbidden {
		t.Fatalf("dangerous keys at an unrecognized prompt = %+v", ruling)
	}
	if ruling := ClassifyTerminalKeys("brew outdated", false, unknown, nil); ruling.Risk != guard.Safe {
		t.Fatalf("typing without submitting = %+v", ruling)
	}
	if pending, _ := unknown.DisplayInput(); pending != "" {
		t.Fatalf("display input = %q", pending)
	}
	// Output above an empty cursor row is not pending input.
	empty := ReadTerminalInput(Screen{Text: "last login\n", CursorRow: 1, CursorCol: 0, Cols: 80, Rows: 24})
	if ruling := ClassifyTerminalKeys("brew outdated", true, empty, nil); ruling.Risk != guard.Safe {
		t.Fatalf("submission on an empty row = %+v", ruling)
	}
}

func TestSendKeysAtTwoLinePromptRunsWithoutConfirmation(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "┌──(kali㉿kali)-[~]\n└─$", CursorRow: 1, CursorCol: 4, Cols: 80, Rows: 24}}
	execution := &Execution{
		Registry:   NewRegistry(Dependencies{Terminal: terminal}),
		Scope:      Scope{SessionID: "session", TabID: "tab"},
		Permission: guard.Config{Mode: guard.ReadWrite},
		Memory:     guard.NewMemory(),
	}
	result, err := execution.initial(context.Background(), Call{ID: "keys", Name: "send_keys", Args: json.RawMessage(`{"keys":"brew outdated","enter":true}`)})
	if err != nil || !result.OK {
		t.Fatalf("read-only command at a two-line prompt: result=%+v err=%v", result, err)
	}
	if len(terminal.writes) == 0 {
		t.Fatal("keys were not written")
	}
}

func TestDisplayCallDoesNotEscapeShellOperators(t *testing.T) {
	display := DisplayCall(Call{Name: "send_keys", Args: json.RawMessage(`{"keys":"brew update && brew outdated <x>"}`)})
	if display != `send_keys "brew update && brew outdated <x>"` {
		t.Fatalf("display = %q", display)
	}
}
