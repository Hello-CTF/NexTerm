package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/outcome"
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
			terminal.setCommandState(CommandState{Sequence: 4, LastExitCode: 2, HasLastExitCode: true, ExitCodeSequence: 4})
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

func TestSendKeysWaitWithoutOSC133ReportsUnverified(t *testing.T) {
	terminal := &fakeTerminal{screen: Screen{Text: "$ "}}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	started := time.Now()
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, sendKeysCall(`{"keys":"ls","enter":true,"wait_ms":120}`), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if elapsed := time.Since(started); elapsed < 100*time.Millisecond {
		t.Fatalf("capability present but no 133 seen: wait budget should be honored, returned after %v", elapsed)
	}
	if !strings.Contains(result.Text, "未观察到新命令开始") || !strings.Contains(result.Text, "结果未验证") {
		t.Fatalf("text missing unverified note: %q", result.Text)
	}
	if len(terminal.writes) != 1 {
		t.Fatalf("writes=%q", terminal.writes)
	}
}

func TestSendKeysWaitWithoutCommandStateCapabilityReturnsImmediately(t *testing.T) {
	registry := NewRegistry(Dependencies{Terminal: legacyTerminal{}, PollInterval: time.Millisecond})
	started := time.Now()
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, sendKeysCall(`{"keys":"ls","enter":true,"wait_ms":4000}`), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("no capability should return immediately, waited %v", elapsed)
	}
	if !strings.Contains(result.Text, "不支持 OSC 133 命令跟踪") || !strings.Contains(result.Text, "wait_for") {
		t.Fatalf("fallback text missing explicit unverified note: %q", result.Text)
	}
}

func TestSendKeysWaitFirstCommandWithZeroSequence(t *testing.T) {
	// A fresh 133-capable shell has Sequence 0 until its first command; the
	// wait must still track that command to completion (M115 CommandState).
	terminal := &fakeTerminal{screen: Screen{Text: "$ "}}
	terminal.onWrite = func() {
		terminal.setCommandState(CommandState{Sequence: 1, Running: true})
		go func() {
			time.Sleep(5 * time.Millisecond)
			terminal.setCommandState(CommandState{Sequence: 1, LastExitCode: 3, HasLastExitCode: true, ExitCodeSequence: 1})
		}()
	}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, sendKeysCall(`{"keys":"ls","enter":true,"wait_ms":2000}`), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "命令已完成") || !strings.Contains(result.Text, "退出码 3") || result.ExitCode != 3 {
		t.Fatalf("first command not tracked: %+v", result)
	}
}

func TestSendKeysWaitTimeoutDoesNotBorrowPreviousExitCode(t *testing.T) {
	// 上一命令 exit 7；本次命令仍在运行即截止：不能冒用旧码。
	terminal := &fakeTerminal{screen: Screen{Text: "$ sleep 9\r\n"}}
	terminal.commandState = CommandState{Sequence: 3, LastExitCode: 7, HasLastExitCode: true, ExitCodeSequence: 3}
	terminal.onWrite = func() {
		// tracker 语义：退出码仍绑定在序号 3 上，与正在运行的序号 4 无关
		terminal.setCommandState(CommandState{Sequence: 4, Running: true, LastExitCode: 7, HasLastExitCode: true, ExitCodeSequence: 3})
	}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, sendKeysCall(`{"keys":"sleep 9","enter":true,"wait_ms":120}`), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "命令仍在运行") {
		t.Fatalf("text=%q", result.Text)
	}
	if strings.Contains(result.Text, "退出码") || result.ExitUnknown != true {
		t.Fatalf("stale exit code leaked into running timeout: %+v", result)
	}
}

func TestSendKeysWaitBareFinishDoesNotBorrowPreviousExitCode(t *testing.T) {
	// 上一命令 exit 7；本次命令 bare D（无退出码）：不能冒用旧码。
	terminal := &fakeTerminal{screen: Screen{Text: "$ true\r\n$ "}}
	terminal.commandState = CommandState{Sequence: 3, LastExitCode: 7, HasLastExitCode: true, ExitCodeSequence: 3}
	terminal.onWrite = func() {
		terminal.setCommandState(CommandState{Sequence: 4, Running: true, LastExitCode: 7, HasLastExitCode: true, ExitCodeSequence: 3})
		go func() {
			time.Sleep(5 * time.Millisecond)
			// bare D：退出码保持绑定在序号 3，不借给序号 4
			terminal.setCommandState(CommandState{Sequence: 4, LastExitCode: 7, HasLastExitCode: true, ExitCodeSequence: 3})
		}()
	}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, sendKeysCall(`{"keys":"true","enter":true,"wait_ms":2000}`), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "命令已完成") {
		t.Fatalf("text=%q", result.Text)
	}
	if strings.Contains(result.Text, "退出码") || result.ExitUnknown != true {
		t.Fatalf("bare D borrowed the previous exit code: %+v", result)
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
			terminal.setCommandState(CommandState{Sequence: 5, LastExitCode: 130, HasLastExitCode: true, ExitCodeSequence: 5})
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

func TestSendKeysWaitUnknownOutcomeRecordedAsUnknown(t *testing.T) {
	// 仍在运行即截止：结果必须结构化为 exit-unknown，outcome 记 unknown 而非 exit 0。
	_, ledger, sqliteStore := outcomeFixture(t)
	terminal := &fakeTerminal{screen: Screen{Text: "$ sleep 9\r\n"}}
	terminal.commandState = CommandState{Sequence: 3}
	terminal.onWrite = func() {
		terminal.setCommandState(CommandState{Sequence: 4, Running: true})
	}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond, Outcome: ledger})
	call := Call{ID: "keys", Name: "send_keys", Args: json.RawMessage(`{"keys":"sleep 9","enter":true,"wait_ms":80}`), AuthorizationID: "guard:test"}
	result := registry.Execute(context.Background(), "job-1", Scope{SessionID: "s", TabID: "tab"}, call, nil)
	if !result.OK || !result.ExitUnknown {
		t.Fatalf("result=%+v", result)
	}
	record := recordedOutcome(t, sqliteStore, OutcomeKey("job-1", "keys"))
	if record.Outcome != outcome.OutcomeUnknown {
		t.Fatalf("recorded outcome = %s, want unknown; record=%+v", record.Outcome, record)
	}
	if record.Result.ExitCode != nil {
		t.Fatalf("unknown outcome must not carry an exit code: %+v", record.Result)
	}
}

func TestSendKeysWaitZeroExitRecordedAsAccepted(t *testing.T) {
	// 本次 D 真实携带 exit 0：必须保留为 Accepted/exit 0，不得标 unknown。
	_, ledger, sqliteStore := outcomeFixture(t)
	terminal := &fakeTerminal{screen: Screen{Text: "$ true\r\n$ "}}
	terminal.commandState = CommandState{Sequence: 3}
	terminal.onWrite = func() {
		terminal.setCommandState(CommandState{Sequence: 4, Running: true})
		go func() {
			time.Sleep(5 * time.Millisecond)
			terminal.setCommandState(CommandState{Sequence: 4, LastExitCode: 0, HasLastExitCode: true, ExitCodeSequence: 4})
		}()
	}
	registry := NewRegistry(Dependencies{Terminal: terminal, PollInterval: time.Millisecond, Outcome: ledger})
	call := Call{ID: "keys", Name: "send_keys", Args: json.RawMessage(`{"keys":"true","enter":true,"wait_ms":2000}`), AuthorizationID: "guard:test"}
	result := registry.Execute(context.Background(), "job-1", Scope{SessionID: "s", TabID: "tab"}, call, nil)
	if !result.OK || result.ExitUnknown || result.ExitCode != 0 || !strings.Contains(result.Text, "退出码 0") {
		t.Fatalf("result=%+v", result)
	}
	record := recordedOutcome(t, sqliteStore, OutcomeKey("job-1", "keys"))
	if record.Outcome != outcome.OutcomeAccepted || record.Result.ExitCode == nil || *record.Result.ExitCode != 0 {
		t.Fatalf("recorded outcome = %s code=%v, want accepted/0; record=%+v", record.Outcome, record.Result.ExitCode, record)
	}
}
