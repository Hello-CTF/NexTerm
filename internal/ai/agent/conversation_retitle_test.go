package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type promptCaptureModel struct {
	fakeModel
	answer string
	err    error
	prompt string
}

func (m *promptCaptureModel) Generate(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if len(messages) > 0 {
		m.prompt = messages[0].Content
	}
	if m.err != nil {
		return nil, m.err
	}
	return schema.AssistantMessage(m.answer, nil), nil
}

func TestSplitTitlePrefix(t *testing.T) {
	cases := []struct {
		title  string
		prefix string
		topic  string
	}{
		{"主机名: 磁盘清理", "主机名: ", "磁盘清理"},
		{"主机名:磁盘清理", "主机名:", "磁盘清理"},
		{"主机名：磁盘清理", "主机名：", "磁盘清理"},
		{"主机名： 磁盘清理", "主机名： ", "磁盘清理"},
		{"【生产】数据库巡检", "【生产】", "数据库巡检"},
		{"【生产】 数据库巡检", "【生产】 ", "数据库巡检"},
		{"新会话", "", "新会话"},
		{"磁盘清理", "", "磁盘清理"},
		{"时间 12:30 的例会", "", "时间 12:30 的例会"},
		{"主机名: ", "主机名: ", ""},
		{"这是一个没有任何分隔符的很长很长的会话标题内容啊", "", "这是一个没有任何分隔符的很长很长的会话标题内容啊"},
	}
	for _, tc := range cases {
		prefix, topic := splitTitlePrefix(tc.title)
		if prefix != tc.prefix || topic != tc.topic {
			t.Fatalf("splitTitlePrefix(%q) = (%q, %q), want (%q, %q)", tc.title, prefix, topic, tc.prefix, tc.topic)
		}
	}
}

func retitleConversation(t *testing.T, title string) (*Runner, *store.Store, *promptCaptureModel, string) {
	t.Helper()
	chat := &promptCaptureModel{fakeModel: fakeModel{stream: answeringStream}, answer: "占位"}
	runner, storage := titleTestRunner(t, chat)
	conversation, err := storage.ConvCreate(context.Background(), title, map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := storage.MsgInsert(ctx, conversation.ID, "user", map[string]any{"role": "user", "content": "帮我清理磁盘"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := storage.MsgInsert(ctx, conversation.ID, "assistant", map[string]any{"role": "assistant", "content": "好的，先看磁盘占用"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	return runner, storage, chat, conversation.ID
}

func TestRetitleConversationKeepsPrefix(t *testing.T) {
	runner, storage, chat, conversationID := retitleConversation(t, "主机名: 旧主题")
	chat.answer = "清理临时文件"

	title, err := runner.RetitleConversation(context.Background(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if title != "主机名: 清理临时文件" {
		t.Fatalf("retitled = %q", title)
	}
	if got := conversationTitleOf(t, storage, conversationID); got != "主机名: 清理临时文件" {
		t.Fatalf("stored title = %q", got)
	}
	if !strings.Contains(chat.prompt, "现有标题：主机名: 旧主题") {
		t.Fatalf("prompt missing current title: %q", chat.prompt)
	}
	if !strings.Contains(chat.prompt, "user: 帮我清理磁盘") || !strings.Contains(chat.prompt, "assistant: 好的，先看磁盘占用") {
		t.Fatalf("prompt missing conversation context: %q", chat.prompt)
	}
	if !strings.Contains(chat.prompt, `"主机名: " 是标题的分组前缀`) {
		t.Fatalf("prompt missing prefix instruction: %q", chat.prompt)
	}
}

func TestRetitleConversationDefaultTitleGeneratesFreely(t *testing.T) {
	runner, storage, chat, conversationID := retitleConversation(t, "新会话")
	chat.answer = "磁盘清理"

	title, err := runner.RetitleConversation(context.Background(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if title != "磁盘清理" {
		t.Fatalf("retitled = %q", title)
	}
	if got := conversationTitleOf(t, storage, conversationID); got != "磁盘清理" {
		t.Fatalf("stored title = %q", got)
	}
	if strings.Contains(chat.prompt, "分组前缀") {
		t.Fatalf("default title should not carry prefix instruction: %q", chat.prompt)
	}
}

func TestRetitleConversationModelFailureKeepsTitle(t *testing.T) {
	runner, storage, chat, conversationID := retitleConversation(t, "主机名: 旧主题")
	chat.err = errors.New("模型不可用")

	if _, err := runner.RetitleConversation(context.Background(), conversationID); err == nil {
		t.Fatal("expected error")
	}
	if got := conversationTitleOf(t, storage, conversationID); got != "主机名: 旧主题" {
		t.Fatalf("failed retitle changed the title: %q", got)
	}
	summary, err := storage.RunUsageSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range summary {
		if row.Source == store.RunSourceTitle {
			t.Fatalf("failed retitle recorded usage: %+v", row)
		}
	}
}
