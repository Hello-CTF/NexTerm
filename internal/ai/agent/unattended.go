package agent

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/components/tool"
)

func executionTools(ctx context.Context, execution *tools.Execution) ([]tool.BaseTool, error) {
	if !guard.UnattendedFrom(ctx) {
		return execution.Tools()
	}
	if execution.Subagents != nil {
		cloned := *execution.Subagents
		if len(cloned.AllowedTools) == 0 {
			probe := *execution
			probe.Subagents = nil
			available, err := probe.Tools()
			if err != nil {
				return nil, err
			}
			available, err = withoutTerminalCreate(ctx, available)
			if err != nil {
				return nil, err
			}
			cloned.AllowedTools, err = baseToolNames(ctx, available)
			if err != nil {
				return nil, err
			}
		} else {
			cloned.AllowedTools = withoutToolName(cloned.AllowedTools, "terminal_create")
		}
		if len(cloned.AllowedTools) == 0 {
			execution.Subagents = nil
		} else {
			execution.Subagents = &cloned
		}
	}
	available, err := execution.Tools()
	if err != nil {
		return nil, err
	}
	return withoutTerminalCreate(ctx, available)
}

func withoutTerminalCreate(ctx context.Context, available []tool.BaseTool) ([]tool.BaseTool, error) {
	filtered := make([]tool.BaseTool, 0, len(available))
	for _, candidate := range available {
		info, err := candidate.Info(ctx)
		if err != nil {
			return nil, err
		}
		if info == nil {
			return nil, errors.New("AI 工具返回空描述")
		}
		if info.Name != "terminal_create" {
			filtered = append(filtered, candidate)
		}
	}
	return filtered, nil
}

func baseToolNames(ctx context.Context, available []tool.BaseTool) ([]string, error) {
	names := make([]string, 0, len(available))
	for _, candidate := range available {
		info, err := candidate.Info(ctx)
		if err != nil {
			return nil, err
		}
		if info == nil {
			return nil, errors.New("AI 工具返回空描述")
		}
		names = append(names, info.Name)
	}
	return names, nil
}

func withoutToolName(names []string, excluded string) []string {
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if name != excluded {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

func systemPromptFor(ctx context.Context, planMode bool) string {
	prompt := systemPrompt()
	if planMode {
		prompt = planSystemPrompt()
	}
	if guard.UnattendedFrom(ctx) {
		prompt += "\nUnattended execution: do not call terminal_create or revive old tabs; use exec_commands for shell commands in the already-connected session."
	}
	return prompt
}
