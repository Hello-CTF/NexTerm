package subagent

import (
	"context"
	"errors"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const SpawnToolName = "spawn_subagent"

type SpawnInput struct {
	Task    string `json:"task" jsonschema:"required"`
	Persona string `json:"persona,omitempty"`
}

func NewSpawnTool(manager *Manager, scope Scope) (tool.InvokableTool, error) {
	if manager == nil {
		return nil, errors.New("subagent manager is required")
	}
	normalized, _, err := normalizeScope(&scope)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(SpawnToolName, "Run one bounded isolated task using only the caller-configured tool scope; nested tasks are rejected.", func(ctx context.Context, input SpawnInput) (string, error) {
		handle, err := manager.Spawn(ctx, Request{Task: input.Task, Persona: input.Persona, Scope: &normalized})
		if errors.Is(err, ErrNestedSpawn) {
			return err.Error(), nil
		}
		if err != nil {
			return "", err
		}
		result, err := manager.Wait(ctx, handle)
		if err != nil {
			return "", err
		}
		return result.Output, nil
	})
}
