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

var compactionCanaries = []string{"canary-cmd-0 systemctl restart nginx", "canary-path-1 /etc/nginx/nginx.conf"}

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

func (m *compactionModel) snapshotGenerateInputs() [][]*schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]*schema.Message(nil), m.generateIn...)
}

func (m *compactionModel) snapshotInputs() [][]*schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]*schema.Message(nil), m.inputs...)
}

func derivingSummaryGenerate(_ context.Context, messages []*schema.Message) (*schema.Message, error) {
	var facts []string
	for _, message := range messages {
		for _, canary := range compactionCanaries {
			if strings.Contains(message.Content, canary) {
				facts = append(facts, canary)
			}
		}
	}
	return schema.AssistantMessage("摘要："+strings.Join(facts, "；"), nil), nil
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
	for i := 0; i < 12; i++ {
		question := fmt.Sprintf("旧 filler %d %s", i, strings.Repeat("x", 400))
		switch i {
		case 0:
			question = "旧 filler 0 canary-cmd-0 systemctl restart nginx " + strings.Repeat("x", 350)
		case 1:
			question = "旧 filler 1 canary-path-1 /etc/nginx/nginx.conf " + strings.Repeat("x", 350)
		case 11:
			question = "最近的问题：nginx 502 如何修复"
		}
		if err := storage.MsgInsert(ctx, row.ID, "user", map[string]any{"role": "user", "content": question}, nil, nil); err != nil {
			t.Fatal(err)
		}
		answer := fmt.Sprintf("旧回复 %d %s", i, strings.Repeat("y", 400))
		if i == 11 {
			answer = "最近的回复：正在排查"
		}
		if err := storage.MsgInsert(ctx, row.ID, "assistant", map[string]any{"role": "assistant", "content": answer}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	return row.ID
}

func seedOversizedLatestExchange(t *testing.T, storage *store.Store) string {
	t.Helper()
	row, err := storage.ConvCreate(context.Background(), "超预算最近交换", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := storage.MsgInsert(ctx, row.ID, "user", map[string]any{"role": "user", "content": "旧问题 " + strings.Repeat("x", 3100)}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := storage.MsgInsert(ctx, row.ID, "assistant", map[string]any{"role": "assistant", "content": "旧回复 " + strings.Repeat("y", 3100)}, nil, nil); err != nil {
		t.Fatal(err)
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

func requireCompactionRunCompleted(t *testing.T, stream *SliceStream, chat *compactionModel) (string, []Event) {
	t.Helper()
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("run did not complete: done=%d failed=%d events=%+v", done, failed, events)
	}
	inputs := chat.snapshotInputs()
	if len(inputs) == 0 {
		t.Fatal("model was never called")
	}
	context := flattenMessages(inputs[0])
	if !strings.Contains(context, "继续") {
		t.Fatalf("current input missing from model context:\n%s", context)
	}
	return context, events
}

func TestDurableHistorySummarizedKeepingKeyFacts(t *testing.T) {
	chat := &compactionModel{generate: derivingSummaryGenerate}
	runner, storage := compactionRunner(t, chat, 2000)
	conversationID := seedLongHistory(t, storage)
	stream := &SliceStream{}
	if _, err := runner.Start(context.Background(), ChatArgs{ConversationID: conversationID, Message: "继续", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream)); err != nil {
		t.Fatal(err)
	}
	context, events := requireCompactionRunCompleted(t, stream, chat)
	if !hasStatusPhase(events, "compacting") {
		t.Fatal("missing compacting status event")
	}
	generateInputs := chat.snapshotGenerateInputs()
	if len(generateInputs) < 2 {
		t.Fatalf("history was not summarized in chunks: %d Generate calls", len(generateInputs))
	}
	var summarizerSeen strings.Builder
	for _, input := range generateInputs {
		if estimatedMessages(input) > 2000*3 {
			t.Fatalf("summary input exceeds model budget: %d bytes", estimatedMessages(input))
		}
		summarizerSeen.WriteString(flattenMessages(input))
	}
	for _, canary := range compactionCanaries {
		if !strings.Contains(summarizerSeen.String(), canary) {
			t.Fatalf("early canary %q never reached the summarizer", canary)
		}
	}
	for _, fact := range append(append([]string{}, compactionCanaries...), "[历史摘要]", "最近的问题") {
		if !strings.Contains(context, fact) {
			t.Fatalf("fact %q missing from model context:\n%s", fact, context)
		}
	}
	if strings.Contains(context, "旧 filler 0") || strings.Contains(context, "旧回复 11") {
		t.Fatalf("raw history was not compacted away:\n%s", context)
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
	context, events := requireCompactionRunCompleted(t, stream, chat)
	if !hasStatusPhase(events, "compacting") {
		t.Fatal("missing compacting status event")
	}
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

func TestDurableOversizedLatestExchangeSummarized(t *testing.T) {
	chat := &compactionModel{generate: derivingSummaryGenerate}
	runner, storage := compactionRunner(t, chat, 2000)
	conversationID := seedOversizedLatestExchange(t, storage)
	stream := &SliceStream{}
	if _, err := runner.Start(context.Background(), ChatArgs{ConversationID: conversationID, Message: "继续", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream)); err != nil {
		t.Fatal(err)
	}
	requireCompactionRunCompleted(t, stream, chat)
	if len(chat.snapshotGenerateInputs()) == 0 {
		t.Fatal("oversized latest exchange was not summarized")
	}
}

func TestDurableOversizedLatestExchangeSummaryFailure(t *testing.T) {
	chat := &compactionModel{generate: func(context.Context, []*schema.Message) (*schema.Message, error) {
		return nil, errors.New("模型不可用")
	}}
	runner, storage := compactionRunner(t, chat, 2000)
	conversationID := seedOversizedLatestExchange(t, storage)
	stream := &SliceStream{}
	if _, err := runner.Start(context.Background(), ChatArgs{ConversationID: conversationID, Message: "继续", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream)); err != nil {
		t.Fatal(err)
	}
	context, _ := requireCompactionRunCompleted(t, stream, chat)
	if strings.Contains(context, "[历史摘要]") {
		t.Fatalf("failed summary still replaced history:\n%s", context)
	}
}

func TestDurableOversizedLatestExchangeEmptySummary(t *testing.T) {
	chat := &compactionModel{generate: func(context.Context, []*schema.Message) (*schema.Message, error) {
		return schema.AssistantMessage("  ", nil), nil
	}}
	runner, storage := compactionRunner(t, chat, 2000)
	conversationID := seedOversizedLatestExchange(t, storage)
	stream := &SliceStream{}
	if _, err := runner.Start(context.Background(), ChatArgs{ConversationID: conversationID, Message: "继续", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream)); err != nil {
		t.Fatal(err)
	}
	context, _ := requireCompactionRunCompleted(t, stream, chat)
	if strings.Contains(context, "[历史摘要]") {
		t.Fatalf("empty summary still replaced history:\n%s", context)
	}
}

func TestCompactionHandlerSkipsShortHistory(t *testing.T) {
	t.Parallel()
	chat := &compactionModel{generate: func(context.Context, []*schema.Message) (*schema.Message, error) {
		t.Error("summary model must not be called for short history")
		return nil, nil
	}}
	handler := newCompactionHandler(chat, 300, nil)
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("短"), schema.AssistantMessage("短", nil)}}
	_, after, err := handler.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if after != state {
		t.Fatal("short history was rewritten")
	}
}

func TestSummarizeAllCoversHistoryInChunks(t *testing.T) {
	t.Parallel()
	chat := &compactionModel{generate: derivingSummaryGenerate}
	handler := newCompactionHandler(chat, 2000, nil)
	var messages []*schema.Message
	for i := 0; i < 24; i++ {
		content := fmt.Sprintf("旧 filler %d %s", i, strings.Repeat("x", 400))
		if i == 0 {
			content = "旧 filler 0 canary-cmd-0 systemctl restart nginx " + strings.Repeat("x", 350)
		}
		if i == 23 {
			content = "旧 filler 23 canary-path-1 /etc/nginx/nginx.conf " + strings.Repeat("x", 350)
		}
		if i%2 == 0 {
			messages = append(messages, schema.UserMessage(content))
		} else {
			messages = append(messages, schema.AssistantMessage(content, nil))
		}
	}
	summary, err := handler.summarizeAll(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range compactionCanaries {
		if !strings.Contains(summary, canary) {
			t.Fatalf("canary %q missing from derived summary: %s", canary, summary)
		}
	}
	if len(chat.snapshotGenerateInputs()) < 2 {
		t.Fatal("history was not chunked")
	}
}

func TestSummarizeAllRejectsEmptySummary(t *testing.T) {
	t.Parallel()
	chat := &compactionModel{generate: func(context.Context, []*schema.Message) (*schema.Message, error) {
		return schema.AssistantMessage("\n ", nil), nil
	}}
	handler := newCompactionHandler(chat, 2000, nil)
	if _, err := handler.summarizeAll(context.Background(), []*schema.Message{schema.UserMessage(strings.Repeat("x", 4000))}); err == nil {
		t.Fatal("empty summary was accepted")
	}
}

func TestCompactionFinalizeRetainsLatestUnit(t *testing.T) {
	t.Parallel()
	handler := newCompactionHandler(nil, 2000, nil)
	original := []*schema.Message{
		schema.SystemMessage("sys"),
		schema.UserMessage("旧问题"),
		schema.AssistantMessage("旧回复", nil),
		schema.UserMessage("最近的问题"),
		schema.AssistantMessage("最近回复", nil),
	}
	system, contextMsgs := splitSystemPrefix(original)
	result := handler.finalize(system, contextMsgs, "摘要：保留关键事实", 0)
	if len(result) != 4 || result[0].Role != schema.System || result[1].Role != schema.User || result[2].Content != "最近的问题" || result[3].Content != "最近回复" {
		t.Fatalf("unexpected finalized history: %+v", result)
	}
	if !strings.Contains(result[1].Content, "[历史摘要]") || !strings.Contains(result[1].Content, "摘要：保留关键事实") {
		t.Fatalf("summary message malformed: %+v", result[1])
	}
}

func TestFitHistoryBudgetNeverFailsOnOversizedHistory(t *testing.T) {
	t.Parallel()
	messages := []*schema.Message{
		schema.UserMessage(strings.Repeat("u", 4000)),
		schema.AssistantMessage(strings.Repeat("a", 4000), nil),
	}
	fitted, compacted := fitHistoryBudget(messages, 1000)
	if !compacted {
		t.Fatal("oversized history was not compacted")
	}
	if estimatedMessages(fitted) > 3000 {
		t.Fatalf("history still over budget: %d", estimatedMessages(fitted))
	}
	if len(fitted) == 0 {
		t.Fatal("history was dropped entirely")
	}
	single := []*schema.Message{schema.UserMessage(strings.Repeat("u", 9000))}
	fitted, compacted = fitHistoryBudget(single, 1000)
	if !compacted || estimatedMessages(fitted) > 3000 {
		t.Fatalf("single oversized history message not truncated: %d", estimatedMessages(fitted))
	}
}
