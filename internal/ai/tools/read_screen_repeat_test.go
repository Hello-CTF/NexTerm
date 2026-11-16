package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func readScreenOnce(t *testing.T, registry *Registry, args string) string {
	t.Helper()
	result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"}, Call{ID: "r", Name: "read_screen", Args: json.RawMessage(args)}, nil)
	if !result.OK {
		t.Fatalf("read_screen %s: %+v", args, result)
	}
	return result.Text
}

func setScreen(terminal *fakeTerminal, screen Screen) {
	terminal.mu.Lock()
	terminal.screen = screen
	terminal.mu.Unlock()
}

func TestReadScreenRepeatOnNormalScreenReturnsOnlyNewOutput(t *testing.T) {
	ring := newFakeRing(0, "$ old-command\r\nold output\r\n$ ")
	terminal := anchoredTerminal(ring)
	setScreen(terminal, Screen{Text: "$ old-command\nold output\n$ \n\n\n", Seq: ring.latest, Cols: 80, Rows: 24, CursorRow: 2, CursorCol: 2})
	registry := NewRegistry(Dependencies{Terminal: terminal})

	first := readScreenOnce(t, registry, `{}`)
	if !strings.Contains(first, "old output") || strings.HasSuffix(first, "\n") {
		t.Fatalf("the first read must show the whole screen without trailing blank rows: %q", first)
	}

	ring.append("ls\r\nfile-a\r\n$ ")
	setScreen(terminal, Screen{Text: "$ old-command\nold output\n$ ls\nfile-a\n$ ", Seq: ring.latest, Cols: 80, Rows: 24, CursorRow: 4, CursorCol: 2})
	second := readScreenOnce(t, registry, `{}`)
	if !strings.Contains(second, "file-a") || strings.Contains(second, "old output") || !strings.Contains(second, "以来的新输出") {
		t.Fatalf("a repeat read must show only the output since the previous read: %q", second)
	}

	unchanged := readScreenOnce(t, registry, `{}`)
	if !strings.Contains(unchanged, "没有新输出") || strings.Contains(unchanged, "file-a") {
		t.Fatalf("an unchanged screen must say so instead of repeating it: %q", unchanged)
	}

	full := readScreenOnce(t, registry, `{"full":true}`)
	if !strings.Contains(full, "old output") || !strings.Contains(full, "file-a") {
		t.Fatalf("full=true must return the whole screen: %q", full)
	}
}

func TestReadScreenRepeatShowsTheWholeScreenForFullScreenProgramsResizesAndLongOutput(t *testing.T) {
	ring := newFakeRing(0, "top")
	terminal := anchoredTerminal(ring)
	setScreen(terminal, Screen{Text: "PID USER\n1 root", Seq: ring.latest, Cols: 80, Rows: 24, AltScreen: true})
	registry := NewRegistry(Dependencies{Terminal: terminal})
	readScreenOnce(t, registry, `{}`)
	ring.append(" refresh")
	setScreen(terminal, Screen{Text: "PID USER\n1 root\n2 daemon", Seq: ring.latest, Cols: 80, Rows: 24, AltScreen: true})
	if alt := readScreenOnce(t, registry, `{}`); !strings.Contains(alt, "1 root") || strings.Contains(alt, "以来") {
		t.Fatalf("a full-screen program must always return its whole screen: %q", alt)
	}

	setScreen(terminal, Screen{Text: "$ back\n$ ", Seq: ring.latest, Cols: 80, Rows: 24})
	readScreenOnce(t, registry, `{}`)
	ring.append("x")
	setScreen(terminal, Screen{Text: "$ back\n$ x", Seq: ring.latest, Cols: 100, Rows: 30})
	if resized := readScreenOnce(t, registry, `{}`); !strings.Contains(resized, "$ back") {
		t.Fatalf("a resized terminal must return its whole screen: %q", resized)
	}

	ring.append(strings.Repeat("y", defaultDiffBytes+10))
	setScreen(terminal, Screen{Text: "$ back\n$ xyyy", Seq: ring.latest, Cols: 100, Rows: 30})
	if long := readScreenOnce(t, registry, `{}`); !strings.Contains(long, "$ back") || strings.Contains(long, "以来的新输出") {
		t.Fatalf("output longer than one page must fall back to the whole screen: %q", long)
	}
}

func TestReadScreenRejectsFullWithAnchors(t *testing.T) {
	registry := NewRegistry(Dependencies{Terminal: anchoredTerminal(newFakeRing(0, "x"))})
	result := registry.Execute(context.Background(), "job", Scope{TabID: "tab"}, Call{ID: "r", Name: "read_screen", Args: json.RawMessage(`{"full":true,"since_seq":1}`)}, nil)
	if result.OK {
		t.Fatalf("full with since_seq must be refused: %+v", result)
	}
}

func TestReadScreenMemoryFollowsTheConversationAndSkipsSubagents(t *testing.T) {
	ring := newFakeRing(0, "$ old\r\nold output\r\n$ ")
	terminal := anchoredTerminal(ring)
	setScreen(terminal, Screen{Text: "$ old\nold output\n$ ", Seq: ring.latest, Cols: 80, Rows: 24})
	registry := NewRegistry(Dependencies{Terminal: terminal})
	read := func(execution *Execution, args string) string {
		t.Helper()
		ctx := execution.screenMemoryContext(context.Background())
		result := registry.Execute(ctx, execution.JobID, Scope{TabID: "tab"}, Call{ID: "r", Name: "read_screen", Args: json.RawMessage(args)}, nil)
		if !result.OK {
			t.Fatalf("read_screen: %+v", result)
		}
		return result.Text
	}
	firstRun := &Execution{JobID: "job-1", ConversationID: "conv", Registry: registry}
	nextRun := &Execution{JobID: "job-2", ConversationID: "conv", Registry: registry}
	subagent := &Execution{JobID: "job-2", ConversationID: "conv", Registry: registry, Subagent: true}

	read(firstRun, `{}`)
	if text := read(nextRun, `{}`); !strings.Contains(text, "没有新输出") {
		t.Fatalf("a follow-up run of the conversation must continue from the earlier read: %q", text)
	}
	if text := read(subagent, `{}`); !strings.Contains(text, "old output") {
		t.Fatalf("a subagent has its own context and must get the whole screen: %q", text)
	}
	registry.ForgetScreenReads("conv")
	if text := read(nextRun, `{}`); !strings.Contains(text, "old output") {
		t.Fatalf("after the history was compacted the next read must show the whole screen: %q", text)
	}
	other := &Execution{JobID: "job-3", ConversationID: "other", Registry: registry}
	if text := read(other, `{}`); !strings.Contains(text, "old output") {
		t.Fatalf("another conversation must not reuse these reads: %q", text)
	}
}
