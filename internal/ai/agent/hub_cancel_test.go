package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ai/usage"
	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/cloudwego/eino/schema"
)

func TestR1HubBoundUnreadReceiverDrainsGracefulCompletion(t *testing.T) {
	bus := hub.New(hub.Options{})
	t.Cleanup(func() { _ = bus.Close() })
	stream := hubJobStream(t, bus)
	receiver, err := bus.Bind("channel")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	if err := stream.Send(context.Background(), Event{Type: "delta", Text: "queued"}); err != nil {
		t.Fatal(err)
	}
	current := &job{stream: stream, deliveryCtx: context.Background()}
	current.finish("done", 1, usage.Usage{}, nil)
	first := nextHubEvent(t, receiver)
	second := nextHubEvent(t, receiver)
	if first["type"] != "delta" || second["type"] != "done" || second["seq"] != float64(2) {
		t.Fatalf("bound-unread events = %v, %v", first, second)
	}
}

func TestR1UserCancelUnblocksFullHubQueue(t *testing.T) {
	backpressured := make(chan struct{})
	var once sync.Once
	bus := hub.New(hub.Options{QueueFrames: 1, OnBackpressure: func(string, int) {
		once.Do(func() { close(backpressured) })
	}})
	t.Cleanup(func() { _ = bus.Close() })
	chat := &fakeModel{steps: []fakeStep{{chunks: []*schema.Message{
		schema.AssistantMessage("first", nil),
		schema.AssistantMessage("second", nil),
	}}}}
	runner, _ := testRunner(t, chat, tools.Dependencies{}, 0)
	response, err := runner.Start(context.Background(), ChatArgs{Message: "go", ChannelID: "channel"}, IPCStreamFactory(hub.StreamFactory{Hub: bus}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-backpressured:
	case <-time.After(time.Second):
		t.Fatal("producer did not reach full queue")
	}
	canceled := make(chan error, 1)
	go func() { canceled <- runner.Cancel(response.JobID) }()
	select {
	case err := <-canceled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("user cancel blocked on full queue")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	if stats := bus.Stats(); stats.QueuedFrames != 0 || stats.LiveChannels != 0 || stats.PendingChannels != 0 {
		t.Fatalf("canceled channel remains: %+v", stats)
	}
}
