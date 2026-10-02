package agent

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
)

func TestRealProviderAcceptance(t *testing.T) {
	if os.Getenv("NEXTERM_AI_ACCEPTANCE_PROVIDER") != "1" {
		t.Skip("set NEXTERM_AI_ACCEPTANCE_PROVIDER=1 and provider environment to run")
	}
	baseURL, modelName := os.Getenv("NEXTERM_AI_BASE_URL"), os.Getenv("NEXTERM_AI_MODEL")
	if baseURL == "" || modelName == "" {
		t.Fatal("NEXTERM_AI_BASE_URL and NEXTERM_AI_MODEL are required")
	}
	client, err := provider.NewClient(provider.Config{BaseURL: baseURL, APIKey: os.Getenv("NEXTERM_AI_API_KEY"), Model: modelName, Temperature: 0, ContextWindow: 32768, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := any(client).(baseChatModelClient)
	if !ok {
		t.Fatal("provider does not expose the final Eino BaseChatModel factory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	chatModel, contextWindow, err := adapter.BaseChatModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return chatModel, contextWindow, nil },
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage, MaxTurns: 2,
	})
	defer runner.Close()
	stream := &SliceStream{}
	_, err = runner.Start(ctx, ChatArgs{Message: "这是一次验收测试。不要调用任何工具，只回复 OK。"}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	events := waitClosedTimeout(t, stream, 90*time.Second)
	done, failed := terminalCounts(events)
	if done != 1 || failed != 0 {
		t.Fatalf("real provider terminal events: done=%d error=%d events=%+v", done, failed, events)
	}
}
