package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
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
	if len(names) != 4 || !names["docker_control"] || !names["ask_user"] || !names["todo_write"] || !names["shell_history"] {
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
	for _, unavailable := range []string{"read_screen", "send_keys", "wait_for"} {
		if names[unavailable] {
			t.Errorf("%s exposed in mixed scope: %v", unavailable, names)
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
