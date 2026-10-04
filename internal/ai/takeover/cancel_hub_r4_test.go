package takeover

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	"github.com/ProbiusOfficial/NexTerm/internal/hub"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func nextTakeoverHubEvent(t *testing.T, receiver *hub.Receiver) map[string]any {
	t.Helper()
	frame, err := receiver.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Ack(frame.Sequence); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(frame.Data, &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestR4TakeoverCancelDeliversExactlyOneTerminal(t *testing.T) {
	for _, mode := range []string{"running", "paused"} {
		t.Run(mode, func(t *testing.T) {
			bus := hub.New(hub.Options{})
			t.Cleanup(func() { _ = bus.Close() })
			var chat model.BaseChatModel
			if mode == "running" {
				chat = &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
					<-ctx.Done()
					return nil, ctx.Err()
				}}
			} else {
				chat = sequence(toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"sudo reboot<enter>"}`)))
			}
			h := newHarness(t, chat)
			receiver, err := bus.Bind("channel")
			if err != nil {
				t.Fatal(err)
			}
			defer receiver.Close()
			response, err := h.manager.Run(context.Background(), RunArgs{TabID: "tab", ChannelID: "channel", Instruction: "go"}, agent.IPCStreamFactory(hub.StreamFactory{Hub: bus}))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "paused" {
				for {
					if event := nextTakeoverHubEvent(t, receiver); event["type"] == "confirmRequired" {
						break
					}
				}
			}
			if err := h.manager.Cancel(response.JobID); err != nil {
				t.Fatal(err)
			}
			terminals := 0
			for {
				event := nextTakeoverHubEvent(t, receiver)
				if event["type"] != "done" && event["type"] != "error" {
					continue
				}
				terminals++
				break
			}
			if terminals != 1 {
				t.Fatalf("terminal count = %d", terminals)
			}
			if _, err := receiver.Next(context.Background()); !errors.Is(err, hub.ErrClosed) {
				t.Fatalf("post-terminal read error = %v", err)
			}
		})
	}
}
