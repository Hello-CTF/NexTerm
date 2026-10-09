package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/coder/websocket"
)

type replayEnvelope struct {
	ID      uint64          `json:"id"`
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
	Resync  bool            `json:"resync"`
}

func emitSeq(t *testing.T, broker *EventBroker, topic ipc.Topic, seq int) {
	t.Helper()
	if err := broker.Emit(context.Background(), ipc.Event{Event: topic, Payload: map[string]int{"seq": seq}}); err != nil {
		t.Fatal(err)
	}
}

func drainReplay(t *testing.T, subscriber *eventSubscription, count int) []replayEnvelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := make([]replayEnvelope, 0, count)
	for i := 0; i < count; i++ {
		select {
		case data := <-subscriber.queue:
			var envelope replayEnvelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatal(err)
			}
			out = append(out, envelope)
		case <-ctx.Done():
			t.Fatalf("replay stalled after %d/%d messages", i, count)
		}
	}
	return out
}

func TestEventBrokerSubscribeReplaysMissedEventsInOrder(t *testing.T) {
	broker := NewEventBroker()
	for i := 1; i <= 10; i++ {
		emitSeq(t, broker, ipc.TopicSessionStatus, i)
	}
	subscriber, unsubscribe, err := broker.subscribe(7, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	replayed := drainReplay(t, subscriber, 3)
	for i, envelope := range replayed {
		if envelope.ID != uint64(8+i) {
			t.Fatalf("replay %d id = %d", i, envelope.ID)
		}
		if envelope.Event != string(ipc.TopicSessionStatus) {
			t.Fatalf("replay %d event = %q", i, envelope.Event)
		}
	}
	select {
	case data := <-subscriber.queue:
		t.Fatalf("unexpected fourth replay message: %s", data)
	case <-time.After(50 * time.Millisecond):
	}
	if err := broker.Emit(context.Background(), ipc.Event{Event: ipc.TopicAppError, Payload: map[string]int{"seq": 11}}); err != nil {
		t.Fatal(err)
	}
	live := drainReplay(t, subscriber, 1)
	if live[0].ID != 11 || live[0].Event != string(ipc.TopicAppError) {
		t.Fatalf("live event after replay = %+v", live[0])
	}
}

func TestEventBrokerSubscribeCaughtUpAndInvalidCursor(t *testing.T) {
	broker := NewEventBroker()
	emitSeq(t, broker, ipc.TopicSessionStatus, 1)
	subscriber, unsubscribe, err := broker.subscribe(1, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	select {
	case data := <-subscriber.queue:
		t.Fatalf("caught-up subscribe got %s", data)
	case <-time.After(50 * time.Millisecond):
	}

	ahead, unsubscribeAhead, err := broker.subscribe(99, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeAhead()
	markers := drainReplay(t, ahead, 1)
	if !markers[0].Resync || markers[0].ID != 1 {
		t.Fatalf("ahead-of-server cursor marker = %+v", markers[0])
	}

	invalid, unsubscribeInvalid, err := broker.subscribe(0, true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeInvalid()
	markers = drainReplay(t, invalid, 1)
	if !markers[0].Resync || markers[0].ID != 1 {
		t.Fatalf("invalid cursor marker = %+v", markers[0])
	}
}

func TestEventBrokerSubscribeResyncWhenGapExceedsBufferOrQueue(t *testing.T) {
	broker := NewEventBroker()
	broker.QueueSize = 4
	broker.BufferSize = 8
	for i := 1; i <= 20; i++ {
		emitSeq(t, broker, ipc.TopicSessionStatus, i)
	}
	stale, unsubscribeStale, err := broker.subscribe(1, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeStale()
	markers := drainReplay(t, stale, 1)
	if !markers[0].Resync || markers[0].ID != 20 {
		t.Fatalf("stale cursor marker = %+v", markers[0])
	}

	withinBuffer, unsubscribeWithin, err := broker.subscribe(15, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeWithin()
	markers = drainReplay(t, withinBuffer, 1)
	if !markers[0].Resync || markers[0].ID != 20 {
		t.Fatalf("gap-exceeds-queue marker = %+v", markers[0])
	}

	small, unsubscribeSmall, err := broker.subscribe(18, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeSmall()
	replayed := drainReplay(t, small, 2)
	for i, envelope := range replayed {
		if envelope.Resync || envelope.ID != uint64(19+i) {
			t.Fatalf("small gap replay %d = %+v", i, envelope)
		}
	}
}

func TestWebSocketEventsReconnectReplaysMissedOneShotEvents(t *testing.T) {
	server, httpServer := newTestHTTP(t, testConfig(t, false))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	eventsURL := strings.Replace(httpServer.URL, "http", "ws", 1) + "/ws/events"

	victim, _, err := websocket.Dial(ctx, eventsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return server.Events().SubscriberCount() == 1 })
	if err := server.Events().Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 1}}); err != nil {
		t.Fatal(err)
	}
	readEnvelope := func(connection *websocket.Conn) replayEnvelope {
		t.Helper()
		_, data, err := connection.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var envelope replayEnvelope
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope
	}
	first := readEnvelope(victim)
	if first.ID != 1 {
		t.Fatalf("first event id = %d", first.ID)
	}
	if err := victim.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return server.Events().SubscriberCount() == 0 })

	gap := []ipc.Topic{ipc.TopicSessionStatus, ipc.TopicTerminalExit, ipc.TopicAppError, ipc.TopicLayoutChanged}
	for i, topic := range gap {
		if err := server.Events().Emit(ctx, ipc.Event{Event: topic, Payload: map[string]int{"seq": 100 + i}}); err != nil {
			t.Fatal(err)
		}
	}

	reconnected, _, err := websocket.Dial(ctx, eventsURL+fmt.Sprintf("?since=%d", first.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close(websocket.StatusNormalClosure, "")
	for i, topic := range gap {
		envelope := readEnvelope(reconnected)
		if envelope.Resync {
			t.Fatalf("replayable gap %d triggered resync: %+v", i, envelope)
		}
		if envelope.ID != uint64(2+i) || envelope.Event != string(topic) {
			t.Fatalf("replay %d = %+v, want id %d topic %s", i, envelope, 2+i, topic)
		}
	}
	if err := server.Events().Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 200}}); err != nil {
		t.Fatal(err)
	}
	live := readEnvelope(reconnected)
	if live.ID != 6 || live.Event != string(ipc.TopicSessionStatus) {
		t.Fatalf("live event after replay = %+v", live)
	}
}

func TestWebSocketEventsForcedDropRecoversViaResyncMarker(t *testing.T) {
	config := testConfig(t, false)
	config.Events = NewEventBroker()
	config.Events.QueueSize = 4
	config.Events.BufferSize = 8
	server, httpServer := newTestHTTP(t, config)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	eventsURL := strings.Replace(httpServer.URL, "http", "ws", 1) + "/ws/events"

	fast, _, err := websocket.Dial(ctx, eventsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	fast.SetReadLimit(1 << 20)
	defer fast.Close(websocket.StatusNormalClosure, "")
	victim, _, err := websocket.Dial(ctx, eventsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer victim.CloseNow()
	waitFor(t, func() bool { return server.Events().SubscriberCount() == 2 })

	readFast := func() replayEnvelope {
		t.Helper()
		_, data, err := fast.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var envelope replayEnvelope
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope
	}

	padding := strings.Repeat("x", 256<<10)
	emitted := 0
	for ; emitted < 200 && server.Events().SubscriberCount() == 2; emitted++ {
		if err := server.Events().Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]any{"seq": emitted, "pad": padding}}); err != nil {
			t.Fatal(err)
		}
		if envelope := readFast(); envelope.ID != uint64(emitted+1) {
			t.Fatalf("fast client event %d = id %d", emitted, envelope.ID)
		}
	}
	waitFor(t, func() bool { return server.Events().SubscriberCount() == 1 })

	critical := []ipc.Topic{ipc.TopicSessionStatus, ipc.TopicTerminalExit, ipc.TopicAppError, ipc.TopicLayoutChanged}
	for i, topic := range critical {
		if err := server.Events().Emit(ctx, ipc.Event{Event: topic, Payload: map[string]int{"seq": 300 + i}}); err != nil {
			t.Fatal(err)
		}
		if envelope := readFast(); envelope.ID != uint64(emitted+i+1) || envelope.Event != string(topic) {
			t.Fatalf("fast client critical event %d = %+v", i, envelope)
		}
	}
	lastID := emitted + len(critical)

	reconnected, _, err := websocket.Dial(ctx, eventsURL+"?since=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close(websocket.StatusNormalClosure, "")
	_, data, err := reconnected.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var marker replayEnvelope
	if err := json.Unmarshal(data, &marker); err != nil {
		t.Fatal(err)
	}
	if !marker.Resync || marker.ID != uint64(lastID) {
		t.Fatalf("forced-drop recovery marker = %+v, want resync at %d", marker, lastID)
	}

	if err := server.Events().Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 400}}); err != nil {
		t.Fatal(err)
	}
	_, data, err = reconnected.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var live replayEnvelope
	if err := json.Unmarshal(data, &live); err != nil {
		t.Fatal(err)
	}
	if live.Resync || live.ID != uint64(lastID+1) {
		t.Fatalf("live event after resync marker = %+v", live)
	}
}

func TestWebSocketEventsSinceZeroResumesFromStart(t *testing.T) {
	server, httpServer := newTestHTTP(t, testConfig(t, false))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	eventsURL := strings.Replace(httpServer.URL, "http", "ws", 1) + "/ws/events"
	for i := 1; i <= 3; i++ {
		if err := server.Events().Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": i}}); err != nil {
			t.Fatal(err)
		}
	}

	connection, _, err := websocket.Dial(ctx, eventsURL+"?since=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	for i := 1; i <= 3; i++ {
		_, data, err := connection.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var envelope replayEnvelope
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Resync || envelope.ID != uint64(i) {
			t.Fatalf("resume replay %d = %+v", i, envelope)
		}
	}
}

func TestWebSocketEventsInvalidSinceGetsResyncMarker(t *testing.T) {
	server, httpServer := newTestHTTP(t, testConfig(t, false))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	eventsURL := strings.Replace(httpServer.URL, "http", "ws", 1) + "/ws/events"
	if err := server.Events().Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 1}}); err != nil {
		t.Fatal(err)
	}

	connection, _, err := websocket.Dial(ctx, eventsURL+"?since=abc", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	_, data, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var marker replayEnvelope
	if err := json.Unmarshal(data, &marker); err != nil {
		t.Fatal(err)
	}
	if !marker.Resync || marker.ID != 1 {
		t.Fatalf("invalid cursor marker = %+v", marker)
	}
}

func TestParseEventBufferEnv(t *testing.T) {
	size, err := ParseEventBufferEnv(func(string) string { return "" })
	if err != nil || size != 0 {
		t.Fatalf("empty env = %d, %v", size, err)
	}
	size, err = ParseEventBufferEnv(func(string) string { return "512" })
	if err != nil || size != 512 {
		t.Fatalf("env 512 = %d, %v", size, err)
	}
	for _, invalid := range []string{"0", "-1", "abc"} {
		if _, err := ParseEventBufferEnv(func(string) string { return invalid }); err == nil {
			t.Fatalf("ParseEventBufferEnv(%q) accepted", invalid)
		}
	}
}
