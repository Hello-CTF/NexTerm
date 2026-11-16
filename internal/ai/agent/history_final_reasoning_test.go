package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/usage"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestFinalAndPartialAnswerReasoningReplays(t *testing.T) {
	ctx := context.Background()
	storage, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	conversation, err := storage.ConvCreate(ctx, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{store: storage}

	// An old-format row without reasoning rebuilds unchanged.
	if err := storage.MsgInsert(ctx, conversation.ID, "user", map[string]any{"role": "user", "content": "q0"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := storage.MsgInsert(ctx, conversation.ID, "assistant", map[string]any{"role": "assistant", "content": "old", "jobId": "j0"}, nil, nil); err != nil {
		t.Fatal(err)
	}

	final := &job{id: "j1", ctx: ctx, args: ChatArgs{ConversationID: conversation.ID}}
	final.recordStreamedReasoning("final-thought")
	final.recordStreamed("answer")
	if err := runner.persistFinalAnswer(final, "answer", usage.Usage{}); err != nil {
		t.Fatal(err)
	}
	if text, reasoning := final.takeStreamedWithReasoning(); text != "" || reasoning != "" {
		t.Fatalf("streamed buffers not cleared: %q %q", text, reasoning)
	}

	partial := &job{id: "j2", ctx: ctx, args: ChatArgs{ConversationID: conversation.ID}}
	partial.recordStreamedReasoning("partial-thought")
	partial.recordStreamed("cut off")
	runner.persistPartialAnswer(partial)

	rows, err := storage.MsgList(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	messages := rebuildHistory(rows, "next-job")
	if len(messages) != 4 {
		t.Fatalf("messages = %+v", messages)
	}
	if messages[1].Content != "old" || messages[1].ReasoningContent != "" {
		t.Fatalf("old row changed: %+v", messages[1])
	}
	if messages[2].Content != "answer" || messages[2].ReasoningContent != "final-thought" {
		t.Fatalf("final row: %+v", messages[2])
	}
	if messages[3].ReasoningContent != "partial-thought" || !strings.HasPrefix(messages[3].Content, "cut off") {
		t.Fatalf("partial row: %+v", messages[3])
	}
}
