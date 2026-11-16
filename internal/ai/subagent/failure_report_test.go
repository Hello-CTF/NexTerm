package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

func runSpawn(t *testing.T, manager *Manager, allowed ...string) SpawnOutput {
	t.Helper()
	spawnTool, err := NewSpawnTool(manager, Scope{AllowedTools: allowed})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := spawnTool.InvokableRun(ctx, `{"task":"child task"}`)
	if err != nil {
		t.Fatalf("subagent failure leaked to the caller as an error: %v", err)
	}
	var output SpawnOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatalf("spawn output %q is not a result envelope: %v", raw, err)
	}
	return output
}

func probeTool(t *testing.T) tool.InvokableTool {
	t.Helper()
	probe, err := utils.InferTool("probe", "inspect", func(context.Context, testArgs) (string, error) {
		return `{"ok":true,"text":"probe found 3 files"}`, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return probe
}

func TestSpawnToolReportsIterationLimitWithPartialOutput(t *testing.T) {
	chat := &testModel{step: func(_ context.Context, _ []*schema.Message, call int) (*schema.Message, error) {
		return schema.AssistantMessage(fmt.Sprintf("notes after step %d", call), []schema.ToolCall{
			{ID: fmt.Sprintf("probe-%d", call), Type: "function", Function: schema.FunctionCall{Name: "probe", Arguments: `{}`}},
		}), nil
	}}
	manager, err := NewManager(Config{
		NewModel:      func(context.Context) (model.BaseChatModel, error) { return chat, nil },
		NewTools:      func(context.Context, Scope) ([]tool.BaseTool, error) { return []tool.BaseTool{probeTool(t)}, nil },
		MaxIterations: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	output := runSpawn(t, manager, "probe")
	if output.OK || output.ExitCode == 0 {
		t.Fatalf("iteration limit reported as success: %+v", output)
	}
	for _, want := range []string{"step limit (2 steps)", "notes after step 2", "probe", "probe found 3 files"} {
		if !strings.Contains(output.ModelText, want) {
			t.Fatalf("model text missing %q:\n%s", want, output.ModelText)
		}
	}
	for _, want := range []string{"步数上限（2 步）", "notes after step 2", "probe found 3 files"} {
		if !strings.Contains(output.Text, want) {
			t.Fatalf("user text missing %q:\n%s", want, output.Text)
		}
	}
}

func TestSpawnToolReportsModelErrorAsFailedResult(t *testing.T) {
	chat := &testModel{step: func(_ context.Context, _ []*schema.Message, call int) (*schema.Message, error) {
		if call == 1 {
			return toolCall("probe-1", "probe"), nil
		}
		return nil, errors.New("provider returned 503")
	}}
	manager := timelineManager(t, chat, func(context.Context, Scope) ([]tool.BaseTool, error) {
		return []tool.BaseTool{probeTool(t)}, nil
	})

	output := runSpawn(t, manager, "probe")
	if output.OK {
		t.Fatalf("model error reported as success: %+v", output)
	}
	for _, want := range []string{"provider returned 503", "probe found 3 files", "status: failed"} {
		if !strings.Contains(output.ModelText, want) {
			t.Fatalf("model text missing %q:\n%s", want, output.ModelText)
		}
	}
}

func TestSpawnToolReportsChildTimeoutAsFailedResult(t *testing.T) {
	chat := &testModel{step: func(ctx context.Context, _ []*schema.Message, _ int) (*schema.Message, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	manager, err := NewManager(Config{
		NewModel:   func(context.Context) (model.BaseChatModel, error) { return chat, nil },
		MaxRunTime: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	output := runSpawn(t, manager)
	if output.OK || !strings.Contains(output.ModelText, "ran out of time") {
		t.Fatalf("child timeout not reported as a failed result: %+v", output)
	}
}

func TestSpawnToolReportsInterruptingToolAsFailedResult(t *testing.T) {
	interrupting, err := utils.InferTool("confirm_me", "needs the user", func(ctx context.Context, _ testArgs) (string, error) {
		return "", tool.StatefulInterrupt(ctx, "interaction required", "state")
	})
	if err != nil {
		t.Fatal(err)
	}
	chat := &testModel{step: func(_ context.Context, _ []*schema.Message, call int) (*schema.Message, error) {
		if call == 1 {
			return toolCall("confirm-1", "confirm_me"), nil
		}
		return schema.AssistantMessage("unexpected", nil), nil
	}}
	manager := timelineManager(t, chat, func(context.Context, Scope) ([]tool.BaseTool, error) {
		return []tool.BaseTool{interrupting}, nil
	})

	output := runSpawn(t, manager, "confirm_me")
	if output.OK || !strings.Contains(output.ModelText, "needed user interaction") {
		t.Fatalf("interrupting tool not reported as a failed result: %+v", output)
	}
}

func TestSpawnToolReportsUnavailableToolAsFailedResult(t *testing.T) {
	chat := &testModel{step: func(context.Context, []*schema.Message, int) (*schema.Message, error) {
		return schema.AssistantMessage("unused", nil), nil
	}}
	manager := timelineManager(t, chat, func(context.Context, Scope) ([]tool.BaseTool, error) {
		return []tool.BaseTool{probeTool(t)}, nil
	})
	spawnTool, err := NewSpawnTool(manager, Scope{AllowedTools: []string{"missing_tool"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := spawnTool.InvokableRun(context.Background(), `{"task":"child task"}`)
	if err != nil {
		t.Fatalf("unavailable tool leaked as an error: %v", err)
	}
	var output SpawnOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if output.OK || !strings.Contains(output.ModelText, "missing_tool") {
		t.Fatalf("unavailable scoped tool not reported as a failed result: %+v", output)
	}
}
