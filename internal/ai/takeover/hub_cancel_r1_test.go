package takeover

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestR1TakeoverUserCancelUnblocksFullHubQueue(t *testing.T) {
	backpressured := make(chan struct{})
	var once sync.Once
	bus := hub.New(hub.Options{QueueFrames: 1, OnBackpressure: func(string, int) {
		once.Do(func() { close(backpressured) })
	}})
	t.Cleanup(func() { _ = bus.Close() })
	chat := &fakeModel{stream: func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		return schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("first", nil),
			schema.AssistantMessage("second", nil),
		}), nil
	}}
	h := newHarness(t, chat)
	response, err := h.manager.Run(context.Background(), RunArgs{TabID: "tab", ChannelID: "channel", Instruction: "go"}, agent.IPCStreamFactory(hub.StreamFactory{Hub: bus}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-backpressured:
	case <-time.After(time.Second):
		t.Fatal("producer did not reach full queue")
	}
	canceled := make(chan error, 1)
	go func() { canceled <- h.manager.Cancel(response.JobID) }()
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
	if err := h.manager.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	if stats := bus.Stats(); stats.QueuedFrames != 0 || stats.LiveChannels != 0 || stats.PendingChannels != 0 {
		t.Fatalf("canceled channel remains: %+v", stats)
	}
}
