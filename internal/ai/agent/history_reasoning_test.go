package agent

import (
	"context"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/schema"
)

func TestRebuildHistoryRestoresToolCallReasoning(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
	}{{"with interim text", "checking"}, {"without text", ""}} {
		t.Run(test.name, func(t *testing.T) {
			storage, err := store.OpenInMemory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = storage.Close() })
			conversation, err := storage.ConvCreate(context.Background(), "t", nil)
			if err != nil {
				t.Fatal(err)
			}
			runner := &Runner{store: storage}
			current := &job{id: "job-1", args: ChatArgs{ConversationID: conversation.ID}}
			calls := []schema.ToolCall{namedToolCall("c1", "a", `{}`), namedToolCall("c2", "b", `{}`)}
			if err := runner.persistToolCallRows(context.Background(), current, test.text, "thought", calls); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"c1", "c2"} {
				message := &schema.Message{Role: schema.Tool, ToolCallID: id, ToolName: "a"}
				if err := runner.persistToolResultRows(context.Background(), current, message, tools.Output{OK: true, Text: "ok"}, "ok"); err != nil {
					t.Fatal(err)
				}
			}
			rows, err := storage.MsgList(context.Background(), conversation.ID)
			if err != nil {
				t.Fatal(err)
			}
			messages := rebuildHistory(rows, "other-job")
			if len(messages) != 3 || messages[0].ReasoningContent != "thought" || len(messages[0].ToolCalls) != 2 || messages[0].Content != test.text {
				t.Fatalf("messages = %+v", messages)
			}
		})
	}
}
