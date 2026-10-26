package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
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

func titleTestRunner(t *testing.T, chat model.BaseChatModel, titleTimeout ...time.Duration) (*Runner, *store.Store) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	return titleTestRunnerWithStore(t, chat, storage, titleTimeout...), storage
}

func titleTestRunnerWithStore(t *testing.T, chat model.BaseChatModel, storage *store.Store, titleTimeout ...time.Duration) *Runner {
	t.Helper()
	config := Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage, Runs: storage,
	}
	if len(titleTimeout) > 0 {
		config.TitleTimeout = titleTimeout[0]
	}
	runner := NewRunner(config)
	t.Cleanup(func() { _ = runner.Close() })
	return runner
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

func waitTitleGeneration(t *testing.T, runner *Runner) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		runner.wg.Wait()
		close(done)
	}()
	waitChannelClosed(t, done, "title generation did not finish")
}

func waitTitleChange(t *testing.T, runner *Runner, storage *store.Store, conversationID, from string) string {
	t.Helper()
	waitTitleGeneration(t, runner)
	title := conversationTitleOf(t, storage, conversationID)
	if title == from {
		t.Fatalf("title did not change from %q", from)
	}
	return title
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
}

// A title cut at the rune limit keeps emoji clusters whole: the family emoji
// straddling the limit is dropped rather than split at a ZWJ.
func TestConversationTitlesKeepEmojiIntact(t *testing.T) {
	family := "👨‍👩‍👧‍👦"
	generated := strings.Repeat("中", titleMaxRunes-3) + family + "尾"
	if got := normalizeTitle(generated); got != strings.Repeat("中", titleMaxRunes-3) {
		t.Fatalf("normalizeTitle(%q) = %q", generated, got)
	}
	message := strings.Repeat("字", 22) + "❤️" + "尾"
	if got, want := AutoTitle(message), strings.Repeat("字", 22)+"❤️…"; got != want {
		t.Fatalf("AutoTitle(%q) = %q, want %q", message, got, want)
	}
	flags := strings.Repeat("字", 23) + "🇯🇵🇺🇸"
	if got, want := AutoTitle(flags), strings.Repeat("字", 23)+"…"; got != want {
		t.Fatalf("AutoTitle(%q) = %q, want %q", flags, got, want)
	}
}

func TestTitleGenerationDefaultTimeout(t *testing.T) {
	runner := NewRunner(Config{})
	t.Cleanup(func() { _ = runner.Close() })
	if runner.config.TitleTimeout != 15*time.Second {
		t.Fatalf("title generation timeout = %s", runner.config.TitleTimeout)
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
	title := waitTitleChange(t, runner, storage, conversation.ID, autoTitle)
	expected := string([]rune("这是一个超过二十个字符限制的会话标题内容啊")[:titleMaxRunes])
	if title != expected {
		t.Fatalf("generated title = %q, want %q", title, expected)
	}
	summary, err := storage.RunUsageSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var titleRow store.RunUsageSummaryRow
	usageFound := false
	for _, row := range summary {
		if row.Source == store.RunSourceTitle {
			titleRow = row
			usageFound = true
			break
		}
	}
	if !usageFound {
		t.Fatal("title usage dimension missing from summary")
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

func TestRunnerCloseCancelsTitleGenerationAndReturnsPromptly(t *testing.T) {
	generateCalled := make(chan struct{})
	generateErr := make(chan error, 1)
	chat := &titleTestModel{
		fakeModel: fakeModel{stream: answeringStream},
		generate: func(ctx context.Context) (*schema.Message, error) {
			close(generateCalled)
			<-ctx.Done()
			generateErr <- ctx.Err()
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
	waitChannelClosed(t, generateCalled, "title generation did not start")
	closeReturned := make(chan error, 1)
	go func() {
		closeReturned <- runner.Close()
	}()
	select {
	case err := <-closeReturned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner close did not cancel title generation promptly")
	}
	if err := <-generateErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("title generation error = %v, want runner cancellation", err)
	}
	if _, active := runner.titleInFlight.Load(conversation.ID); active {
		t.Fatal("runner close returned with title generation in flight")
	}
	if title := conversationTitleOf(t, storage, conversation.ID); title != AutoTitle(message) {
		t.Fatalf("canceled title generation changed the title: %q", title)
	}
	summary, err := storage.RunUsageSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range summary {
		if row.Source == store.RunSourceTitle {
			t.Fatalf("canceled title generation recorded usage: %+v", row)
		}
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
	waitTitleGeneration(t, runner)
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

func TestTitleGenerationDoesNotOverwriteUserRenameDuringModelCall(t *testing.T) {
	release := make(chan struct{})
	generateCalled := make(chan struct{})
	var generateOnce sync.Once
	chat := &titleTestModel{
		fakeModel: fakeModel{stream: answeringStream},
		generate: func(ctx context.Context) (*schema.Message, error) {
			generateOnce.Do(func() { close(generateCalled) })
			select {
			case <-release:
				return schema.AssistantMessage("自动生成标题", nil), nil
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
	runner.maybeGenerateConversationTitle(&job{args: ChatArgs{ConversationID: conversation.ID, Message: message}})
	waitChannelClosed(t, generateCalled, "title generation did not start")
	if err := storage.ConvRename(context.Background(), conversation.ID, "用户改的标题"); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitTitleGeneration(t, runner)
	if title := conversationTitleOf(t, storage, conversation.ID); title != "用户改的标题" {
		t.Fatalf("user rename during model call was overwritten: %q", title)
	}
	summary, err := storage.RunUsageSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range summary {
		if row.Source == store.RunSourceTitle {
			t.Fatalf("skipped rename recorded title usage: %+v", row)
		}
	}
}

func TestTitleGenerationTimeout(t *testing.T) {
	chat := &titleTestModel{
		fakeModel: fakeModel{stream: answeringStream},
		generate: func(ctx context.Context) (*schema.Message, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	runner, storage := titleTestRunner(t, chat, time.Nanosecond)
	const message = "帮我排查数据库连接失败的问题"
	conversation, err := storage.ConvCreate(context.Background(), AutoTitle(message), map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	runner.maybeGenerateConversationTitle(&job{args: ChatArgs{ConversationID: conversation.ID, Message: message}})
	waitTitleGeneration(t, runner)
	if title := conversationTitleOf(t, storage, conversation.ID); title != AutoTitle(message) {
		t.Fatalf("timed out title generation changed the title: %q", title)
	}
}

func TestTitleGenerationSingleFlight(t *testing.T) {
	release := make(chan struct{})
	generateCalled := make(chan struct{})
	var generateOnce sync.Once
	var generateCalls atomic.Int64
	chat := &titleTestModel{
		fakeModel: fakeModel{stream: answeringStream},
		generate: func(ctx context.Context) (*schema.Message, error) {
			generateCalls.Add(1)
			generateOnce.Do(func() { close(generateCalled) })
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
	waitChannelClosed(t, generateCalled, "title generation did not start")
	if calls := generateCalls.Load(); calls != 1 {
		t.Fatalf("concurrent title generations = %d, want 1", calls)
	}
	close(release)
	title := waitTitleChange(t, runner, storage, conversation.ID, AutoTitle("并发消息"))
	if !strings.Contains(title, "并发") {
		t.Fatalf("generated title = %q", title)
	}
}

func TestTitleGenerationSingleFlightIsPerRunner(t *testing.T) {
	release := make(chan struct{})
	bothGenerateCalled := make(chan struct{})
	var generateCalls atomic.Int64
	chat := &titleTestModel{
		fakeModel: fakeModel{stream: answeringStream},
		generate: func(ctx context.Context) (*schema.Message, error) {
			if generateCalls.Add(1) == 2 {
				close(bothGenerateCalled)
			}
			select {
			case <-release:
				return schema.AssistantMessage("实例标题", nil), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	first, storage := titleTestRunner(t, chat)
	second := titleTestRunnerWithStore(t, chat, storage)
	conversation, err := storage.ConvCreate(context.Background(), AutoTitle("共享消息"), map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	current := &job{args: ChatArgs{ConversationID: conversation.ID, Message: "共享消息"}}
	first.maybeGenerateConversationTitle(current)
	second.maybeGenerateConversationTitle(current)
	waitChannelClosed(t, bothGenerateCalled, "title generation was suppressed across runners")
	if calls := generateCalls.Load(); calls != 2 {
		t.Fatalf("concurrent title generations = %d, want 2", calls)
	}
	close(release)
	waitTitleGeneration(t, first)
	waitTitleGeneration(t, second)
	if calls := generateCalls.Load(); calls != 2 {
		t.Fatalf("finished title generations = %d, want 2", calls)
	}
	if title := conversationTitleOf(t, storage, conversation.ID); title != "实例标题" {
		t.Fatalf("generated title = %q", title)
	}
}
