package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
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
	if usage.PromptTokens != 9 || usage.CompletionTokens != 4 || usage.CachedTokens != 2 || usage.Model != "served-m" || usage.RunID != "run-1" || !strings.HasPrefix(usage.CallID, "main:") || usage.Channel != "main" {
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

func TestUsageFallbackCallIDIdentifiesRunChannel(t *testing.T) {
	for _, test := range []struct {
		name    string
		source  string
		prefix  string
		channel string
	}{
		{name: "main", prefix: "main:", channel: "main"},
		{name: "tool subagent", source: "subagent", prefix: "tool-subagent:", channel: "tool-subagent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			chat := sequenceModel(assistantWithUsage("完成", 12, 3))
			runner, storage := sceneRunner(t, chat, nil)
			stream := &SliceStream{}
			response, err := runner.Start(context.Background(), ChatArgs{Message: "go", Source: test.source}, StaticStream(stream))
			if err != nil {
				t.Fatal(err)
			}
			events := waitClosed(t, stream)
			var usage *Event
			for index := range events {
				if events[index].Type == "usage" {
					if usage != nil {
						t.Fatalf("multiple usage events: %+v", events)
					}
					usage = &events[index]
				}
			}
			if usage == nil || !strings.HasPrefix(usage.CallID, test.prefix) {
				t.Fatalf("usage channel call ID = %+v, want prefix %q", usage, test.prefix)
			}
			journaled := false
			for _, event := range runEventsOf(t, storage, response.JobID) {
				if event.Type != "usage" {
					continue
				}
				var payload struct {
					CallID  string
					Channel string
				}
				if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
					t.Fatal(err)
				}
				if payload.CallID == usage.CallID {
					journaled = true
					if payload.Channel != test.channel {
						t.Fatalf("journaled usage channel = %q, want %q", payload.Channel, test.channel)
					}
				}
			}
			if !journaled {
				t.Fatalf("stable call ID %q missing from usage journal", usage.CallID)
			}
		})
	}
}

func TestUsageCallIDIsAgentOwnedAndUniquePerActualCall(t *testing.T) {
	first := compactionUsageMessage("first", 10, 2, "shared-provider")
	second := compactionUsageMessage("second", 20, 4, "shared-provider")
	firstID := usageCallID(first, "main", "chat")
	secondID := usageCallID(second, "main", "chat")
	if firstID == secondID {
		t.Fatalf("different calls sharing a provider ID were collapsed to %q", firstID)
	}
	if got := usageCallID(first, "main", "chat"); got != firstID {
		t.Fatalf("same response call ID changed from %q to %q", firstID, got)
	}
	if got := first.Extra[provider.MessageExtraCallID]; got != "shared-provider" {
		t.Fatalf("provider call ID metadata was overwritten: %v", got)
	}
	if got := second.Extra[provider.MessageExtraCallID]; got != "shared-provider" {
		t.Fatalf("provider call ID metadata was overwritten: %v", got)
	}
}
