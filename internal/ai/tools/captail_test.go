package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

var omissionPattern = regexp.MustCompile(`\n\[\.\.\. 已省略 (\d+) 行 / (\d+) 字节 \.\.\.\]\n`)

func capNumberedLines(count int, prefix string) string {
	lines := make([]string, count)
	for i := range lines {
		lines[i] = fmt.Sprintf("%s line %d", prefix, i+1)
	}
	return strings.Join(lines, "\n")
}

func TestCapHeadTailKeepsSmallOutputUnchanged(t *testing.T) {
	for _, text := range []string{"", "ok", "中文\n输出\n", capNumberedLines(MaxOutputLines, "x"), strings.Repeat("a", MaxOutputBytes)} {
		display, model, cut := capHeadTail(text)
		if cut || display != text || model != text {
			t.Fatalf("small output changed: cut=%v len=%d->%d", cut, len(text), len(display))
		}
	}
}

func TestCapHeadTailKeepsTailAndCountsOmission(t *testing.T) {
	text := capNumberedLines(2000, "build") + "\nerror: link failed"
	display, model, cut := capHeadTail(text)
	if !cut {
		t.Fatal("long output was not cut")
	}
	if !strings.HasSuffix(display, "error: link failed") || !strings.HasPrefix(display, "build line 1\n") {
		t.Fatalf("head or tail lost: %q ... %q", display[:20], display[len(display)-40:])
	}
	match := omissionPattern.FindStringSubmatch(display)
	if match == nil {
		t.Fatalf("no omission marker in %q", display)
	}
	if !strings.Contains(model, "[... omitted "+match[1]+" lines / "+match[2]+" bytes ...]") {
		t.Fatalf("model text lacks matching English marker")
	}
	parts := omissionPattern.Split(display, 2)
	kept := strings.Count(parts[0], "\n") + 1 + strings.Count(parts[1], "\n") + 1
	var omittedLines, omittedBytes int
	fmt.Sscan(match[1], &omittedLines)
	fmt.Sscan(match[2], &omittedBytes)
	if kept+omittedLines != 2001 {
		t.Fatalf("kept %d + omitted %d != 2001 lines", kept, omittedLines)
	}
	if len(parts[0])+len(parts[1])+omittedBytes != len(text) {
		t.Fatalf("byte accounting off: %d + %d + %d != %d", len(parts[0]), len(parts[1]), omittedBytes, len(text))
	}
	if kept > MaxOutputLines {
		t.Fatalf("kept %d lines, over the %d budget", kept, MaxOutputLines)
	}
	if len(display) > MaxOutputBytes {
		t.Fatalf("display %d bytes exceeds budget", len(display))
	}
}

func TestCapHeadTailByteBudgetKeepsValidCJK(t *testing.T) {
	// Long lines force the byte budget; the cut points land inside multi-byte runes.
	text := strings.Repeat("构建失败，请检查日志。", 40000)
	display, model, cut := capHeadTail(text)
	if !cut || !utf8.ValidString(display) || !utf8.ValidString(model) {
		t.Fatalf("cut=%v validDisplay=%v validModel=%v", cut, utf8.ValidString(display), utf8.ValidString(model))
	}
	if len(display) > MaxOutputBytes || !strings.HasSuffix(display, "日志。") || !strings.HasPrefix(display, "构建失败") {
		t.Fatalf("unexpected cut: len=%d", len(display))
	}
	multiLine := strings.Repeat("错误：磁盘空间不足\n", 20000)
	display, _, _ = capHeadTail(multiLine)
	if !utf8.ValidString(display) || len(display) > MaxOutputBytes {
		t.Fatalf("multi-line CJK output invalid or oversized (%d bytes)", len(display))
	}
}

func TestExecCommandsKeepsEveryExitCodeWhenOutputIsHuge(t *testing.T) {
	transport := &seqExecTransport{results: []base.ExecResult{
		{Stdout: capNumberedLines(5000, "first"), Stderr: capNumberedLines(3000, "warn"), ExitCode: intPtr(2)},
		{Stdout: capNumberedLines(5000, "second") + "\nfatal: second failed", ExitCode: intPtr(3)},
		{Stdout: "done", ExitCode: intPtr(0)},
	}}
	registry := NewRegistry(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "session"}, execCalls("call", "cmd-one", "cmd-two", "cmd-three"), nil)
	if !result.Truncated {
		t.Fatal("huge output was not flagged truncated")
	}
	for _, text := range []string{result.Text, result.ModelText} {
		for _, want := range []string{"$ cmd-one\n", "[exit_code=2]", "$ cmd-two\n", "[exit_code=3]", "$ cmd-three\n", "done", "[exit_code=0]", "fatal: second failed", "[stderr]"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%q lost from combined output", want)
			}
		}
		if len(text) > MaxOutputBytes+2048 {
			t.Fatalf("combined output %d bytes is over budget", len(text))
		}
	}
	if !strings.Contains(result.ModelText, "omitted") || strings.Contains(result.ModelText, "已省略") {
		t.Fatalf("model text marker not in English: %q", result.ModelText[:200])
	}
	if !omissionPattern.MatchString(result.Text) {
		t.Fatal("display text lacks the omission marker")
	}
}

func intPtr(value int) *int { return &value }

func execCalls(id string, commands ...string) Call {
	args, err := json.Marshal(ExecCommandsArgs{Commands: commands})
	if err != nil {
		panic(err)
	}
	return Call{ID: id, Name: "exec_commands", Args: args}
}

// seqExecTransport answers each Exec with the next scripted result.
type seqExecTransport struct {
	fakeTransport
	results []base.ExecResult
	next    int
}

func (s *seqExecTransport) Exec(_ context.Context, _ string, _ base.ExecOptions) (base.ExecResult, error) {
	result := s.results[s.next]
	s.next++
	return result, nil
}

func TestExecCommandsSmallOutputIsUnchanged(t *testing.T) {
	transport := &seqExecTransport{results: []base.ExecResult{{Stdout: "hello", ExitCode: intPtr(0)}}}
	registry := NewRegistry(Dependencies{Transport: func(context.Context, string) (base.Transport, error) { return transport, nil }})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "session"}, execCalls("call", "echo hello"), nil)
	if result.Truncated || result.Text != "$ echo hello\nhello\n[exit_code=0]\n\n" || result.ModelText != result.Text {
		t.Fatalf("small exec output changed: %+v", result)
	}
}
