package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestFailedStreamFinalizesPartialUsageOnce(t *testing.T) {
	chat := &fakeModel{stream: func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		reader, writer := schema.Pipe[*schema.Message](1)
		go func() {
			frame := assistantWithUsage("partial", 9, 4)
			frame.ResponseMeta.Usage.PromptTokenDetails.CachedTokens = 2
			frame.Extra = map[string]any{
				"model": "served-m", "run_id": "run-1", "call_id": "call-1", "context_window": uint64(32768),
			}
			writer.Send(frame, nil)
			writer.Send(nil, errors.New("stream exploded"))
			writer.Close()
		}()
		return reader, nil
	}}
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model:    func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools:    tools.NewRegistry(tools.Dependencies{}),
		Store:    storage,
		Runs:     storage,
		MaxTurns: 0,
	})
	t.Cleanup(func() {
		_ = runner.Close()
		_ = storage.Close()
	})
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	events := waitClosed(t, stream)

	var usageEvents []Event
	var deltas []Event
	for _, event := range events {
		switch event.Type {
		case "usage":
			usageEvents = append(usageEvents, event)
		case "delta":
			deltas = append(deltas, event)
		}
	}
	if len(usageEvents) != 1 {
		t.Fatalf("usage events = %+v", usageEvents)
	}
	usage := usageEvents[0]
	if usage.PromptTokens != 9 || usage.CompletionTokens != 4 || usage.CachedTokens != 2 || usage.Model != "served-m" || usage.RunID != "run-1" || usage.CallID != "call-1" {
		t.Fatalf("usage event = %+v", usage)
	}
	if len(deltas) != 1 || deltas[0].Text != "partial" {
		t.Fatalf("delta events = %+v", deltas)
	}
	done, failed := terminalCounts(events)
	if done != 0 || failed != 1 {
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
	row, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.RunStatusFailed || row.TokensIn != 9 || row.TokensOut != 4 {
		t.Fatalf("run row = %+v", row)
	}
}

func TestFailedStreamWithoutFramesEmitsNoUsage(t *testing.T) {
	chat := &fakeModel{stream: func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		reader, writer := schema.Pipe[*schema.Message](1)
		go func() {
			writer.Send(nil, errors.New("immediate explosion"))
			writer.Close()
		}()
		return reader, nil
	}}
	runner, _ := testRunner(t, chat, tools.Dependencies{}, 0)
	stream := &SliceStream{}
	startTestJob(t, runner, stream, "go")
	events := waitClosedTimeout(t, stream, 5*time.Second)
	for _, event := range events {
		if event.Type == "usage" {
			t.Fatalf("unexpected usage event %+v", event)
		}
	}
	done, failed := terminalCounts(events)
	if done != 0 || failed != 1 {
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
}
