package server

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestEventBrokerFilterPanicLiveIsolated(t *testing.T) {
	broker := NewEventBroker()
	broker.QueueSize = 8
	defer func() { _ = broker.Close() }()
	observer, unsubscribeObserver, err := broker.subscribe(0, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeObserver()

	filtering := make(chan struct{})
	releaseFilter := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFilter) }) }
	defer release()
	panicking, unsubscribePanicking, err := broker.subscribe(0, false, false, func(event ipc.Event) bool {
		if event.Payload.(map[string]int)["seq"] == 1 {
			close(filtering)
			<-releaseFilter
			panic("injected live filter panic")
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribePanicking()

	firstResult := eventTestEmitAsync(broker, 1)
	eventTestAwaitSignal(t, filtering, "live Emit did not reach the panicking filter")
	eventTestRequireSeq(t, observer, 1, 1)
	secondResult := eventTestEmitAsync(broker, 2)
	eventTestRequireSeq(t, observer, 2, 2)
	thirdResult := eventTestEmitAsync(broker, 3)
	eventTestRequireSeq(t, observer, 3, 3)
	release()
	for _, result := range []<-chan error{firstResult, secondResult, thirdResult} {
		if err := eventTestAwaitResult(t, result); !errors.Is(err, ErrEventFilterPanic) {
			t.Fatalf("Emit error = %v, want ErrEventFilterPanic", err)
		}
	}
	eventTestAwaitWorker(t, panicking)
	eventTestRequireNoEvent(t, panicking)
	if count := broker.SubscriberCount(); count != 1 {
		t.Fatalf("subscriber count after filter panic = %d, want 1", count)
	}

	fourthResult := eventTestEmitAsync(broker, 4)
	if err := eventTestAwaitResult(t, fourthResult); err != nil {
		t.Fatal(err)
	}
	eventTestRequireSeq(t, observer, 4, 4)
	eventTestRequireNoEvent(t, panicking)
}

func TestEventBrokerFilterPanicReplayIsolated(t *testing.T) {
	broker := NewEventBroker()
	broker.QueueSize = 8
	defer func() { _ = broker.Close() }()
	observer, unsubscribeObserver, err := broker.subscribe(0, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeObserver()
	for seq := 1; seq <= 3; seq++ {
		emitSeq(t, broker, ipc.TopicSessionStatus, seq)
		eventTestRequireSeq(t, observer, seq, seq)
	}

	filtering := make(chan struct{})
	releaseFilter := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFilter) }) }
	defer release()
	subscribeResult := make(chan eventSubscribeResult, 1)
	go func() {
		subscriber, unsubscribe, err := broker.subscribe(0, true, false, func(event ipc.Event) bool {
			if event.Payload.(map[string]int)["seq"] == 1 {
				close(filtering)
				<-releaseFilter
				panic("injected replay filter panic")
			}
			return true
		})
		subscribeResult <- eventSubscribeResult{subscriber: subscriber, unsubscribe: unsubscribe, err: err}
	}()
	eventTestAwaitSignal(t, filtering, "subscribe did not reach the panicking replay filter")
	broker.mu.Lock()
	var panicking *eventSubscription
	for _, subscriber := range broker.subscribers {
		if subscriber.filter != nil {
			panicking = subscriber
			break
		}
	}
	broker.mu.Unlock()
	if panicking == nil {
		t.Fatal("panicking replay subscriber was not registered")
	}

	fourthResult := eventTestEmitAsync(broker, 4)
	eventTestRequireSeq(t, observer, 4, 4)
	fifthResult := eventTestEmitAsync(broker, 5)
	eventTestRequireSeq(t, observer, 5, 5)
	release()
	var subscribed eventSubscribeResult
	select {
	case subscribed = <-subscribeResult:
	case <-time.After(5 * time.Second):
		t.Fatal("replay subscribe did not return after filter panic")
	}
	if !errors.Is(subscribed.err, ErrEventFilterPanic) {
		t.Fatalf("replay subscribe error = %v, want ErrEventFilterPanic", subscribed.err)
	}
	if subscribed.subscriber != nil || subscribed.unsubscribe != nil {
		t.Fatal("failed replay subscribe returned a live subscription")
	}
	for _, result := range []<-chan error{fourthResult, fifthResult} {
		if err := eventTestAwaitResult(t, result); !errors.Is(err, ErrEventFilterPanic) {
			t.Fatalf("pending live Emit error = %v, want ErrEventFilterPanic", err)
		}
	}
	eventTestAwaitWorker(t, panicking)
	eventTestRequireNoEvent(t, panicking)
	if count := broker.SubscriberCount(); count != 1 {
		t.Fatalf("subscriber count after replay filter panic = %d, want 1", count)
	}

	sixthResult := eventTestEmitAsync(broker, 6)
	if err := eventTestAwaitResult(t, sixthResult); err != nil {
		t.Fatal(err)
	}
	eventTestRequireSeq(t, observer, 6, 6)
	eventTestRequireNoEvent(t, panicking)
}
