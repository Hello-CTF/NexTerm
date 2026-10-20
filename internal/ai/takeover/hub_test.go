package takeover

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/hub"
)

func TestR1TakeoverHubGracefulTerminalDrain(t *testing.T) {
	bus := hub.New(hub.Options{})
	t.Cleanup(func() { _ = bus.Close() })
	stream, err := agent.IPCStreamFactory(hub.StreamFactory{Hub: bus})(context.Background(), "channel", "job")
	if err != nil {
		t.Fatal(err)
	}
	state := &runState{stream: agent.WithEventSequence(stream), deliveryCtx: context.Background()}
	state.finish(agent.Event{Type: "done", Answer: "complete"})
	receiver, err := bus.Bind("channel")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	frame, err := receiver.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(frame.Data, &event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "done" || event["answer"] != "complete" || event["seq"] != float64(1) {
		t.Fatalf("terminal frame = %v", event)
	}
}
