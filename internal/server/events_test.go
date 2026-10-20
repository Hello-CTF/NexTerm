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

func TestEventBrokerEmitCommitsInIDOrderWithDelayedFilter(t *testing.T) {
	broker := NewEventBroker()
	firstFiltering := make(chan struct{})
	releaseFirst := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseFirst)
		}
	}()
	subscriber, unsubscribe, err := broker.subscribe(0, false, false, func(event ipc.Event) bool {
		if event.Payload.(map[string]int)["seq"] == 1 {
			close(firstFiltering)
			<-releaseFirst
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- broker.Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 1}})
	}()
	select {
	case <-firstFiltering:
	case <-ctx.Done():
		t.Fatal("first Emit did not reach the filter")
	}
	secondResult := make(chan error, 1)
	go func() {
		secondResult <- broker.Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 2}})
	}()
	var secondErr error
	secondFinished := false
	select {
	case secondErr = <-secondResult:
		secondFinished = true
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	released = true
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	if !secondFinished {
		secondErr = <-secondResult
	}
	if secondErr != nil {
		t.Fatal(secondErr)
	}
	received := drainReplay(t, subscriber, 2)
	if received[0].ID != 1 || received[1].ID != 2 {
		t.Fatalf("received IDs = [%d %d], want [1 2]", received[0].ID, received[1].ID)
	}
}

func TestEventBrokerConcurrentPublishersDeliverStrictlyIncreasingIDs(t *testing.T) {
	const publishers = 8
	const eventsPerPublisher = 25
	const total = publishers * eventsPerPublisher
	broker := NewEventBroker()
	broker.QueueSize = total
	broker.BufferSize = total
	subscriber, unsubscribe, err := broker.subscribe(0, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, publishers)
	for publisher := 0; publisher < publishers; publisher++ {
		go func() {
			<-start
			for seq := 0; seq < eventsPerPublisher; seq++ {
				if err := broker.Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": seq}}); err != nil {
					results <- err
					return
				}
			}
			results <- nil
		}()
	}
	close(start)
	for i := 0; i < publishers; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	received := drainReplay(t, subscriber, total)
	for i, envelope := range received {
		if envelope.ID != uint64(i+1) {
			t.Fatalf("received event %d ID = %d, want %d", i, envelope.ID, i+1)
		}
	}
}

func TestEventBrokerMarshalFailureDoesNotConsumeID(t *testing.T) {
	broker := NewEventBroker()
	broker.QueueSize = 4
	subscriber, unsubscribe, err := broker.subscribe(0, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	emitSeq(t, broker, ipc.TopicSessionStatus, 1)
	if err := broker.Emit(context.Background(), ipc.Event{Event: ipc.TopicSessionStatus, Payload: func() {}}); err == nil {
		t.Fatal("Emit accepted an unsupported payload")
	}
	emitSeq(t, broker, ipc.TopicSessionStatus, 2)
	received := drainReplay(t, subscriber, 2)
	if received[0].ID != 1 || received[1].ID != 2 {
		t.Fatalf("live IDs after Marshal failure = [%d %d], want [1 2]", received[0].ID, received[1].ID)
	}

	replayedSubscriber, unsubscribeReplay, err := broker.subscribe(0, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeReplay()
	replayed := drainReplay(t, replayedSubscriber, 2)
	if replayed[0].ID != 1 || replayed[1].ID != 2 {
		t.Fatalf("replay IDs after Marshal failure = [%d %d], want [1 2]", replayed[0].ID, replayed[1].ID)
	}
	select {
	case data := <-replayedSubscriber.queue:
		t.Fatalf("failed Marshal was replayed: %s", data)
	default:
	}
}

func TestEventBrokerEmitCanceledBeforeCommitDoesNotAllocate(t *testing.T) {
	broker := NewEventBroker()
	subscriber, unsubscribe, err := broker.subscribe(0, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := broker.Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 99}}); err != context.Canceled {
		t.Fatalf("canceled Emit error = %v, want context.Canceled", err)
	}
	emitSeq(t, broker, ipc.TopicSessionStatus, 1)
	received := drainReplay(t, subscriber, 1)
	if received[0].ID != 1 {
		t.Fatalf("first committed event ID = %d, want 1", received[0].ID)
	}
	select {
	case data := <-subscriber.queue:
		t.Fatalf("canceled Emit allocated an event: %s", data)
	default:
	}
	if err := broker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := broker.Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus}); err != ErrEventBrokerClosed {
		t.Fatalf("closed canceled Emit error = %v, want ErrEventBrokerClosed", err)
	}
}

func TestEventBrokerEmitCancelAfterCommitPreservesSequence(t *testing.T) {
	broker := NewEventBroker()
	broker.QueueSize = 1
	secondFiltering := make(chan struct{})
	releaseSecond := make(chan struct{})
	subscriber, unsubscribe, err := broker.subscribe(0, false, false, func(event ipc.Event) bool {
		if event.Payload.(map[string]int)["seq"] == 2 {
			close(secondFiltering)
			<-releaseSecond
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	emitSeq(t, broker, ipc.TopicSessionStatus, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- broker.Emit(ctx, ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 2}})
	}()
	select {
	case <-secondFiltering:
	case <-time.After(5 * time.Second):
		close(releaseSecond)
		t.Fatal("second Emit did not reach the filter")
	}
	cancel()
	close(releaseSecond)
	if err := <-result; err != context.Canceled {
		t.Fatalf("canceled committed Emit error = %v, want context.Canceled", err)
	}
	select {
	case <-subscriber.done:
		t.Fatal("canceled Emit dropped the subscriber")
	default:
	}
	if first := drainReplay(t, subscriber, 1)[0]; first.ID != 1 {
		t.Fatalf("first queued event ID = %d, want 1", first.ID)
	}
	emitSeq(t, broker, ipc.TopicSessionStatus, 3)
	if third := drainReplay(t, subscriber, 1)[0]; third.ID != 3 {
		t.Fatalf("event after canceled commit ID = %d, want 3", third.ID)
	}
}

func TestEventBrokerSlowSubscriberCallbackRunsOutsideBrokerLocks(t *testing.T) {
	broker := NewEventBroker()
	broker.QueueSize = 1
	slow, unsubscribe, err := broker.subscribe(0, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	emitSeq(t, broker, ipc.TopicSessionStatus, 1)
	callbackResult := make(chan error, 1)
	broker.OnSlowSubscriber = func() {
		callbackResult <- broker.Emit(context.Background(), ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 3}})
	}
	emitResult := make(chan error, 1)
	go func() {
		emitResult <- broker.Emit(context.Background(), ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 2}})
	}()
	select {
	case err := <-emitResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("slow subscriber callback deadlocked Emit")
	}
	select {
	case err := <-callbackResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reentrant Emit did not run in the callback")
	}
	select {
	case <-slow.done:
	default:
		t.Fatal("slow subscriber was not dropped before the callback")
	}
}

func TestEventBrokerSlowSubscriberCallbackSurvivesUnsubscribeInterleave(t *testing.T) {
	for attempt := 0; attempt < 32; attempt++ {
		exercised := func() bool {
			broker := NewEventBroker()
			broker.QueueSize = 1
			callbacks := 0
			broker.OnSlowSubscriber = func() { callbacks++ }
			slow, unsubscribeSlow, err := broker.subscribe(0, false, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer unsubscribeSlow()
			filtering := make(chan struct{})
			releaseFilter := make(chan struct{})
			gate, unsubscribeGate, err := broker.subscribe(0, false, false, func(event ipc.Event) bool {
				if event.Payload.(map[string]int)["seq"] == 2 {
					close(filtering)
					<-releaseFilter
				}
				return true
			})
			if err != nil {
				t.Fatal(err)
			}
			defer unsubscribeGate()
			emitSeq(t, broker, ipc.TopicSessionStatus, 1)
			emitResult := make(chan error, 1)
			go func() {
				emitResult <- broker.Emit(context.Background(), ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 2}})
			}()
			select {
			case <-filtering:
			case <-time.After(5 * time.Second):
				close(releaseFilter)
				t.Fatal("Emit did not reach the gate filter")
			}
			broker.mu.Lock()
			_, slowStillRegistered := broker.subscribers[slow.id]
			broker.mu.Unlock()
			if slowStillRegistered {
				close(releaseFilter)
				if err := <-emitResult; err != nil {
					t.Fatal(err)
				}
				return false
			}
			unsubscribeSlow()
			close(releaseFilter)
			if err := <-emitResult; err != nil {
				t.Fatal(err)
			}
			if callbacks != 2 {
				t.Fatalf("slow subscriber callbacks = %d, want 2", callbacks)
			}
			for _, subscriber := range []*eventSubscription{slow, gate} {
				select {
				case <-subscriber.done:
				default:
					t.Fatal("slow subscriber done channel is not closed")
				}
			}
			if count := broker.SubscriberCount(); count != 0 {
				t.Fatalf("subscriber count = %d, want 0", count)
			}
			return true
		}()
		if exercised {
			return
		}
	}
	t.Fatal("did not exercise unsubscribe between map removal and drop")
}

func TestEventBrokerCloseDuringFilterDoesNotWaitForDispatch(t *testing.T) {
	broker := NewEventBroker()
	filtering := make(chan struct{})
	releaseFilter := make(chan struct{})
	subscriber, unsubscribe, err := broker.subscribe(0, false, false, func(ipc.Event) bool {
		close(filtering)
		<-releaseFilter
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	emitResult := make(chan error, 1)
	go func() {
		emitResult <- broker.Emit(context.Background(), ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 1}})
	}()
	select {
	case <-filtering:
	case <-time.After(5 * time.Second):
		close(releaseFilter)
		t.Fatal("Emit did not reach the filter")
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- broker.Close() }()
	select {
	case err := <-closeResult:
		if err != nil {
			close(releaseFilter)
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		close(releaseFilter)
		t.Fatal("Close waited for the dispatch filter")
	}
	close(releaseFilter)
	if err := <-emitResult; err != ErrEventBrokerClosed {
		t.Fatalf("Emit error after Close = %v, want ErrEventBrokerClosed", err)
	}
	eventTestAwaitWorker(t, subscriber)
	select {
	case <-subscriber.done:
	default:
		t.Fatal("Close did not drop the subscriber")
	}
	select {
	case data := <-subscriber.queue:
		t.Fatalf("event was enqueued after Close: %s", data)
	default:
	}
}

func TestEventBrokerDropDuringFilterDoesNotFailEmit(t *testing.T) {
	broker := NewEventBroker()
	filtering := make(chan struct{})
	releaseFilter := make(chan struct{})
	slow, unsubscribeSlow, err := broker.subscribe(0, false, false, func(ipc.Event) bool {
		close(filtering)
		<-releaseFilter
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeSlow()
	fast, unsubscribeFast, err := broker.subscribe(0, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeFast()
	emitResult := make(chan error, 1)
	go func() {
		emitResult <- broker.Emit(context.Background(), ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": 1}})
	}()
	select {
	case <-filtering:
	case <-time.After(5 * time.Second):
		close(releaseFilter)
		t.Fatal("Emit did not reach the filter")
	}
	dropResult := make(chan struct{})
	go func() {
		unsubscribeSlow()
		close(dropResult)
	}()
	select {
	case <-dropResult:
	case <-time.After(5 * time.Second):
		close(releaseFilter)
		t.Fatal("drop waited for the dispatch filter")
	}
	close(releaseFilter)
	if err := <-emitResult; err != nil {
		t.Fatal(err)
	}
	if received := drainReplay(t, fast, 1)[0]; received.ID != 1 {
		t.Fatalf("fast subscriber ID = %d, want 1", received.ID)
	}
	eventTestAwaitWorker(t, slow)
	select {
	case data := <-slow.queue:
		t.Fatalf("dropped subscriber received an event: %s", data)
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

func BenchmarkEventBrokerBufferAppend(b *testing.B) {
	for _, size := range []int{256, 4096, 65536} {
		b.Run(fmt.Sprintf("buffer=%d", size), func(b *testing.B) {
			broker := NewEventBroker()
			broker.BufferSize = size
			event := ipc.Event{Event: ipc.TopicSessionStatus, Payload: "payload"}
			data := make([]byte, 256)
			for i := 0; i < size; i++ {
				broker.appendBufferLocked(uint64(i+1), event, data)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				broker.appendBufferLocked(uint64(size+i+1), event, data)
			}
		})
	}
}

func BenchmarkEventBrokerEmit(b *testing.B) {
	for _, bufferSize := range []int{256, 4096, 65536} {
		for _, subscriberCount := range []int{0, 10, 100} {
			for _, payloadSize := range []int{256, 4096} {
				b.Run(fmt.Sprintf("buffer=%d/subscribers=%d/payload=%d", bufferSize, subscriberCount, payloadSize), func(b *testing.B) {
					broker := NewEventBroker()
					broker.BufferSize = bufferSize
					broker.QueueSize = DefaultEventQueueSize
					event := ipc.Event{Event: ipc.TopicSessionStatus, Payload: strings.Repeat("x", payloadSize)}
					for i := 0; i < bufferSize; i++ {
						broker.appendBufferLocked(uint64(i+1), event, nil)
					}
					broker.nextEventID = uint64(bufferSize)
					b.Cleanup(func() { _ = broker.Close() })
					subscribers := make([]*eventSubscription, 0, subscriberCount)
					for i := 0; i < subscriberCount; i++ {
						subscriber, _, err := broker.subscribe(0, false, false, nil)
						if err != nil {
							b.Fatal(err)
						}
						subscribers = append(subscribers, subscriber)
					}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if err := broker.Emit(context.Background(), event); err != nil {
							b.Fatal(err)
						}
						b.StopTimer()
						for _, subscriber := range subscribers {
							<-subscriber.queue
						}
						b.StartTimer()
					}
					b.StopTimer()
					if count := broker.SubscriberCount(); count != subscriberCount {
						b.Fatalf("subscriber count = %d, want %d", count, subscriberCount)
					}
				})
			}
		}
	}
}
