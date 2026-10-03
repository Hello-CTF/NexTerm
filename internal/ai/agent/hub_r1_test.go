package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/usage"
	"github.com/ProbiusOfficial/NexTerm/internal/hub"
)

func hubJobStream(t *testing.T, bus *hub.Hub) Stream {
	t.Helper()
	stream, err := IPCStreamFactory(hub.StreamFactory{Hub: bus})(context.Background(), "channel", "job")
	if err != nil {
		t.Fatal(err)
	}
	return WithEventSequence(stream)
}

func nextHubEvent(t *testing.T, receiver *hub.Receiver) map[string]any {
	t.Helper()
	frame, err := receiver.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(frame.Data, &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestR1HubCloseBeforeBindPreservesAllEvents(t *testing.T) {
	bus := hub.New(hub.Options{})
	t.Cleanup(func() { _ = bus.Close() })
	stream := hubJobStream(t, bus)
	if err := stream.Send(context.Background(), Event{Type: "delta", Text: "before"}); err != nil {
		t.Fatal(err)
	}
	current := &job{stream: stream, deliveryCtx: context.Background()}
	current.finish("answer", 1, usage.Usage{}, nil)
	receiver, err := bus.Bind("channel")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	first := nextHubEvent(t, receiver)
	if first["type"] != "delta" || first["seq"] != float64(1) {
		t.Fatalf("first event = %v", first)
	}
	second := nextHubEvent(t, receiver)
	if second["type"] != "done" || second["answer"] != "answer" || second["seq"] != float64(2) {
		t.Fatalf("terminal event = %v", second)
	}
	if _, err := receiver.Next(context.Background()); !errors.Is(err, hub.ErrClosed) {
		t.Fatalf("post-drain read error = %v", err)
	}
	if stats := bus.Stats(); stats.QueuedFrames != 0 || stats.LiveChannels != 0 || stats.PendingChannels != 0 {
		t.Fatalf("post-drain channel remains: %+v", stats)
	}
}

func TestR1HubUnreadDisconnectReconnectPreservesTerminal(t *testing.T) {
	bus := hub.New(hub.Options{})
	t.Cleanup(func() { _ = bus.Close() })
	firstReceiver, err := bus.Bind("channel")
	if err != nil {
		t.Fatal(err)
	}
	stream := hubJobStream(t, bus)
	if err := stream.Send(context.Background(), Event{Type: "delta", Text: "unread"}); err != nil {
		t.Fatal(err)
	}
	if err := firstReceiver.Close(); err != nil {
		t.Fatal(err)
	}
	current := &job{stream: stream, deliveryCtx: context.Background()}
	current.finish("terminal", 1, usage.Usage{}, nil)
	secondReceiver, err := bus.Bind("channel")
	if err != nil {
		t.Fatal(err)
	}
	defer secondReceiver.Close()
	first := nextHubEvent(t, secondReceiver)
	second := nextHubEvent(t, secondReceiver)
	if first["type"] != "delta" || first["seq"] != float64(1) || second["type"] != "done" || second["seq"] != float64(2) {
		t.Fatalf("reconnect events = %v, %v", first, second)
	}
}

func TestR1HubFullQueueForceCancelIsBounded(t *testing.T) {
	backpressured := make(chan struct{})
	var once sync.Once
	bus := hub.New(hub.Options{QueueFrames: 1, OnBackpressure: func(string, int) {
		once.Do(func() { close(backpressured) })
	}})
	t.Cleanup(func() { _ = bus.Close() })
	stream := hubJobStream(t, bus)
	if err := stream.Send(context.Background(), Event{Type: "delta", Text: "full"}); err != nil {
		t.Fatal(err)
	}
	deliveryCtx, forceCancel := context.WithCancel(context.Background())
	current := &job{stream: stream, deliveryCtx: deliveryCtx, forceCancel: forceCancel}
	finished := make(chan struct{})
	go func() {
		current.finish("blocked", 1, usage.Usage{}, nil)
		close(finished)
	}()
	select {
	case <-backpressured:
	case <-time.After(time.Second):
		t.Fatal("terminal send did not reach backpressure")
	}
	forceCancel()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("force cancellation did not unblock terminal send")
	}
	if stats := bus.Stats(); stats.QueuedFrames != 0 || stats.LiveChannels != 0 || stats.PendingChannels != 0 {
		t.Fatalf("force-closed channel remains: %+v", stats)
	}
}
