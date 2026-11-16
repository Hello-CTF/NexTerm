package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/steer"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// The steer handler runs before compaction, so a delivered steer is part of
// what compaction measures, and the steer is saved exactly once.
func TestSteerHandlerRunsBeforeCompactionAndPersistsOnce(t *testing.T) {
	ctx := context.Background()
	storage, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	runner := NewRunner(Config{Store: storage})
	t.Cleanup(func() { _ = runner.Close() })
	conversation, err := storage.ConvCreate(ctx, "steer", nil)
	if err != nil {
		t.Fatal(err)
	}
	current := &job{id: "job-steer-order", ctx: ctx, steer: steer.NewQueue(2), args: ChatArgs{ConversationID: conversation.ID}}
	if err := current.steer.Push(schema.UserMessage(strings.Repeat("x", 1000))); err != nil {
		t.Fatal(err)
	}

	const window = 1000 // 3000 byte budget: only the steer pushes the input over
	compactions := 0
	compaction := newCompactionHandler(nil, window, func() { compactions++ })
	handlers := []adk.ChatModelAgentMiddleware{
		&steerHandler{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, runner: runner, current: current, runtime: &einoRuntime{}},
		compaction,
	}
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.SystemMessage("sys"), schema.UserMessage("start"),
		schema.AssistantMessage("", []schema.ToolCall{{ID: "c1", Type: "function", Function: schema.FunctionCall{Name: "t", Arguments: "{}"}}}),
		{Role: schema.Tool, ToolCallID: "c1", Content: strings.Repeat("o", 2500)},
	}}
	for _, handler := range handlers {
		var err error
		if ctx, state, err = handler.BeforeModelRewriteState(ctx, state, nil); err != nil {
			t.Fatal(err)
		}
	}
	if compactions != 1 {
		t.Fatalf("compaction did not see the delivered steer: compactions = %d", compactions)
	}
	if last := state.Messages[len(state.Messages)-1]; last.Role != schema.User || !strings.HasPrefix(last.Content, "xxx") {
		t.Fatalf("steer missing from the final model input: %d messages", len(state.Messages))
	}

	// A second model call has nothing left to deliver.
	if _, _, err := handlers[0].BeforeModelRewriteState(ctx, state, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := storage.MsgList(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	steered := 0
	for _, row := range rows {
		if strings.Contains(row.ContentJSON, `"steered":true`) {
			steered++
		}
	}
	if steered != 1 {
		t.Fatalf("persisted steer rows = %d, want 1", steered)
	}
}
