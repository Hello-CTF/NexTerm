package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	aicontext "github.com/ProbiusOfficial/NexTerm/internal/ai/context"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestVolatileTimeAnchorContextNotPersistedIntoMessageRows(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()

	fixed := time.Date(2026, 10, 5, 20, 48, 0, 0, time.FixedZone("CST", 8*3600))
	builder := aicontext.NewBuilder(aicontext.Dependencies{Now: func() time.Time { return fixed }})

	var mu sync.Mutex
	var captured []*schema.Message
	chat := &fakeModel{stream: func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		mu.Lock()
		if captured == nil {
			captured = messages
		}
		mu.Unlock()
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
	}}
	runner := NewRunner(Config{
		Model:   func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools:   tools.NewRegistry(tools.Dependencies{}),
		Store:   storage,
		Context: builder,
	})
	defer runner.Close()

	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "现在几点")
	waitClosed(t, stream)

	mu.Lock()
	input := captured
	mu.Unlock()
	if len(input) == 0 {
		t.Fatal("model received no input")
	}
	anchor := "2026-10-05T20:48:00+08:00"
	volatileSeen := false
	for _, message := range input {
		if strings.Contains(message.Content, "[环境上下文]") && strings.Contains(message.Content, "[当前时间]") && strings.Contains(message.Content, anchor) {
			volatileSeen = true
		}
	}
	if !volatileSeen {
		t.Fatalf("volatile time-anchor context never reached the model: %s", describeMessages(input))
	}

	rows, err := storage.MsgList(context.Background(), response.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Role != "user" || rows[1].Role != "assistant" {
		t.Fatalf("message rows = %+v", rows)
	}
	for _, row := range rows {
		for _, forbidden := range []string{"[当前时间]", "[环境上下文]", anchor} {
			if strings.Contains(row.ContentJSON, forbidden) {
				t.Errorf("volatile context %q leaked into %s message row: %q", forbidden, row.Role, row.ContentJSON)
			}
		}
	}
}
