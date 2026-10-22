package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

func shellHistoryTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func writeHistoryFile(t *testing.T, home, name, content string) {
	t.Helper()
	path := filepath.Join(home, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func shellHistoryCall(limit int) Call {
	args, _ := json.Marshal(ShellHistoryArgs{Limit: limit})
	return Call{ID: "h", Name: "shell_history", Args: args}
}

func TestShellHistoryParsesZshNewestFirst(t *testing.T) {
	home := shellHistoryTestHome(t)
	writeHistoryFile(t, home, ".zsh_history", ": 1700000000:0;ls -la\n: 1700000001:0;git status\n: 1700000002:0;make test\n")
	registry := NewRegistry(Dependencies{LocalHistory: true})
	result := registry.Execute(context.Background(), "job", Scope{}, shellHistoryCall(0), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "[本机 zsh shell 历史，最新 3 条]") {
		t.Fatalf("text=%q", result.Text)
	}
	newest := strings.Index(result.Text, "make test")
	middle := strings.Index(result.Text, "git status")
	oldest := strings.Index(result.Text, "ls -la")
	if newest < 0 || middle < 0 || oldest < 0 || !(newest < middle && middle < oldest) {
		t.Fatalf("entries not newest-first: %q", result.Text)
	}
}

func TestShellHistoryParsesPlainZshLines(t *testing.T) {
	home := shellHistoryTestHome(t)
	writeHistoryFile(t, home, ".zsh_history", "ls -la\ngit status\nmake test\n")
	registry := NewRegistry(Dependencies{LocalHistory: true})
	result := registry.Execute(context.Background(), "job", Scope{}, shellHistoryCall(0), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "[本机 zsh shell 历史，最新 3 条]") {
		t.Fatalf("text=%q", result.Text)
	}
	newest := strings.Index(result.Text, "make test")
	oldest := strings.Index(result.Text, "ls -la")
	if newest < 0 || oldest < 0 || newest > oldest {
		t.Fatalf("plain zsh entries wrong: %q", result.Text)
	}
	limited := registry.Execute(context.Background(), "job", Scope{}, shellHistoryCall(2), nil)
	if !strings.Contains(limited.Text, "最新 2 条") || strings.Contains(limited.Text, "ls -la") {
		t.Fatalf("plain zsh limit wrong: %q", limited.Text)
	}
}

func TestShellHistoryZshExtendedSkipsContinuationLines(t *testing.T) {
	home := shellHistoryTestHome(t)
	writeHistoryFile(t, home, ".zsh_history", ": 1700000000:0;for i in 1 2; do\nplain-continuation\n: 1700000001:0;done\n")
	registry := NewRegistry(Dependencies{LocalHistory: true})
	result := registry.Execute(context.Background(), "job", Scope{}, shellHistoryCall(0), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if strings.Contains(result.Text, "plain-continuation") {
		t.Fatalf("continuation line treated as a command: %q", result.Text)
	}
	if !strings.Contains(result.Text, "for i in 1 2; do") || !strings.Contains(result.Text, "done") {
		t.Fatalf("extended entries missing: %q", result.Text)
	}
}

func TestShellHistoryParsesBashSkippingTimestamps(t *testing.T) {
	home := shellHistoryTestHome(t)
	writeHistoryFile(t, home, ".bash_history", "#1700000000\nls\necho hi\n#1700000001\ngit status\n")
	registry := NewRegistry(Dependencies{LocalHistory: true})
	result := registry.Execute(context.Background(), "job", Scope{}, shellHistoryCall(0), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "[本机 bash shell 历史，最新 3 条]") {
		t.Fatalf("text=%q", result.Text)
	}
	if strings.Contains(result.Text, "#1700000000") {
		t.Fatalf("timestamp comment leaked: %q", result.Text)
	}
}

func TestShellHistoryParsesFishCmdLines(t *testing.T) {
	home := shellHistoryTestHome(t)
	writeHistoryFile(t, home, ".local/share/fish/fish_history", "- cmd: ls\n  when: 1700000000\n- cmd: make test\n  when: 1700000001\n")
	registry := NewRegistry(Dependencies{LocalHistory: true})
	result := registry.Execute(context.Background(), "job", Scope{}, shellHistoryCall(0), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "[本机 fish shell 历史，最新 2 条]") {
		t.Fatalf("text=%q", result.Text)
	}
	if strings.Contains(result.Text, "when:") {
		t.Fatalf("fish metadata leaked: %q", result.Text)
	}
}

func TestShellHistoryPrefersFirstExistingShell(t *testing.T) {
	home := shellHistoryTestHome(t)
	writeHistoryFile(t, home, ".zsh_history", ": 1700000000:0;zsh-command\n")
	writeHistoryFile(t, home, ".bash_history", "bash-command\n")
	registry := NewRegistry(Dependencies{LocalHistory: true})
	result := registry.Execute(context.Background(), "job", Scope{}, shellHistoryCall(0), nil)
	if !result.OK || !strings.Contains(result.Text, "zsh") || strings.Contains(result.Text, "bash-command") {
		t.Fatalf("text=%q", result.Text)
	}
}

func TestShellHistoryLimitClampsToNewest(t *testing.T) {
	home := shellHistoryTestHome(t)
	writeHistoryFile(t, home, ".bash_history", "one\ntwo\nthree\nfour\n")
	registry := NewRegistry(Dependencies{LocalHistory: true})
	result := registry.Execute(context.Background(), "job", Scope{}, shellHistoryCall(2), nil)
	if !result.OK || !strings.Contains(result.Text, "最新 2 条") || strings.Contains(result.Text, "one") {
		t.Fatalf("text=%q", result.Text)
	}
	result = registry.Execute(context.Background(), "job", Scope{}, shellHistoryCall(9999), nil)
	if !result.OK || !strings.Contains(result.Text, "最新 4 条") {
		t.Fatalf("hard cap text=%q", result.Text)
	}
	result = registry.Execute(context.Background(), "job", Scope{}, shellHistoryCall(-1), nil)
	if result.OK {
		t.Fatalf("negative limit accepted: %+v", result)
	}
}

func TestShellHistoryRedactsSecrets(t *testing.T) {
	home := shellHistoryTestHome(t)
	writeHistoryFile(t, home, ".bash_history", "curl -H \"Authorization: Bearer tok-123\" https://x\nmysql -psecretpw db\nexport password=hunter2\n")
	registry := NewRegistry(Dependencies{LocalHistory: true})
	result := registry.Execute(context.Background(), "job", Scope{}, shellHistoryCall(0), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	for _, leaked := range []string{"tok-123", "secretpw", "hunter2"} {
		if strings.Contains(result.Text, leaked) {
			t.Fatalf("secret %q leaked: %q", leaked, result.Text)
		}
	}
	if !strings.Contains(result.Text, "<redacted>") {
		t.Fatalf("redaction marker missing: %q", result.Text)
	}
}

func TestShellHistoryMissingFilesListsAttemptedPaths(t *testing.T) {
	shellHistoryTestHome(t)
	registry := NewRegistry(Dependencies{LocalHistory: true})
	result := registry.Execute(context.Background(), "job", Scope{}, shellHistoryCall(0), nil)
	if result.OK || !strings.Contains(result.Text, "未找到本机 shell 历史文件") ||
		!strings.Contains(result.Text, ".zsh_history") || !strings.Contains(result.Text, ".bash_history") || !strings.Contains(result.Text, "fish_history") {
		t.Fatalf("result=%+v", result)
	}
}

func TestShellHistoryReadsOnlyTailOfLargeFile(t *testing.T) {
	home := shellHistoryTestHome(t)
	var builder strings.Builder
	builder.WriteString("ancient-command\n")
	for i := 0; i < 300000; i++ {
		builder.WriteString("filler-command-with-some-length-to-grow-the-file\n")
	}
	builder.WriteString("recent-command\n")
	writeHistoryFile(t, home, ".bash_history", builder.String())
	registry := NewRegistry(Dependencies{LocalHistory: true})
	result := registry.Execute(context.Background(), "job", Scope{}, shellHistoryCall(1), nil)
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Text, "recent-command") {
		t.Fatalf("tail entry missing: %q", result.Text[:200])
	}
	if strings.Contains(result.Text, "ancient-command") {
		t.Fatalf("read beyond the 8 MiB tail cap: %q", result.Text[:200])
	}
}

func TestShellHistoryRejectsRemoteAssetScope(t *testing.T) {
	home := shellHistoryTestHome(t)
	const desktopHistory = "desktop-history-must-not-leak"
	writeHistoryFile(t, home, ".bash_history", desktopHistory+"\n")
	registry := NewRegistry(Dependencies{LocalHistory: true, ListAssets: func(context.Context) ([]Asset, error) {
		return []Asset{{ID: "a1", Kind: "ssh"}, {ID: "local-1", Kind: "local"}}, nil
	}})
	for _, assetID := range []string{"a1", "missing"} {
		result := registry.Execute(context.Background(), "job", Scope{AssetID: assetID}, shellHistoryCall(0), nil)
		if result.OK || !strings.Contains(result.Text, "exec_commands") || strings.Contains(result.Text, desktopHistory) {
			t.Fatalf("asset %q must fail closed with an exec alternative: %+v", assetID, result)
		}
	}
	local := registry.Execute(context.Background(), "job", Scope{AssetID: "local-1"}, shellHistoryCall(0), nil)
	if !local.OK || !strings.Contains(local.Text, desktopHistory) {
		t.Fatalf("local asset history = %+v", local)
	}
}

func TestShellHistoryServerModeRejectsDirectExecution(t *testing.T) {
	home := shellHistoryTestHome(t)
	writeHistoryFile(t, home, ".bash_history", "server-history-must-not-be-read\n")
	result := NewRegistry(Dependencies{}).Execute(context.Background(), "job", Scope{}, shellHistoryCall(0), nil)
	if result.OK || !strings.Contains(result.Text, "桌面模式") {
		t.Fatalf("server mode history = %+v", result)
	}
}

func TestShellHistoryRequiresConfirmation(t *testing.T) {
	home := shellHistoryTestHome(t)
	writeHistoryFile(t, home, ".bash_history", "ls\n")
	registry := NewRegistry(Dependencies{LocalHistory: true, Terminal: &fakeTerminal{assetKind: "local"}})
	readWrite := &Execution{
		Registry:   registry,
		Scope:      Scope{SessionID: "s"},
		Permission: guard.Config{Mode: guard.ReadWrite},
		Memory:     guard.NewMemory(),
	}
	if _, err := readWrite.initial(context.Background(), shellHistoryCall(0)); err == nil {
		t.Fatal("shell_history executed without confirmation")
	}
	readWrite.Memory.Add(guard.Kind(shellHistoryKind))
	result, err := readWrite.initial(context.Background(), shellHistoryCall(0))
	if err != nil || !result.OK {
		t.Fatalf("remembered confirmation should execute: result=%+v err=%v", result, err)
	}
	readOnly := &Execution{
		Registry:   registry,
		Scope:      Scope{SessionID: "s"},
		Permission: guard.Config{Mode: guard.ReadOnly},
		Memory:     guard.NewMemory(),
	}
	denied, err := readOnly.initial(context.Background(), shellHistoryCall(0))
	if err != nil || denied.OK || !strings.Contains(denied.Text, "权限策略已拒绝") {
		t.Fatalf("read-only should deny: result=%+v err=%v", denied, err)
	}
}

func TestShellHistoryRemoteSessionSuggestsExecWithoutConfirmation(t *testing.T) {
	home := shellHistoryTestHome(t)
	const desktopHistory = "remote-session-must-not-read-desktop-history"
	writeHistoryFile(t, home, ".bash_history", desktopHistory+"\n")
	registry := NewRegistry(Dependencies{LocalHistory: true, Terminal: &fakeTerminal{assetKind: "ssh"}})
	scope := Scope{SessionID: "s", TabID: "tab"}
	for _, mode := range []guard.Mode{guard.ReadWrite, guard.Silent} {
		execution := &Execution{
			Registry:   registry,
			Scope:      scope,
			Permission: guard.Config{Mode: mode},
			Memory:     guard.NewMemory(),
		}
		execution.Memory.Add(guard.Kind(shellHistoryKind))
		result, err := execution.initial(context.Background(), shellHistoryCall(0))
		if err != nil {
			t.Fatalf("mode %s asked for confirmation instead of rejecting the remote call: %v", mode, err)
		}
		if result.OK || !strings.Contains(result.Text, "exec_commands") || strings.Contains(result.Text, desktopHistory) {
			t.Fatalf("mode %s remote result = %+v", mode, result)
		}
	}
}

func TestShellHistoryKindResolutionFailsClosed(t *testing.T) {
	home := shellHistoryTestHome(t)
	const desktopHistory = "unknown-scope-must-not-read-desktop-history"
	writeHistoryFile(t, home, ".bash_history", desktopHistory+"\n")
	for _, test := range []struct {
		name     string
		terminal Terminal
	}{
		{name: "resolution error", terminal: &fakeTerminal{assetKindErr: errors.New("boom")}},
		{name: "missing kind capability", terminal: legacyTerminal{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry(Dependencies{LocalHistory: true, Terminal: test.terminal})
			result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, shellHistoryCall(0), nil)
			if result.OK || !strings.Contains(result.Text, "exec_commands") || strings.Contains(result.Text, desktopHistory) {
				t.Fatalf("unknown kind must fail closed: %+v", result)
			}
		})
	}
	result := NewRegistry(Dependencies{LocalHistory: true}).Execute(context.Background(), "job", Scope{}, shellHistoryCall(0), nil)
	if !result.OK || !strings.Contains(result.Text, desktopHistory) {
		t.Fatalf("session-less desktop scope should stay local: %+v", result)
	}
}

func TestShellHistoryLocalSessionUsesRememberedApproval(t *testing.T) {
	home := shellHistoryTestHome(t)
	writeHistoryFile(t, home, ".bash_history", "ls\n")
	registry := NewRegistry(Dependencies{LocalHistory: true, Terminal: &fakeTerminal{assetKind: "local"}})
	execution := &Execution{
		Registry:   registry,
		Scope:      Scope{SessionID: "s", TabID: "tab"},
		Permission: guard.Config{Mode: guard.ReadWrite},
		Memory:     guard.NewMemory(),
	}
	if _, err := execution.initial(context.Background(), shellHistoryCall(0)); err == nil {
		t.Fatal("first local call must ask")
	}
	execution.Memory.Add(guard.Kind(shellHistoryKind))
	result, err := execution.initial(context.Background(), shellHistoryCall(0))
	if err != nil || !result.OK {
		t.Fatalf("remembered local approval should execute: result=%+v err=%v", result, err)
	}
}
