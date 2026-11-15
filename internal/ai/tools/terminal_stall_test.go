package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
)

// stalledWriteTerminal parks every Write until its context ends, like a
// session write blocked on a stalled channel.
type stalledWriteTerminal struct {
	*fakeTerminal
	started chan struct{}
}

func (s *stalledWriteTerminal) Write(ctx context.Context, _ string, _ []byte) error {
	select {
	case s.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

// blockedReadTerminal parks screen and output reads, ignoring their
// context, until release is closed: a read stuck on a terminal lock.
type blockedReadTerminal struct {
	*fakeTerminal
	release chan struct{}
}

func (b *blockedReadTerminal) Snapshot(ctx context.Context, tabID string) (Screen, error) {
	<-b.release
	return b.fakeTerminal.Snapshot(ctx, tabID)
}

func (b *blockedReadTerminal) WaitSnapshot(ctx context.Context, tabID string) (Screen, error) {
	return b.Snapshot(ctx, tabID)
}

func (b *blockedReadTerminal) OutputSince(ctx context.Context, tabID string, seq uint64, maxBytes int) ([]byte, uint64, uint64, error) {
	<-b.release
	return b.fakeTerminal.OutputSince(ctx, tabID, seq, maxBytes)
}

func setSendKeysWriteTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()
	previous := sendKeysWriteTimeout
	sendKeysWriteTimeout = timeout
	t.Cleanup(func() { sendKeysWriteTimeout = previous })
}

func executeWithin(t *testing.T, budget time.Duration, execute func() Output) Output {
	t.Helper()
	done := make(chan Output, 1)
	go func() { done <- execute() }()
	select {
	case result := <-done:
		return result
	case <-time.After(budget):
		t.Fatalf("tool did not return within %v", budget)
		return Output{}
	}
}

func TestSendKeysStalledWriteTimesOutWithVisibleError(t *testing.T) {
	setSendKeysWriteTimeout(t, 50*time.Millisecond)
	terminal := &stalledWriteTerminal{fakeTerminal: &fakeTerminal{screen: Screen{Text: "$ "}}, started: make(chan struct{}, 1)}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	result := executeWithin(t, 5*time.Second, func() Output {
		return registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, sendKeysCall(`{"keys":"ls","enter":true}`), nil)
	})
	if result.OK || result.ExitCode == 0 {
		t.Fatalf("result = %+v, want a failure", result)
	}
	if !strings.Contains(result.Text, "发送按键超时") || !strings.Contains(result.Text, "终端可能无响应") {
		t.Fatalf("text = %q, want the send_keys timeout message", result.Text)
	}
	if result.OperationID == "" {
		t.Fatalf("timed-out write must keep its operation ID: %+v", result)
	}
	projected := provider.ProjectModelOutput("send_keys", result.OK, result.Text, false, false)
	if !strings.Contains(projected, "sending keys timed out") {
		t.Fatalf("model projection = %q, want the translated timeout", projected)
	}
}

func TestSendKeysStalledWriteReturnsOnCancel(t *testing.T) {
	terminal := &stalledWriteTerminal{fakeTerminal: &fakeTerminal{screen: Screen{Text: "$ "}}, started: make(chan struct{}, 1)}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-terminal.started
		cancel()
	}()
	result := executeWithin(t, 5*time.Second, func() Output {
		return registry.Execute(ctx, "job", Scope{SessionID: "s", TabID: "tab"}, sendKeysCall(`{"keys":"ls","enter":true}`), nil)
	})
	if result.OK {
		t.Fatalf("result = %+v, want a failure", result)
	}
	if strings.Contains(result.Text, "发送按键超时") {
		t.Fatalf("a cancelled run must not be reported as a terminal timeout: %q", result.Text)
	}
}

func TestSendKeysHealthyWriteUnaffectedByBound(t *testing.T) {
	setSendKeysWriteTimeout(t, 50*time.Millisecond)
	terminal := &fakeTerminal{screen: Screen{Text: "$ "}}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, sendKeysCall(`{"keys":"ls","enter":true}`), nil)
	if !result.OK || !strings.Contains(result.Text, "已发送") {
		t.Fatalf("result = %+v", result)
	}
	if len(terminal.writes) != 1 || string(terminal.writes[0]) != "ls\r" {
		t.Fatalf("writes = %q", terminal.writes)
	}
}

func waitForCall(args string) Call {
	return Call{ID: "wait", Name: "wait_for", Args: []byte(args)}
}

func TestWaitForBlockedSnapshotReturnsStructuredTimeout(t *testing.T) {
	for _, test := range []struct {
		name string
		args string
	}{
		{name: "visible tail", args: `{"pattern":"never","timeout_ms":100}`},
		{name: "quiet snapshot", args: `{"pattern":"never","timeout_ms":100,"quiet_ms":10}`},
		{name: "since_seq", args: `{"pattern":"never","timeout_ms":100,"since_seq":1}`},
		{name: "lines", args: `{"pattern":"never","timeout_ms":100,"lines":1}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			terminal := &blockedReadTerminal{fakeTerminal: &fakeTerminal{}, release: make(chan struct{})}
			t.Cleanup(func() { close(terminal.release) })
			registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
			started := time.Now()
			result := executeWithin(t, 5*time.Second, func() Output {
				return registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, waitForCall(test.args), nil)
			})
			if result.OK || result.Wait == nil || !result.Wait.TimedOut {
				t.Fatalf("result = %+v, want the structured wait timeout", result)
			}
			if elapsed := time.Since(started); elapsed < 100*time.Millisecond {
				t.Fatalf("returned after %v, before the wait budget", elapsed)
			}
		})
	}
}

func TestWaitForLineAnchoredPatternMatchesOneLine(t *testing.T) {
	const pattern = `"^===DONE-E===$"`
	for _, test := range []struct {
		name  string
		tail  []string
		match bool
	}{
		{name: "line among others", tail: []string{"$ ./run.sh", "===DONE-E===", "$ "}, match: true},
		{name: "space-padded row", tail: []string{"$ ./run.sh", "===DONE-E===      ", "$ "}, match: true},
		{name: "carriage return", tail: []string{"===DONE-E===\r", "$ "}, match: true},
		{name: "mid-line only", tail: []string{"$ echo ===DONE-E=== now", "$ "}, match: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			terminal := &fakeTerminal{screen: Screen{Text: strings.Join(test.tail, "\n"), Tail: test.tail}}
			registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
			result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, waitForCall(`{"pattern":`+pattern+`,"timeout_ms":50}`), nil)
			if result.OK != test.match {
				t.Fatalf("matched = %v, want %v: %+v", result.OK, test.match, result)
			}
		})
	}
}

func TestWaitForLineAnchoredPatternMatchesNewOutput(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		args   string
		match  bool
	}{
		{name: "since_seq", output: "building\r\n===DONE-E===\r\n$ ", args: `{"pattern":"^===DONE-E===$","timeout_ms":50,"since_seq":1}`, match: true},
		{name: "since_seq mid-line", output: "echo ===DONE-E=== x\r\n$ ", args: `{"pattern":"^===DONE-E===$","timeout_ms":50,"since_seq":1}`, match: false},
		{name: "multi-tab scan", output: "building\r\n===DONE-E===\r\n$ ", args: `{"pattern":"^===DONE-E===$","timeout_ms":50,"lines":1,"since_seq":1}`, match: true},
		{name: "multi-tab scan mid-line", output: "echo ===DONE-E=== x\r\n$ ", args: `{"pattern":"^===DONE-E===$","timeout_ms":50,"lines":1,"since_seq":1}`, match: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := []byte(test.output)
			terminal := &fakeTerminal{screen: Screen{Cols: 80, Rows: 24, Seq: 1}}
			// The output occupies sequences [1, 1+len) of the ring.
			terminal.outputSinceFunc = func(seq uint64, _ int) ([]byte, uint64, uint64, error) {
				end := 1 + uint64(len(data))
				if seq >= end {
					return nil, seq, end, nil
				}
				if seq < 1 {
					return nil, 1, 1, nil
				}
				return data[seq-1:], seq, end, nil
			}
			registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
			result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, waitForCall(test.args), nil)
			if result.OK != test.match {
				t.Fatalf("matched = %v, want %v: %+v", result.OK, test.match, result)
			}
		})
	}
}

func TestWaitPatternUnanchoredBehaviorUnchanged(t *testing.T) {
	for _, test := range []struct {
		pattern string
		lines   []string
		match   bool
	}{
		{pattern: "READY", lines: []string{"x", "server READY now"}, match: true},
		{pattern: `\$ $`, lines: []string{"out", "user@host:~$ "}, match: true},
		{pattern: "a\nb", lines: []string{"a", "b"}, match: true},
		{pattern: "", lines: nil, match: true},
		{pattern: "^x$", lines: []string{"xx"}, match: false},
	} {
		regex, err := CompileWaitPattern(test.pattern)
		if err != nil {
			t.Fatal(err)
		}
		if got := MatchWaitLines(regex, test.lines); got != test.match {
			t.Fatalf("MatchWaitLines(%q, %q) = %v, want %v", test.pattern, test.lines, got, test.match)
		}
	}
	if _, err := CompileWaitPattern("("); err == nil {
		t.Fatal("invalid pattern compiled")
	}
}

func TestRunWatchedAbandonsBlockedTool(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	output := executeWithin(t, 5*time.Second, func() Output {
		output, err := runWatched(context.Background(), "wait_for", 50*time.Millisecond, func(context.Context) (Output, error) {
			<-release
			return OK("late"), nil
		})
		if err != nil {
			t.Errorf("runWatched error = %v", err)
		}
		return output
	})
	if output.OK || !strings.Contains(output.Text, "仍未返回") {
		t.Fatalf("output = %+v, want the watchdog failure", output)
	}
	projected := provider.ProjectModelOutput("wait_for", output.OK, output.Text, false, false)
	if !strings.Contains(projected, "did not return within") {
		t.Fatalf("model projection = %q, want the translated watchdog failure", projected)
	}
}

func TestRunWatchedReturnsPromptlyOnCancel(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		<-started
		cancel()
	}()
	type outcome struct {
		output Output
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		output, err := runWatched(ctx, "send_keys", time.Hour, func(context.Context) (Output, error) {
			close(started)
			<-release
			return OK("late"), nil
		})
		done <- outcome{output, err}
	}()
	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("runWatched = %+v, %v; want context.Canceled", result.output, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runWatched ignored cancellation")
	}
}

func TestRunWatchedHealthyAndPanicPaths(t *testing.T) {
	output, err := runWatched(context.Background(), "read_screen", time.Minute, func(context.Context) (Output, error) {
		return OK("screen"), nil
	})
	if err != nil || !output.OK || output.Text != "screen" {
		t.Fatalf("runWatched = %+v, %v", output, err)
	}
	output, err = runWatched(context.Background(), "read_screen", time.Minute, func(context.Context) (Output, error) {
		panic("boom")
	})
	if err != nil || !output.Panic {
		t.Fatalf("panic path = %+v, %v; want a panic output", output, err)
	}
}

func TestToolWatchdogCoversOnlyTerminalTools(t *testing.T) {
	if limit := toolWatchdog(WaitForArgs{TimeoutMS: 120_000}); limit <= waitForMaxTimeout {
		t.Fatalf("wait_for watchdog %v must exceed its own timeout", limit)
	}
	if limit := toolWatchdog(SendKeysArgs{WaitMS: maxSendKeysWaitMS, Paste: true, Enter: true, SubmitDelayMS: maxSubmitDelayMS}); limit <= sendKeysWriteTimeout+(maxSendKeysWaitMS+maxSubmitDelayMS)*time.Millisecond {
		t.Fatalf("send_keys watchdog %v must exceed its own budget", limit)
	}
	if limit := toolWatchdog(ExecCommandsArgs{Commands: []string{"sleep 1"}, Timeout: 300}); limit != 0 {
		t.Fatalf("exec_commands watchdog = %v, want exempt", limit)
	}
}
