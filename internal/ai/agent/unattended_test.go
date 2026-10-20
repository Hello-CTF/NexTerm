package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type unattendedCreateTerminal struct{}

func (unattendedCreateTerminal) Snapshot(context.Context, string) (tools.Screen, error) {
	return tools.Screen{}, nil
}

func (unattendedCreateTerminal) Write(context.Context, string, []byte) error { return nil }

func (unattendedCreateTerminal) Create(context.Context, string, tools.CreateTerminalOptions) (tools.CreatedTab, error) {
	return tools.CreatedTab{TabID: "new-tab"}, nil
}

func TestExecutionToolsUnattendedExcludeTerminalCreate(t *testing.T) {
	newExecution := func() *tools.Execution {
		return &tools.Execution{
			Registry: tools.NewRegistry(tools.Dependencies{
				Terminal: unattendedCreateTerminal{},
				Transport: func(context.Context, string) (base.Transport, error) {
					return nil, nil
				},
			}),
			Scope: tools.Scope{SessionID: "session"},
			Subagents: &tools.SubagentConfig{
				Model: func(context.Context) (model.BaseChatModel, error) {
					return sequenceModel(schema.AssistantMessage("done", nil)), nil
				},
			},
		}
	}
	interactive := newExecution()
	interactiveTools, err := executionTools(context.Background(), interactive)
	if err != nil {
		t.Fatal(err)
	}
	interactiveNames, err := baseToolNames(context.Background(), interactiveTools)
	if err != nil {
		t.Fatal(err)
	}
	if !containsToolName(interactiveNames, "terminal_create") {
		t.Fatalf("interactive tools lost terminal_create: %v", interactiveNames)
	}
	if interactive.SubagentManager != nil {
		_ = interactive.SubagentManager.Close()
	}

	unattendedCtx := guard.WithUnattended(context.Background())
	unattended := newExecution()
	unattendedTools, err := executionTools(unattendedCtx, unattended)
	if err != nil {
		t.Fatal(err)
	}
	if unattended.SubagentManager != nil {
		t.Cleanup(func() { _ = unattended.SubagentManager.Close() })
	}
	unattendedNames, err := baseToolNames(unattendedCtx, unattendedTools)
	if err != nil {
		t.Fatal(err)
	}
	if containsToolName(unattendedNames, "terminal_create") {
		t.Fatalf("unattended tools include terminal_create: %v", unattendedNames)
	}
	if !containsToolName(unattendedNames, "exec_commands") {
		t.Fatalf("unattended tools lost exec_commands: %v", unattendedNames)
	}
	if containsToolName(unattended.Subagents.AllowedTools, "terminal_create") ||
		!containsToolName(unattended.Subagents.AllowedTools, "exec_commands") {
		t.Fatalf("unattended subagent tools = %v", unattended.Subagents.AllowedTools)
	}

	prompt := systemPromptFor(unattendedCtx, false)
	if !strings.Contains(prompt, "do not call terminal_create") || !strings.Contains(prompt, "use exec_commands") {
		t.Fatalf("unattended prompt lacks terminal guidance: %q", prompt)
	}
	if strings.Contains(systemPromptFor(context.Background(), false), "Unattended execution") {
		t.Fatal("interactive prompt gained unattended guidance")
	}
}

func containsToolName(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}
