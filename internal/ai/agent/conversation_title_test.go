package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type titleTestModel struct {
	fakeModel
	generate func(context.Context) (*schema.Message, error)
}

func (m *titleTestModel) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return m.generate(ctx)
}

func titleTestRunner(t *testing.T, chat model.BaseChatModel) (*Runner, *store.Store) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage, Runs: storage,
	})
	t.Cleanup(func() {
		_ = runner.Close()
		_ = storage.Close()
	})
	return runner, storage
}

func answeringStream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("回答", nil)}), nil
}

func conversationTitleOf(t *testing.T, storage *store.Store, conversationID string) string {
	t.Helper()
	row, err := storage.ConvGet(context.Background(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	return row.Title
}

func waitTitleChange(t *testing.T, storage *store.Store, conversationID, from string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if title := conversationTitleOf(t, storage, conversationID); title != from {
			return title
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("title did not change from %q", from)
	return ""
}

func TestNormalizeTitleLimitsRunesAndStripsQuotes(t *testing.T) {
	long := "这是一个超过二十个字符限制的会话标题内容啊"
	if got := normalizeTitle(long); len([]rune(got)) != titleMaxRunes || got != string([]rune(long)[:titleMaxRunes]) {
		t.Fatalf("normalizeTitle(%q) = %q", long, got)
	}
	if got := normalizeTitle("“带引号的标题”\n第二行"); got != "带引号的标题" {
		t.Fatalf("quoted title = %q", got)
	}
	if got := normalizeTitle("  多空格   标题 "); got != "多空格 标题" {
		t.Fatalf("spaced title = %q", got)
	}
	if got := normalizeTitle("\n\n"); got != "" {
		t.Fatalf("blank title = %q", got)
	}
	if titleGenerationTimeout != 15*time.Second {
		t.Fatalf("title generation timeout = %s", titleGenerationTimeout)
	}
}

func TestTitleGenerationAfterFirstRun(t *testing.T) {
	release := make(chan struct{})
	generateCalled := make(chan struct{})
	var generateOnce sync.Once
	titleResponse := schema.AssistantMessage("这是一个超过二十个字符限制的会话标题内容啊", nil)
	titleResponse.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 12, CompletionTokens: 8}}
	chat := &titleTestModel{
		fakeModel: fakeModel{stream: answeringStream},
		generate: func(ctx context.Context) (*schema.Message, error) {
			generateOnce.Do(func() { close(generateCalled) })
			select {
			case <-release:
				return titleResponse, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	runner, storage := titleTestRunner(t, chat)
	const message = "帮我排查数据库连接失败的问题"
	conversation, err := storage.ConvCreate(context.Background(), AutoTitle(message), map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	current := &job{args: ChatArgs{ConversationID: conversation.ID, Message: message}}
	runner.maybeGenerateConversationTitle(current)
	waitChannelClosed(t, generateCalled, "title generation did not start")
	autoTitle := AutoTitle(message)
	if title := conversationTitleOf(t, storage, conversation.ID); title != autoTitle {
		t.Fatalf("title generation blocked its caller: title = %q", title)
	}
	close(release)
	title := waitTitleChange(t, storage, conversation.ID, autoTitle)
	expected := string([]rune("这是一个超过二十个字符限制的会话标题内容啊")[:titleMaxRunes])
	if title != expected {
		t.Fatalf("generated title = %q, want %q", title, expected)
	}
	summary, err := storage.RunUsageSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bySource := map[string]store.RunUsageSummaryRow{}
	for _, row := range summary {
		bySource[row.Source] = row
	}
	titleRow, ok := bySource[store.RunSourceTitle]
	if !ok {
		t.Fatalf("title usage dimension missing from summary: %+v", summary)
	}
	if titleRow.TokensIn != 12 || titleRow.TokensOut != 8 || titleRow.Runs != 1 {
		t.Fatalf("title usage row = %+v", titleRow)
	}
	runs, err := runner.RunList(context.Background(), conversation.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("run list leaked title row: %+v", runs)
	}
}

func TestTitleGenerationSilentFailure(t *testing.T) {
	chat := &titleTestModel{
		fakeModel: fakeModel{stream: answeringStream},
		generate: func(context.Context) (*schema.Message, error) {
			return nil, errors.New("模型不可用")
		},
	}
	runner, storage := titleTestRunner(t, chat)
	const message = "帮我排查数据库连接失败的问题"
	conversation, err := storage.ConvCreate(context.Background(), AutoTitle(message), map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	runner.maybeGenerateConversationTitle(&job{args: ChatArgs{ConversationID: conversation.ID, Message: message}})
	time.Sleep(300 * time.Millisecond)
	if title := conversationTitleOf(t, storage, conversation.ID); title != AutoTitle(message) {
		t.Fatalf("failed title generation changed the title: %q", title)
	}
	summary, err := storage.RunUsageSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range summary {
		if row.Source == store.RunSourceTitle {
			t.Fatalf("failed title generation recorded usage: %+v", row)
		}
	}
}

func TestTitleGenerationSkipsRenamedConversation(t *testing.T) {
	var generateCalls atomic.Int64
	chat := &titleTestModel{
		fakeModel: fakeModel{stream: answeringStream},
		generate: func(context.Context) (*schema.Message, error) {
			generateCalls.Add(1)
			return schema.AssistantMessage("新标题", nil), nil
		},
	}
	runner, storage := titleTestRunner(t, chat)
	conversation, err := storage.ConvCreate(context.Background(), AutoTitle("原始消息"), map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.ConvRename(context.Background(), conversation.ID, "用户自定义标题"); err != nil {
		t.Fatal(err)
	}
	runner.generateConversationTitle(context.Background(), runner.config.Model, conversation.ID, "原始消息", "")
	if title := conversationTitleOf(t, storage, conversation.ID); title != "用户自定义标题" {
		t.Fatalf("user rename was overwritten: %q", title)
	}
	if generateCalls.Load() != 0 {
		t.Fatalf("model called %d times for a renamed conversation", generateCalls.Load())
	}
	summary, err := storage.RunUsageSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range summary {
		if row.Source == store.RunSourceTitle {
			t.Fatalf("skipped title generation recorded usage: %+v", row)
		}
	}
}

func TestTitleGenerationTimeout(t *testing.T) {
	original := titleGenerationTimeout
	titleGenerationTimeout = 50 * time.Millisecond
	defer func() { titleGenerationTimeout = original }()
	chat := &titleTestModel{
		fakeModel: fakeModel{stream: answeringStream},
		generate: func(ctx context.Context) (*schema.Message, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	runner, storage := titleTestRunner(t, chat)
	const message = "帮我排查数据库连接失败的问题"
	conversation, err := storage.ConvCreate(context.Background(), AutoTitle(message), map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	runner.maybeGenerateConversationTitle(&job{args: ChatArgs{ConversationID: conversation.ID, Message: message}})
	time.Sleep(300 * time.Millisecond)
	if title := conversationTitleOf(t, storage, conversation.ID); title != AutoTitle(message) {
		t.Fatalf("timed out title generation changed the title: %q", title)
	}
}

func TestTitleGenerationSingleFlight(t *testing.T) {
	release := make(chan struct{})
	var generateCalls atomic.Int64
	chat := &titleTestModel{
		fakeModel: fakeModel{stream: answeringStream},
		generate: func(ctx context.Context) (*schema.Message, error) {
			generateCalls.Add(1)
			select {
			case <-release:
				return schema.AssistantMessage("并发标题", nil), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	runner, storage := titleTestRunner(t, chat)
	conversation, err := storage.ConvCreate(context.Background(), AutoTitle("并发消息"), map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	current := &job{args: ChatArgs{ConversationID: conversation.ID, Message: "并发消息"}}
	runner.maybeGenerateConversationTitle(current)
	runner.maybeGenerateConversationTitle(current)
	time.Sleep(100 * time.Millisecond)
	if calls := generateCalls.Load(); calls != 1 {
		t.Fatalf("concurrent title generations = %d, want 1", calls)
	}
	close(release)
	title := waitTitleChange(t, storage, conversation.ID, AutoTitle("并发消息"))
	if !strings.Contains(title, "并发") {
		t.Fatalf("generated title = %q", title)
	}
}
