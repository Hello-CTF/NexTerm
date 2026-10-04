package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestToolPanicIsolatedIntoFailureOutput(t *testing.T) {
	var calls atomic.Int64
	var recovered atomic.Bool
	chat := &fakeModel{stream: func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("c1", "list_assets", `{}`))}), nil
		}
		for _, message := range messages {
			if message.Role != schema.Tool {
				continue
			}
			var output tools.Output
			if json.Unmarshal([]byte(message.Content), &output) == nil && !output.OK && strings.Contains(output.Text, "崩溃") {
				recovered.Store(true)
			}
		}
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("已恢复", nil)}), nil
	}}
	deps := tools.Dependencies{ListAssets: func(context.Context) ([]tools.Asset, error) {
		panic("资产后端崩溃")
	}}
	runner, _ := testRunner(t, chat, deps, 0)
	stream := &SliceStream{}
	startTestJob(t, runner, stream, "列出资产")
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("tool panic terminated the run: done=%d failed=%d events=%+v", done, failed, events)
	}
	if !recovered.Load() {
		t.Fatal("model never received the tool failure output")
	}
	found := false
	for _, event := range events {
		if event.Type == "toolResult" && !event.OK && strings.Contains(event.Summary, "崩溃") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing tool failure event")
	}
}

func TestToolPanicAfterCancelStopsRun(t *testing.T) {
	entered := make(chan struct{})
	deps := tools.Dependencies{ListAssets: func(ctx context.Context) ([]tools.Asset, error) {
		close(entered)
		<-ctx.Done()
		panic("取消后崩溃")
	}}
	chat := sequenceModel(toolCallMessage(namedToolCall("c1", "list_assets", `{}`)), schema.AssistantMessage("不应到达", nil))
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model:    func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools:    tools.NewRegistry(deps),
		Store:    storage,
		Runs:     storage,
		MaxTurns: 4,
	})
	t.Cleanup(func() {
		_ = runner.Close()
		_ = storage.Close()
	})
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "列出资产", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	_ = waitEvent(t, stream, "toolCall")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("tool never started")
	}
	if err := runner.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("cancellation was swallowed: done=%d failed=%d events=%+v", done, failed, events)
	}
	row, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.RunStatusCanceled {
		t.Fatalf("run status = %s, want %s", row.Status, store.RunStatusCanceled)
	}
}
