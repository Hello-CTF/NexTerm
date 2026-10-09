package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/subagent"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestSubagentsConfigPassesThroughToExecution(t *testing.T) {
	advertised := func(t *testing.T, subagents *tools.SubagentConfig) map[string]bool {
		t.Helper()
		var mu sync.Mutex
		seen := map[string]bool{}
		chat := &fakeModel{}
		chat.stream = func(_ context.Context, _ []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			mu.Lock()
			for _, info := range model.GetCommonOptions(nil, options...).Tools {
				seen[info.Name] = true
			}
			mu.Unlock()
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
		}
		storage, err := store.OpenInMemory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer storage.Close()
		runner := NewRunner(Config{
			Model:     func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
			Tools:     tools.NewRegistry(tools.Dependencies{}),
			Store:     storage,
			Subagents: subagents,
		})
		defer runner.Close()
		stream := &SliceStream{}
		startTestJob(t, runner, stream, "go")
		events := waitClosed(t, stream)
		if done, failed := terminalCounts(events); done != 1 || failed != 0 {
			t.Fatalf("terminal counts done=%d error=%d events=%+v", done, failed, events)
		}
		return seen
	}

	withConfig := advertised(t, &tools.SubagentConfig{Model: func(context.Context) (model.BaseChatModel, error) {
		return sequenceModel(schema.AssistantMessage("child done", nil)), nil
	}})
	if !withConfig[subagent.SpawnToolName] {
		t.Fatalf("spawn tool not advertised with Subagents config, tools = %v", withConfig)
	}
	without := advertised(t, nil)
	if without[subagent.SpawnToolName] {
		t.Fatalf("spawn tool advertised without Subagents config, tools = %v", without)
	}
}
