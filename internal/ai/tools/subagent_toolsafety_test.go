package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/subagent"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

type safetyArgs struct {
	Value int `json:"value"`
}

func newSafetySubagent(t *testing.T, step func(call int, input []*schema.Message) *schema.Message, blocker func(context.Context) error) *subagent.Manager {
	t.Helper()
	failing, err := utils.InferTool("fail_tool", "returns a Go error", func(ctx context.Context, _ safetyArgs) (Output, error) {
		if blocker != nil {
			return Output{}, blocker(ctx)
		}
		return Output{}, errors.New("backend exploded")
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	chat := &scriptedModel{step: func(input []*schema.Message) (*schema.Message, error) {
		calls++
		return step(calls, input), nil
	}}
	manager, err := subagent.NewManager(subagent.Config{
		NewModel: func(context.Context) (model.BaseChatModel, error) { return chat, nil },
		NewTools: func(context.Context, subagent.Scope) ([]tool.BaseTool, error) { return []tool.BaseTool{failing}, nil },
		ToolSafety: func(allowed []string) (adk.ChatModelAgentMiddleware, func(context.Context, string, string) (string, error)) {
			return NewToolSafety(allowed)
		},
		MaxRunTime: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func toolCallTurn(id, name, arguments string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: arguments}}})
}

func TestSubagentToolErrorMalformedAndUnknownReachTheModel(t *testing.T) {
	var final []string
	manager := newSafetySubagent(t, func(call int, input []*schema.Message) *schema.Message {
		switch call {
		case 1:
			return toolCallTurn("a", "fail_tool", `{"value":1}`)
		case 2:
			return toolCallTurn("b", "fail_tool", `{"value":`)
		case 3:
			return toolCallTurn("c", "ghost", `{}`)
		}
		for _, message := range input {
			if message.Role == schema.Tool {
				final = append(final, message.Content)
			}
		}
		return schema.AssistantMessage("finished", nil)
	}, nil)
	handle, err := manager.Spawn(context.Background(), subagent.Request{Task: "go", Scope: &subagent.Scope{AllowedTools: []string{"fail_tool"}}})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := manager.Wait(waitCtx, handle)
	if err != nil || result.Status != subagent.StatusCompleted || result.Output != "finished" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(final) != 3 {
		t.Fatalf("model saw %d tool results: %v", len(final), final)
	}
	for index, want := range []string{"backend exploded", "JSON", "fail_tool"} {
		if !strings.Contains(final[index], `"ok":false`) || !strings.Contains(final[index], want) {
			t.Fatalf("result %d = %s", index, final[index])
		}
	}
}

func TestSubagentParentCancellationStillCancels(t *testing.T) {
	started := make(chan struct{})
	manager := newSafetySubagent(t, func(call int, _ []*schema.Message) *schema.Message {
		if call == 1 {
			return toolCallTurn("a", "fail_tool", `{"value":1}`)
		}
		return schema.AssistantMessage("should not get here", nil)
	}, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	handle, err := manager.Spawn(context.Background(), subagent.Request{Task: "go", Scope: &subagent.Scope{AllowedTools: []string{"fail_tool"}}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := manager.Cancel(handle); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, _ := manager.Wait(waitCtx, handle)
	if result.Status != subagent.StatusCanceled {
		t.Fatalf("status=%v result=%+v", result.Status, result)
	}
}
