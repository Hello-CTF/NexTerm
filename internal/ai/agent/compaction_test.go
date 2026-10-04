package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type compactionModel struct {
	mu         sync.Mutex
	generate   func(context.Context, []*schema.Message) (*schema.Message, error)
	inputs     [][]*schema.Message
	generateIn [][]*schema.Message
}

func (m *compactionModel) Generate(ctx context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	m.generateIn = append(m.generateIn, messages)
	m.mu.Unlock()
	return m.generate(ctx, messages)
}

func (m *compactionModel) Stream(ctx context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.inputs = append(m.inputs, messages)
	m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("完成", nil)}), nil
}

func (m *compactionModel) snapshotInputs() [][]*schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]*schema.Message(nil), m.inputs...)
}

func compactionRunner(t *testing.T, chat model.BaseChatModel, window uint64) (*Runner, *store.Store) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return chat, window, nil },
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage,
	})
	t.Cleanup(func() {
		_ = runner.Close()
		_ = storage.Close()
	})
	return runner, storage
}

func seedLongHistory(t *testing.T, storage *store.Store) string {
	t.Helper()
	row, err := storage.ConvCreate(context.Background(), "长历史", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		question := fmt.Sprintf("旧 filler %d %s", i, strings.Repeat("x", 400))
		switch i {
		case 3:
			question = "执行过 systemctl restart nginx，配置在 /etc/nginx/nginx.conf" + strings.Repeat("x", 350)
		case 7:
			question = "最近的问题：nginx 502 如何修复"
		}
		if err := storage.MsgInsert(ctx, row.ID, "user", map[string]any{"role": "user", "content": question}, nil, nil); err != nil {
			t.Fatal(err)
		}
		answer := fmt.Sprintf("旧回复 %d %s", i, strings.Repeat("y", 400))
		if i == 7 {
			answer = "最近的回复：正在排查"
		}
		if err := storage.MsgInsert(ctx, row.ID, "assistant", map[string]any{"role": "assistant", "content": answer}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	return row.ID
}

func flattenMessages(messages []*schema.Message) string {
	var builder strings.Builder
	for _, message := range messages {
		builder.WriteString(message.Content)
		builder.WriteString("\n")
	}
	return builder.String()
}

func hasStatusPhase(events []Event, phase string) bool {
	for _, event := range events {
		if event.Type == "status" && event.Phase == phase {
			return true
		}
	}
	return false
}

func TestDurableHistorySummarizedKeepingKeyFacts(t *testing.T) {
	chat := &compactionModel{generate: func(_ context.Context, messages []*schema.Message) (*schema.Message, error) {
		if flattenMessages(messages) == "" {
			t.Error("summarizer received empty history")
		}
		return schema.AssistantMessage("用户排查 nginx 502；已执行 systemctl restart nginx；配置文件 /etc/nginx/nginx.conf；待验证修复。", nil), nil
	}}
	runner, storage := compactionRunner(t, chat, 2000)
	conversationID := seedLongHistory(t, storage)
	stream := &SliceStream{}
	if _, err := runner.Start(context.Background(), ChatArgs{ConversationID: conversationID, Message: "继续", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream)); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("long history was rejected: done=%d failed=%d events=%+v", done, failed, events)
	}
	if !hasStatusPhase(events, "compacting") {
		t.Fatal("missing compacting status event")
	}
	inputs := chat.snapshotInputs()
	if len(inputs) == 0 {
		t.Fatal("model was never called")
	}
	context := flattenMessages(inputs[0])
	for _, fact := range []string{"[历史摘要]", "systemctl restart nginx", "/etc/nginx/nginx.conf", "最近的问题"} {
		if !strings.Contains(context, fact) {
			t.Fatalf("fact %q missing from model context:\n%s", fact, context)
		}
	}
	if strings.Contains(context, "旧 filler 0") {
		t.Fatalf("oldest history was not compacted away:\n%s", context)
	}
}

func TestSummarizationFailureFallsBackToTruncation(t *testing.T) {
	chat := &compactionModel{generate: func(context.Context, []*schema.Message) (*schema.Message, error) {
		return nil, errors.New("模型不可用")
	}}
	runner, storage := compactionRunner(t, chat, 2000)
	conversationID := seedLongHistory(t, storage)
	stream := &SliceStream{}
	if _, err := runner.Start(context.Background(), ChatArgs{ConversationID: conversationID, Message: "继续", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream)); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("fallback truncation did not save the run: done=%d failed=%d events=%+v", done, failed, events)
	}
	if !hasStatusPhase(events, "compacting") {
		t.Fatal("missing compacting status event")
	}
	inputs := chat.snapshotInputs()
	if len(inputs) == 0 {
		t.Fatal("model was never called")
	}
	context := flattenMessages(inputs[0])
	if strings.Contains(context, "[历史摘要]") {
		t.Fatalf("failed summary still replaced history:\n%s", context)
	}
	if strings.Contains(context, "旧 filler 0") || !strings.Contains(context, "最近的问题") {
		t.Fatalf("fallback truncation did not fit the budget:\n%s", context)
	}
}

func TestSummarizationCancellationIsNotSwallowed(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	chat := &compactionModel{generate: func(ctx context.Context, _ []*schema.Message) (*schema.Message, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	runner, storage := compactionRunner(t, chat, 2000)
	conversationID := seedLongHistory(t, storage)
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{ConversationID: conversationID, Message: "继续", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("summarization never started")
	}
	if err := runner.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("cancellation was swallowed by compaction: done=%d failed=%d", done, failed)
	}
}

func TestCompactionHandlerSkipsShortHistory(t *testing.T) {
	t.Parallel()
	chat := &compactionModel{generate: func(context.Context, []*schema.Message) (*schema.Message, error) {
		t.Error("summary model must not be called for short history")
		return nil, nil
	}}
	handler, err := newCompactionHandler(context.Background(), chat, 300, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("短"), schema.AssistantMessage("短", nil)}}
	_, after, err := handler.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if after != state {
		t.Fatal("short history was rewritten")
	}
}

func TestCompactionFinalizeRetainsLatestUnit(t *testing.T) {
	t.Parallel()
	original := []*schema.Message{
		schema.SystemMessage("sys"),
		schema.UserMessage("旧问题"),
		schema.AssistantMessage("旧回复", nil),
		schema.UserMessage("最近的问题"),
		schema.AssistantMessage("最近回复", nil),
	}
	result, err := compactionFinalize(context.Background(), original, schema.AssistantMessage("摘要：保留关键事实", nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 4 || result[0].Role != schema.System || result[1].Role != schema.User || result[2].Content != "最近的问题" || result[3].Content != "最近回复" {
		t.Fatalf("unexpected finalized history: %+v", result)
	}
	if !strings.Contains(result[1].Content, "[历史摘要]") || !strings.Contains(result[1].Content, "摘要：保留关键事实") {
		t.Fatalf("summary message malformed: %+v", result[1])
	}
	if _, err := compactionFinalize(context.Background(), original, schema.AssistantMessage("  ", nil)); err == nil {
		t.Fatal("empty summary was accepted")
	}
}
