package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/coder/websocket"
)

func TestEventBrokerDropsSlowSubscriberWithoutStallingEmit(t *testing.T) {
	broker := NewEventBroker()
	broker.QueueSize = 4
	slowDrops := 0
	broker.OnSlowSubscriber = func() { slowDrops++ }

	slow, unsubscribeSlow, err := broker.subscribe(0, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeSlow()
	fast, unsubscribeFast, err := broker.subscribe(0, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeFast()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := 0; i < 10; i++ {
		if err := broker.Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": i}}); err != nil {
			t.Fatal(err)
		}
		var data []byte
		select {
		case data = <-fast.queue:
		case <-ctx.Done():
			t.Fatal("Emit stalled on the slow subscriber")
		}
		var envelope struct {
			Payload map[string]int `json:"payload"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Payload["seq"] != i {
			t.Fatalf("fast subscriber seq = %d, want %d", envelope.Payload["seq"], i)
		}
	}

	select {
	case <-slow.done:
	case <-ctx.Done():
		t.Fatal("slow subscriber was not dropped")
	}
	if count := broker.SubscriberCount(); count != 1 {
		t.Fatalf("subscriber count = %d, want 1", count)
	}
	if slowDrops != 1 {
		t.Fatalf("slow subscriber hook fired %d times, want 1", slowDrops)
	}
	select {
	case <-fast.done:
		t.Fatal("fast subscriber was dropped")
	default:
	}
}

func TestEventBrokerEmitContextCancelStillReported(t *testing.T) {
	broker := NewEventBroker()
	broker.QueueSize = 1
	subscriber, unsubscribe, err := broker.subscribe(0, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if err := broker.Emit(context.Background(), ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 0}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := broker.Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 1}}); err != context.Canceled {
		t.Fatalf("Emit error = %v, want context.Canceled", err)
	}
	select {
	case <-subscriber.done:
		t.Fatal("canceled Emit must not drop the subscriber")
	default:
	}
}

func TestWebSocketEventsSlowClientDoesNotStallOthers(t *testing.T) {
	config := testConfig(t, false)
	config.Events = NewEventBroker()
	config.Events.QueueSize = 4
	server, httpServer := newTestHTTP(t, config)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	eventsURL := strings.Replace(httpServer.URL, "http", "ws", 1) + "/ws/events"

	slow, _, err := websocket.Dial(ctx, eventsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.CloseNow()
	fast, _, err := websocket.Dial(ctx, eventsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	fast.SetReadLimit(1 << 20)
	defer fast.Close(websocket.StatusNormalClosure, "")
	waitFor(t, func() bool { return server.Events().SubscriberCount() == 2 })

	padding := strings.Repeat("x", 256<<10)
	emitted := 0
	for ; emitted < 200 && server.Events().SubscriberCount() == 2; emitted++ {
		if err := server.Events().Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]any{"seq": emitted, "pad": padding}}); err != nil {
			t.Fatal(err)
		}
		_, data, err := fast.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(string(data), fmt.Sprintf(`"seq":%d}}`, emitted)) {
			t.Fatalf("fast client event %d = %.64s", emitted, data)
		}
	}
	if server.Events().SubscriberCount() != 1 {
		t.Fatal("slow subscriber was not dropped")
	}
	if emitted == 0 {
		t.Fatal("no events were emitted")
	}

	if err := server.Events().Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": emitted}}); err != nil {
		t.Fatal(err)
	}
	if _, data, err := fast.Read(ctx); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(data), fmt.Sprintf(`"seq":%d`, emitted)) {
		t.Fatalf("fast client follow-up event = %s", data)
	}

	reconnected, _, err := websocket.Dial(ctx, eventsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close(websocket.StatusNormalClosure, "")
	waitFor(t, func() bool { return server.Events().SubscriberCount() == 2 })
	if err := server.Events().Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": emitted + 1}}); err != nil {
		t.Fatal(err)
	}
	if _, data, err := reconnected.Read(ctx); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(data), fmt.Sprintf(`"seq":%d`, emitted+1)) {
		t.Fatalf("reconnected client event = %s", data)
	}
}

func TestParseEventQueueEnv(t *testing.T) {
	size, err := ParseEventQueueEnv(func(string) string { return "" })
	if err != nil || size != 0 {
		t.Fatalf("empty env = %d, %v", size, err)
	}
	size, err = ParseEventQueueEnv(func(string) string { return "32" })
	if err != nil || size != 32 {
		t.Fatalf("env 32 = %d, %v", size, err)
	}
	for _, invalid := range []string{"0", "-1", "abc"} {
		if _, err := ParseEventQueueEnv(func(string) string { return invalid }); err == nil {
			t.Fatalf("ParseEventQueueEnv(%q) accepted", invalid)
		}
	}
}
