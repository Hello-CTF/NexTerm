package subagent

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

func TestToolResultPanicMarkerPropagates(t *testing.T) {
	crashTool, err := utils.InferTool("crash_tool", "always crashes", func(context.Context, testArgs) (string, error) {
		return `{"ok":false,"text":"工具 crash_tool 执行时崩溃：后端不可用","exitCode":1,"panic":true}`, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	chat := &testModel{step: func(_ context.Context, _ []*schema.Message, call int) (*schema.Message, error) {
		if call == 1 {
			return toolCall("c1", "crash_tool"), nil
		}
		return schema.AssistantMessage("child recovered", nil), nil
	}}
	manager := timelineManager(t, chat, func(context.Context, Scope) ([]tool.BaseTool, error) {
		return []tool.BaseTool{crashTool}, nil
	})
	observer, recorded := eventRecorder()
	handle, err := manager.Spawn(context.Background(), Request{
		Task:     "child task",
		Scope:    &Scope{AllowedTools: []string{"crash_tool"}},
		Observer: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := waitResult(t, manager, handle)
	if err != nil {
		t.Fatalf("subagent failed: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %s, want %s", result.Status, StatusCompleted)
	}
	events := recorded()
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4: %+v", len(events), events)
	}
	toolResult := events[1]
	if toolResult.Kind != EventToolResult || toolResult.OK || !toolResult.Panic {
		t.Fatalf("toolResult event = %+v, want structured panic failure", toolResult)
	}
}
