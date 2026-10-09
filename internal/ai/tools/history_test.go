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
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHistoryFile(t, home, ".zsh_history", ": 1700000000:0;ls -la\n: 1700000001:0;git status\n: 1700000002:0;make test\n")
	registry := NewRegistry(Dependencies{})
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHistoryFile(t, home, ".zsh_history", "ls -la\ngit status\nmake test\n")
	registry := NewRegistry(Dependencies{})
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHistoryFile(t, home, ".zsh_history", ": 1700000000:0;for i in 1 2; do\nplain-continuation\n: 1700000001:0;done\n")
	registry := NewRegistry(Dependencies{})
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHistoryFile(t, home, ".bash_history", "#1700000000\nls\necho hi\n#1700000001\ngit status\n")
	registry := NewRegistry(Dependencies{})
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHistoryFile(t, home, ".local/share/fish/fish_history", "- cmd: ls\n  when: 1700000000\n- cmd: make test\n  when: 1700000001\n")
	registry := NewRegistry(Dependencies{})
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHistoryFile(t, home, ".zsh_history", ": 1700000000:0;zsh-command\n")
	writeHistoryFile(t, home, ".bash_history", "bash-command\n")
	registry := NewRegistry(Dependencies{})
	result := registry.Execute(context.Background(), "job", Scope{}, shellHistoryCall(0), nil)
	if !result.OK || !strings.Contains(result.Text, "zsh") || strings.Contains(result.Text, "bash-command") {
		t.Fatalf("text=%q", result.Text)
	}
}

func TestShellHistoryLimitClampsToNewest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHistoryFile(t, home, ".bash_history", "one\ntwo\nthree\nfour\n")
	registry := NewRegistry(Dependencies{})
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHistoryFile(t, home, ".bash_history", "curl -H \"Authorization: Bearer tok-123\" https://x\nmysql -psecretpw db\nexport password=hunter2\n")
	registry := NewRegistry(Dependencies{})
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	registry := NewRegistry(Dependencies{})
	result := registry.Execute(context.Background(), "job", Scope{}, shellHistoryCall(0), nil)
	if result.OK || !strings.Contains(result.Text, "未找到本机 shell 历史文件") ||
		!strings.Contains(result.Text, ".zsh_history") || !strings.Contains(result.Text, ".bash_history") || !strings.Contains(result.Text, "fish_history") {
		t.Fatalf("result=%+v", result)
	}
}

func TestShellHistoryReadsOnlyTailOfLargeFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var builder strings.Builder
	builder.WriteString("ancient-command\n")
	for i := 0; i < 300000; i++ {
		builder.WriteString("filler-command-with-some-length-to-grow-the-file\n")
	}
	builder.WriteString("recent-command\n")
	writeHistoryFile(t, home, ".bash_history", builder.String())
	registry := NewRegistry(Dependencies{})
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

func TestShellHistoryLabelsRemoteScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHistoryFile(t, home, ".bash_history", "ls\n")
	registry := NewRegistry(Dependencies{ListAssets: func(context.Context) ([]Asset, error) {
		return []Asset{{ID: "a1", Kind: "ssh"}, {ID: "local-1", Kind: "local"}}, nil
	}})
	result := registry.Execute(context.Background(), "job", Scope{AssetID: "a1"}, shellHistoryCall(0), nil)
	if !result.OK || !strings.Contains(result.Text, "远端资产") || !strings.Contains(result.Text, "本机") {
		t.Fatalf("text=%q", result.Text)
	}
	local := registry.Execute(context.Background(), "job", Scope{AssetID: "local-1"}, shellHistoryCall(0), nil)
	if !local.OK || strings.Contains(local.Text, "远端资产") {
		t.Fatalf("local text=%q", local.Text)
	}
	// 找不到的 AssetID fail closed 按远端处理
	unknown := registry.Execute(context.Background(), "job", Scope{AssetID: "missing"}, shellHistoryCall(0), nil)
	if !unknown.OK || !strings.Contains(unknown.Text, "远端资产") {
		t.Fatalf("unknown asset must fail closed: %q", unknown.Text)
	}
}

func TestShellHistoryRequiresConfirmation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHistoryFile(t, home, ".bash_history", "ls\n")
	registry := NewRegistry(Dependencies{Terminal: &fakeTerminal{}})
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

func TestShellHistoryRemoteScopeForcesConfirmEvenWhenRemembered(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHistoryFile(t, home, ".bash_history", "ls\n")
	registry := NewRegistry(Dependencies{ListAssets: func(context.Context) ([]Asset, error) {
		return []Asset{{ID: "a1", Kind: "ssh"}}, nil
	}})
	execution := &Execution{
		Registry:   registry,
		Scope:      Scope{SessionID: "s", AssetID: "a1"},
		Permission: guard.Config{Mode: guard.ReadWrite},
		Memory:     guard.NewMemory(),
	}
	execution.Memory.Add(guard.Kind(shellHistoryKind))
	if _, err := execution.initial(context.Background(), shellHistoryCall(0)); err == nil {
		t.Fatal("remote scope must confirm shell_history even with remembered approval")
	}
}

func TestShellHistoryRemoteSessionForcesConfirmWithoutAssetID(t *testing.T) {
	// 真实侧边栏只传 sessionId/tabId：远端判定必须走服务端 session asset kind。
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHistoryFile(t, home, ".bash_history", "ls\n")
	terminal := &fakeTerminal{assetKind: "ssh"}
	registry := NewRegistry(Dependencies{Terminal: terminal})
	scope := Scope{SessionID: "s", TabID: "tab"}

	readWrite := &Execution{
		Registry:   registry,
		Scope:      scope,
		Permission: guard.Config{Mode: guard.ReadWrite},
		Memory:     guard.NewMemory(),
	}
	if _, err := readWrite.initial(context.Background(), shellHistoryCall(0)); err == nil {
		t.Fatal("remote session must ask before shell_history")
	}
	readWrite.Memory.Add(guard.Kind(shellHistoryKind))
	if _, err := readWrite.initial(context.Background(), shellHistoryCall(0)); err == nil {
		t.Fatal("remote session must re-ask shell_history even with remembered approval")
	}
	silent := &Execution{
		Registry:   registry,
		Scope:      scope,
		Permission: guard.Config{Mode: guard.Silent},
		Memory:     guard.NewMemory(),
	}
	if _, err := silent.initial(context.Background(), shellHistoryCall(0)); err == nil {
		t.Fatal("remote session must ask shell_history even in silent mode")
	}
	result := registry.Execute(context.Background(), "job", scope, shellHistoryCall(0), nil)
	if !result.OK || !strings.Contains(result.Text, "远端资产") {
		t.Fatalf("remote label missing: %+v", result)
	}
}

func TestShellHistoryKindResolutionFailsClosed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHistoryFile(t, home, ".bash_history", "ls\n")
	// kind 解析失败按远端处理
	registry := NewRegistry(Dependencies{Terminal: &fakeTerminal{assetKindErr: errors.New("boom")}})
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, shellHistoryCall(0), nil)
	if !result.OK || !strings.Contains(result.Text, "远端资产") {
		t.Fatalf("resolution failure must fail closed: %+v", result)
	}
	// 有会话但终端不支持 kind 解析：同样按远端处理
	registry = NewRegistry(Dependencies{Terminal: legacyTerminal{}})
	result = registry.Execute(context.Background(), "job", Scope{SessionID: "s", TabID: "tab"}, shellHistoryCall(0), nil)
	if !result.OK || !strings.Contains(result.Text, "远端资产") {
		t.Fatalf("missing kind capability must fail closed: %+v", result)
	}
	// 无会话上下文的纯本机对话：本机标注，无远端警告
	result = registry.Execute(context.Background(), "job", Scope{}, shellHistoryCall(0), nil)
	if !result.OK || strings.Contains(result.Text, "远端资产") {
		t.Fatalf("session-less scope should stay local: %+v", result)
	}
}

func TestShellHistoryLocalSessionUsesRememberedApproval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHistoryFile(t, home, ".bash_history", "ls\n")
	registry := NewRegistry(Dependencies{Terminal: &fakeTerminal{assetKind: "local"}})
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
	if strings.Contains(result.Text, "远端资产") {
		t.Fatalf("local session mislabeled: %q", result.Text)
	}
}
