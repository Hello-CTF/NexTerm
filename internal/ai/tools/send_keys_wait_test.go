package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func sendKeysCall(args string) Call {
	return Call{ID: "keys", Name: "send_keys", Args: json.RawMessage(args)}
}

func TestSendKeysWaitReturnsExitCodeAndTail(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ls\r\nfile1\r\n$ "}}
	terminal.commandState = CommandState{Sequence: 3}
	terminal.onWrite = func() {
		terminal.setCommandState(CommandState{Sequence: 4, Running: true})
		go func() {
			time.Sleep(5 * time.Millisecond)
			terminal.setCommandState(CommandState{Sequence: 4, LastExitCode: 2, HasLastExitCode: true})
		}()
	}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, sendKeysCall(`{"keys":"ls","enter":true,"wait_ms":2000}`), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if result.ExitCode != 2 {
		t.Fatalf("exit code = %d, want 2: %+v", result.ExitCode, result)
	}
	for _, want := range []string{"命令已完成", "退出码 2", "命令序号 4", "屏幕尾部"} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("text missing %q: %q", want, result.Text)
		}
	}
}

func TestSendKeysWaitWithoutOSC133FallsBackWithoutFalseSuccess(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ "}}
	started := time.Now()
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, sendKeysCall(`{"keys":"ls","enter":true,"wait_ms":4000}`), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("no-133 fallback waited %v, want immediate return", elapsed)
	}
	if !strings.Contains(result.Text, "无法确认命令是否完成") || !strings.Contains(result.Text, "wait_for") {
		t.Fatalf("fallback text missing explicit unverified note: %q", result.Text)
	}
	if len(terminal.writes) != 1 {
		t.Fatalf("writes=%q", terminal.writes)
	}
}

func TestSendKeysWaitBusyReturnsShort(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ make\r\n..."}}
	terminal.commandState = CommandState{Sequence: 5, Running: true}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	started := time.Now()
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, sendKeysCall(`{"keys":"y","enter":true,"wait_ms":4000}`), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	elapsed := time.Since(started)
	if elapsed > 2*time.Second {
		t.Fatalf("busy return burned the full wait: %v", elapsed)
	}
	if !strings.Contains(result.Text, "已有命令在运行") || !strings.Contains(result.Text, "仍在运行") {
		t.Fatalf("busy text=%q", result.Text)
	}
}

func TestSendKeysWaitBusyClearsWhenRunningCommandFinishes(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ make\r\ndone\r\n$ "}}
	terminal.commandState = CommandState{Sequence: 5, Running: true}
	terminal.onWrite = func() {
		go func() {
			time.Sleep(5 * time.Millisecond)
			terminal.setCommandState(CommandState{Sequence: 5, LastExitCode: 130, HasLastExitCode: true})
		}()
	}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, sendKeysCall(`{"keys":"y","enter":true,"wait_ms":4000}`), nil)
	if !result.OK || result.ExitCode != 130 {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "该命令已结束") || !strings.Contains(result.Text, "退出码 130") {
		t.Fatalf("text=%q", result.Text)
	}
}

func TestSendKeysWaitTimesOutWhenNoCommandStarts(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ ls "}}
	terminal.commandState = CommandState{Sequence: 3}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, sendKeysCall(`{"keys":"ls","wait_ms":120}`), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "未观察到新命令") {
		t.Fatalf("text=%q", result.Text)
	}
}

func TestSendKeysWaitStillRunningAtDeadline(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ sleep 100\r\n"}}
	terminal.commandState = CommandState{Sequence: 3}
	terminal.onWrite = func() {
		terminal.setCommandState(CommandState{Sequence: 4, Running: true})
	}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, sendKeysCall(`{"keys":"sleep 100","enter":true,"wait_ms":120}`), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "命令仍在运行") {
		t.Fatalf("text=%q", result.Text)
	}
}

func TestSendKeysWaitRejectsOutOfRangeWait(t *testing.T) {
	terminal := &fakeTerminal{}
	registry := NewRegistry(Dependencies{Terminal: terminal})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, sendKeysCall(`{"keys":"ls","wait_ms":20000}`), nil)
	if result.OK || !strings.Contains(result.Text, "wait_ms") {
		t.Fatalf("result=%+v", result)
	}
	if len(terminal.writes) != 0 {
		t.Fatalf("writes=%q", terminal.writes)
	}
}

func TestSendKeysWithoutWaitKeepsFireAndForget(t *testing.T) {
	terminal := &fakeTerminal{}
	terminal.commandState = CommandState{Sequence: 9, Running: true}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, sendKeysCall(`{"keys":"ls","enter":true}`), nil)
	if !result.OK || result.Text != "已发送" {
		t.Fatalf("result=%+v", result)
	}
}

func TestReadScreenIncludesCommandStateWhenTracked(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ make\r\n"}}
	terminal.commandState = CommandState{Sequence: 7, Running: true}
	registry := NewRegistry(Dependencies{Terminal: terminal})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, Call{ID: "r", Name: "read_screen", Args: json.RawMessage(`{}`)}, nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "命令状态：运行中，序号 7") {
		t.Fatalf("text=%q", result.Text)
	}
	terminal.setCommandState(CommandState{Sequence: 8, LastExitCode: 1, HasLastExitCode: true})
	result = registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, Call{ID: "r2", Name: "read_screen", Args: json.RawMessage(`{}`)}, nil)
	if !strings.Contains(result.Text, "命令状态：空闲，序号 8，上次退出码 1") {
		t.Fatalf("text=%q", result.Text)
	}
	terminal.setCommandState(CommandState{})
	result = registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, Call{ID: "r3", Name: "read_screen", Args: json.RawMessage(`{}`)}, nil)
	if strings.Contains(result.Text, "命令状态") {
		t.Fatalf("untracked terminal should not report command state: %q", result.Text)
	}
}
