package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/hub"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestR4AgentCancelDeliversExactlyOneTerminal(t *testing.T) {
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
				chat = sequenceModel(toolCallMessage(namedToolCall("call", "docker_control", `{"container_id":"web","action":"start"}`)))
			}
			runner, _ := testRunner(t, chat, tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}, 0)
			receiver, err := bus.Bind("channel")
			if err != nil {
				t.Fatal(err)
			}
			defer receiver.Close()
			response, err := runner.Start(context.Background(), ChatArgs{Message: "go", ChannelID: "channel", Scope: tools.Scope{SessionID: "session"}}, IPCStreamFactory(hub.StreamFactory{Hub: bus}))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "paused" {
				for {
					if event := nextHubEvent(t, receiver); event["type"] == "confirmRequired" {
						break
					}
				}
			}
			if err := runner.Cancel(response.JobID); err != nil {
				t.Fatal(err)
			}
			terminals := 0
			for {
				event := nextHubEvent(t, receiver)
				if event["type"] != "done" && event["type"] != "error" {
					continue
				}
				terminals++
				if event["type"] != "error" {
					t.Fatalf("cancel terminal = %v", event)
				}
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
