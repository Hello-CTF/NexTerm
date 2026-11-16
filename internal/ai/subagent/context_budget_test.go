package subagent

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/compact"
	"github.com/Hello-CTF/NexTerm/internal/ai/steer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

func TestSubagentBoundsContextAcrossLargeToolResults(t *testing.T) {
	bigTool, err := utils.InferTool("big_tool", "returns a large result", func(context.Context, testArgs) (string, error) {
		return strings.Repeat("r", 2000), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	const window = 1000 // 3000 bytes of budget, less than three tool results
	const rounds = 6
	chat := &testModel{step: func(_ context.Context, _ []*schema.Message, call int) (*schema.Message, error) {
		if call <= rounds {
			return toolCall("c"+string(rune('0'+call)), "big_tool"), nil
		}
		return schema.AssistantMessage("finished", nil), nil
	}}
	manager, err := NewManager(Config{
		NewModel:      func(context.Context) (model.BaseChatModel, error) { return chat, nil },
		NewTools:      func(context.Context, Scope) ([]tool.BaseTool, error) { return []tool.BaseTool{bigTool}, nil },
		MaxIterations: rounds + 2,
		ContextWindow: window,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	handle, err := manager.Spawn(context.Background(), Request{Task: "scan", Scope: &Scope{AllowedTools: []string{"big_tool"}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := waitResult(t, manager, handle)
	if err != nil {
		t.Fatalf("subagent failed: %v (%s)", err, result.Error)
	}
	if result.Output != "finished" {
		t.Fatalf("output = %q", result.Output)
	}
	inputs := chat.Inputs()
	if len(inputs) != rounds+1 {
		t.Fatalf("model calls = %d, want %d", len(inputs), rounds+1)
	}
	for index, input := range inputs {
		if size := compact.EstimatedMessages(input); size > window*3 {
			t.Fatalf("call %d input is %d bytes, over the %d byte budget", index+1, size, window*3)
		}
		if err := steer.ValidateHistory(input); err != nil {
			t.Fatalf("call %d broke tool pairing: %v", index+1, err)
		}
	}
}
