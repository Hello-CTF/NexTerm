package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestExecutionToolsFollowScopeCapabilities(t *testing.T) {
	toolNames := func(execution *Execution) map[string]bool {
		t.Helper()
		einoTools, err := execution.Tools()
		if err != nil {
			t.Fatal(err)
		}
		names := make(map[string]bool, len(einoTools))
		for _, current := range einoTools {
			info, err := current.Info(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			names[info.Name] = true
		}
		return names
	}
	deps := Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	names := toolNames(&Execution{JobID: "j", Registry: NewRegistry(deps), Scope: Scope{SessionID: "s"}})
	if len(names) != 3 || !names["docker_control"] || !names["ask_user"] || !names["todo_write"] || names["shell_history"] {
		t.Fatalf("names=%v", names)
	}
	for _, unavailable := range []string{"exec_commands", "read_file", "read_screen", "docker_ps", "docker_logs", "docker_exec", "db_query", "list_assets", "exit_plan_mode"} {
		if names[unavailable] {
			t.Errorf("%s exposed without capability: %v", unavailable, names)
		}
	}
	deps.ListAssets = func(context.Context) ([]Asset, error) { return nil, nil }
	names = toolNames(&Execution{JobID: "j", Registry: NewRegistry(deps), Scope: Scope{SessionID: "s"}, PlanMode: true})
	if len(names) != 4 || !names["list_assets"] || !names["ask_user"] || !names["todo_write"] || !names["exit_plan_mode"] || names["docker_control"] || names["shell_history"] {
		t.Fatalf("plan-mode names=%v", names)
	}
	mixed := NewRegistry(Dependencies{Terminal: &fakeTerminal{}, TabSession: func(string) string { return "other" }})
	names = toolNames(&Execution{JobID: "j", Registry: mixed, Scope: Scope{SessionID: "s", TabID: "t"}})
	for _, available := range []string{"read_screen", "send_keys", "wait_for"} {
		if !names[available] {
			t.Errorf("%s missing before request-aware execution check: %v", available, names)
		}
	}
	result := mixed.Execute(context.Background(), "j", Scope{SessionID: "s", TabID: "t"}, Call{ID: "r", Name: "read_screen"}, nil)
	if result.OK || !strings.Contains(result.Text, ErrCrossScope.Error()) {
		t.Fatalf("mixed scope execution = %+v", result)
	}
}

func TestShellHistoryEnabledOnlyForDesktopLocalScope(t *testing.T) {
	for _, test := range []struct {
		name     string
		desktop  bool
		scope    Scope
		terminal Terminal
		assets   []Asset
		want     bool
	}{
		{name: "server no scope", desktop: false, scope: Scope{}, want: false},
		{name: "server local scope", desktop: false, scope: Scope{SessionID: "s"}, terminal: &fakeTerminal{assetKind: "local"}, want: false},
		{name: "desktop no scope", desktop: true, scope: Scope{}, want: true},
		{name: "desktop local asset", desktop: true, scope: Scope{AssetID: "local"}, assets: []Asset{{ID: "local", Kind: "local"}}, want: true},
		{name: "desktop local session", desktop: true, scope: Scope{SessionID: "s"}, terminal: &fakeTerminal{assetKind: "local"}, want: true},
		{name: "desktop remote ssh session", desktop: true, scope: Scope{SessionID: "s"}, terminal: &fakeTerminal{assetKind: "ssh"}, want: false},
		{name: "desktop remote device session", desktop: true, scope: Scope{SessionID: "s"}, terminal: &fakeTerminal{assetKind: "device"}, want: false},
		{name: "desktop remote ssh asset", desktop: true, scope: Scope{AssetID: "ssh"}, assets: []Asset{{ID: "ssh", Kind: "ssh"}}, want: false},
		{name: "desktop unresolved session", desktop: true, scope: Scope{SessionID: "s"}, terminal: legacyTerminal{}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			deps := Dependencies{LocalHistory: test.desktop, Terminal: test.terminal}
			if test.assets != nil {
				deps.ListAssets = func(context.Context) ([]Asset, error) { return test.assets, nil }
			}
			execution := &Execution{Registry: NewRegistry(deps), Scope: test.scope}
			if got := execution.enabled("shell_history"); got != test.want {
				t.Fatalf("enabled = %v, want %v", got, test.want)
			}
			available, err := execution.Tools()
			if err != nil {
				t.Fatal(err)
			}
			advertised := false
			for _, candidate := range available {
				info, err := candidate.Info(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if info.Name == "shell_history" {
					advertised = true
				}
			}
			if advertised != test.want {
				t.Fatalf("advertised = %v, want %v", advertised, test.want)
			}
		})
	}
}

func TestShellHistoryDescriptionIdentifiesDesktopUserAndRemoteAlternative(t *testing.T) {
	description := func(execution *Execution, name string) (string, bool) {
		t.Helper()
		available, err := execution.Tools()
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range available {
			info, err := candidate.Info(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if info.Name == name {
				return info.Desc, true
			}
		}
		return "", false
	}
	local := &Execution{Registry: NewRegistry(Dependencies{LocalHistory: true})}
	localDescription, ok := description(local, "shell_history")
	if !ok {
		t.Fatal("shell_history was not advertised for a desktop scope without a remote target")
	}
	for _, expected := range []string{"OS user", "current NexTerm desktop process", "remote SSH/device", "exec_commands"} {
		if !strings.Contains(localDescription, expected) {
			t.Fatalf("description %q does not contain %q", localDescription, expected)
		}
	}

	remote := &Execution{
		Registry: NewRegistry(Dependencies{
			LocalHistory: true,
			Terminal:     &fakeTerminal{assetKind: "ssh"},
			Transport: func(context.Context, string) (base.Transport, error) {
				return nil, nil
			},
		}),
		Scope: Scope{SessionID: "s"},
	}
	if _, ok := description(remote, "shell_history"); ok {
		t.Fatal("shell_history was advertised for a remote SSH scope")
	}
	execDescription, ok := description(remote, "exec_commands")
	if !ok {
		t.Fatal("exec_commands was not advertised for a remote SSH scope")
	}
	for _, expected := range []string{"remote SSH/device", "shell history", "shell_history"} {
		if !strings.Contains(execDescription, expected) {
			t.Fatalf("remote exec description %q does not contain %q", execDescription, expected)
		}
	}
}

func TestNotifyRunsWithoutConfirmation(t *testing.T) {
	for _, mode := range []guard.Mode{guard.ReadWrite, guard.ReadOnly, guard.Unattended} {
		t.Run(string(mode), func(t *testing.T) {
			var delivered []string
			execution := &Execution{
				JobID: "job-notify",
				Registry: NewRegistry(Dependencies{Notify: func(_ context.Context, text string) error {
					delivered = append(delivered, text)
					return nil
				}}),
				Permission: guard.Config{Mode: mode},
				Memory:     guard.NewMemory(),
			}
			einoTools, err := execution.Tools()
			if err != nil {
				t.Fatal(err)
			}
			node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{Tools: einoTools, ExecuteSequentially: true})
			if err != nil {
				t.Fatal(err)
			}
			message := schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Function: schema.FunctionCall{Name: NotifyTool, Arguments: `{"message":"构建完成"}`}}})
			messages, err := node.Invoke(context.Background(), message)
			if err != nil {
				t.Fatalf("notify must not require confirmation: %v", err)
			}
			if len(delivered) != 1 || delivered[0] != "构建完成" {
				t.Fatalf("delivered=%v", delivered)
			}
			if len(messages) != 1 || !strings.Contains(messages[0].Content, `"ok":true`) {
				t.Fatalf("tool result = %+v", messages)
			}
		})
	}
}
