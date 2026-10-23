package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func compactTestRunner(t *testing.T, summary string) (*Runner, *store.Store, *compactionModel) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	chat := &compactionModel{generate: func(context.Context, []*schema.Message) (*schema.Message, error) {
		return schema.AssistantMessage(summary, nil), nil
	}}
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 200000, nil },
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage, MaxTurns: 2,
	})
	t.Cleanup(func() { _ = runner.Close() })
	return runner, storage, chat
}

func seedConversation(t *testing.T, storage *store.Store, turns int) string {
	t.Helper()
	ctx := context.Background()
	row, err := storage.ConvCreate(ctx, "排查", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < turns; i++ {
		if err := storage.MsgInsert(ctx, row.ID, "user", map[string]any{"role": "user", "content": "旧问题"}, nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := storage.MsgInsert(ctx, row.ID, "assistant", map[string]any{"role": "assistant", "content": "旧回答"}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	return row.ID
}

func TestCompactConversationPersistsASummaryThatReplacesEarlierHistory(t *testing.T) {
	runner, storage, chat := compactTestRunner(t, "已在 web-01 上重启 nginx，下一步检查日志")
	conversationID := seedConversation(t, storage, 3)

	result, err := runner.CompactConversation(context.Background(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Messages != 6 {
		t.Fatalf("result = %+v, want the six history messages summarized", result)
	}
	inputs := chat.snapshotGenerateInputs()
	if len(inputs) == 0 || !strings.Contains(flattenMessages(inputs[0]), "latest request") {
		t.Fatalf("summary call did not use the handoff instruction: %v", inputs)
	}

	if err := storage.MsgInsert(context.Background(), conversationID, "user", map[string]any{"role": "user", "content": "新问题"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := storage.MsgList(context.Background(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	history := historyMessages(rows, "")
	if len(history) != 2 || !strings.HasPrefix(history[0].Content, "[History Summary]\n") ||
		!strings.Contains(history[0].Content, "已在 web-01 上重启 nginx") || strings.Contains(history[0].Content, "旧问题") || history[1].Content != "新问题" {
		t.Fatalf("history after compact = %+v, want the summary followed by the new message", history)
	}
}

func TestCompactConversationRefusesShortOrBusyConversations(t *testing.T) {
	runner, storage, _ := compactTestRunner(t, "摘要")
	if _, err := runner.CompactConversation(context.Background(), seedConversation(t, storage, 0)); !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("empty conversation err = %v, want ErrNothingToCompact", err)
	}
	if _, err := runner.CompactConversation(context.Background(), ""); err == nil {
		t.Fatal("compacting without a conversation must fail")
	}

	conversationID := seedConversation(t, storage, 2)
	runner.mu.Lock()
	runner.jobs["busy-job"] = &job{id: "busy-job", args: ChatArgs{ConversationID: conversationID}}
	runner.mu.Unlock()
	_, err := runner.CompactConversation(context.Background(), conversationID)
	var appErr *ipc.Error
	if !errors.As(err, &appErr) || !errors.Is(err, ErrConversationBusy) {
		t.Fatalf("busy conversation err = %v, want ErrConversationBusy", err)
	}
	runner.mu.Lock()
	delete(runner.jobs, "busy-job")
	runner.mu.Unlock()
}
