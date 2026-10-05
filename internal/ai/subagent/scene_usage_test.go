package subagent

import (
	"context"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestSubagentAccumulatesUsageAndTurns(t *testing.T) {
	chat := &testModel{step: func(_ context.Context, _ []*schema.Message, call int) (*schema.Message, error) {
		message := schema.AssistantMessage("OK", nil)
		message.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 100, CompletionTokens: 40}}
		message.Extra = map[string]any{provider.MessageExtraCacheCreation: uint64(7)}
		return message, nil
	}}
	manager, err := NewManager(Config{
		NewModel:   func(context.Context) (model.BaseChatModel, error) { return chat, nil },
		MaxRunTime: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	handle, err := manager.Spawn(context.Background(), Request{Task: "measure me", Scope: &Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := waitResult(t, manager, handle)
	if err != nil {
		t.Fatal(err)
	}
	if result.Turns != 1 {
		t.Fatalf("turns = %d, want 1", result.Turns)
	}
	if result.Usage.PromptTokens != 100 || result.Usage.CompletionTokens != 40 || result.Usage.CacheCreationTokens != 7 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.Usage.LatencyMS < 0 {
		t.Fatalf("latency = %d", result.Usage.LatencyMS)
	}
}
