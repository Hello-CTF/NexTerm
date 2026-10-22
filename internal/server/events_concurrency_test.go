package server

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/account"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestEventBrokerSlowOwnershipFilterDoesNotBlockDispatch(t *testing.T) {
	broker := NewEventBroker()
	broker.QueueSize = 4
	filtering := make(chan struct{})
	releaseFilter := make(chan struct{})
	var filteringOnce sync.Once
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFilter) }) }
	defer release()

	server := &Server{assetOwner: func(_ context.Context, assetID string) (string, error) {
		if assetID == "asset-1" {
			filteringOnce.Do(func() { close(filtering) })
			<-releaseFilter
		}
		return "u-1", nil
	}}
	filtered, unsubscribeFiltered, err := broker.subscribe(0, false, false, server.eventFilter(&account.Identity{UserID: "u-1", Role: account.RoleUser}))
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeFiltered()
	fast, unsubscribeFast, err := broker.subscribe(0, false, false, nil)
	if err != nil {
		unsubscribeFiltered()
		t.Fatal(err)
	}
	defer unsubscribeFast()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- broker.Emit(ctx, ipc.Event{Event: ipc.TopicConnectProgress, Payload: ipc.ConnectProgressEvent{AssetID: "asset-1"}})
	}()
	select {
	case <-filtering:
	case <-ctx.Done():
		t.Fatal("first Emit did not reach the ownership lookup")
	}

	unsubscribeFiltered()
	secondResult := make(chan error, 1)
	go func() {
		secondResult <- broker.Emit(ctx, ipc.Event{Event: ipc.TopicConnectProgress, Payload: ipc.ConnectProgressEvent{AssetID: "asset-2"}})
	}()
	select {
	case err := <-secondResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		release()
		<-firstResult
		<-secondResult
		t.Fatal("second Dispatch remained blocked by the slow ownership filter")
	}
	release()
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}

	received := drainReplay(t, fast, 2)
	if received[0].ID != 1 || received[1].ID != 2 {
		t.Fatalf("fast subscriber IDs = [%d %d], want [1 2]", received[0].ID, received[1].ID)
	}
	select {
	case <-filtered.done:
	default:
		t.Fatal("filtered subscriber was not unsubscribed")
	}
}

func TestEventBrokerConcurrentSubscribeUnsubscribeDispatch(t *testing.T) {
	broker := NewEventBroker()
	broker.QueueSize = 16
	defer func() { _ = broker.Close() }()
	observer, unsubscribeObserver, err := broker.subscribe(0, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeObserver()
	for seq := 1; seq <= 3; seq++ {
		emitSeq(t, broker, ipc.TopicSessionStatus, seq)
	}
	for seq := 1; seq <= 3; seq++ {
		eventTestRequireSeq(t, observer, seq, seq)
	}

	replayFiltering := make(chan struct{})
	releaseReplay := make(chan struct{})
	liveFiltering := make(chan struct{})
	releaseLive := make(chan struct{})
	var replayOnce sync.Once
	var liveOnce sync.Once
	releaseReplayFilter := func() { replayOnce.Do(func() { close(releaseReplay) }) }
	releaseLiveFilter := func() { liveOnce.Do(func() { close(releaseLive) }) }
	defer releaseReplayFilter()
	defer releaseLiveFilter()
	filter := func(event ipc.Event) bool {
		payload, ok := event.Payload.(map[string]int)
		if !ok {
			return false
		}
		switch payload["seq"] {
		case 1:
			close(replayFiltering)
			<-releaseReplay
		case 6:
			close(liveFiltering)
			<-releaseLive
		}
		return payload["seq"]%2 == 1
	}
	subscribeResult := make(chan eventSubscribeResult, 1)
	go func() {
		subscriber, unsubscribe, err := broker.subscribe(0, true, false, filter)
		subscribeResult <- eventSubscribeResult{subscriber: subscriber, unsubscribe: unsubscribe, err: err}
	}()
	eventTestAwaitSignal(t, replayFiltering, "subscribe did not reach the replay filter")

	fourthResult := eventTestEmitAsync(broker, 4)
	eventTestRequireSeq(t, observer, 4, 4)
	fifthResult := eventTestEmitAsync(broker, 5)
	eventTestRequireSeq(t, observer, 5, 5)
	select {
	case result := <-subscribeResult:
		t.Fatalf("subscribe returned before the replay filter was released: %v", result.err)
	default:
	}
	releaseReplayFilter()
	var subscribed eventSubscribeResult
	select {
	case subscribed = <-subscribeResult:
	case <-time.After(5 * time.Second):
		t.Fatal("subscribe did not finish after the replay filter was released")
	}
	if subscribed.err != nil {
		t.Fatal(subscribed.err)
	}
	defer subscribed.unsubscribe()
	for _, result := range []<-chan error{fourthResult, fifthResult} {
		if err := eventTestAwaitResult(t, result); err != nil {
			t.Fatal(err)
		}
	}
	for _, seq := range []int{1, 3, 5} {
		eventTestRequireSeq(t, subscribed.subscriber, seq, seq)
	}
	eventTestRequireNoEvent(t, subscribed.subscriber)

	sixthResult := eventTestEmitAsync(broker, 6)
	eventTestAwaitSignal(t, liveFiltering, "live Emit did not reach the blocking filter")
	eventTestRequireSeq(t, observer, 6, 6)
	seventhResult := eventTestEmitAsync(broker, 7)
	eventTestRequireSeq(t, observer, 7, 7)
	eighthResult := eventTestEmitAsync(broker, 8)
	eventTestRequireSeq(t, observer, 8, 8)
	subscribed.unsubscribe()
	select {
	case <-subscribed.subscriber.done:
	default:
		t.Fatal("unsubscribe returned without closing done")
	}
	select {
	case <-subscribed.subscriber.workerDone:
		t.Fatal("worker exited while the live filter was still blocked")
	default:
	}
	releaseLiveFilter()
	for _, result := range []<-chan error{sixthResult, seventhResult, eighthResult} {
		if err := eventTestAwaitResult(t, result); err != nil {
			t.Fatal(err)
		}
	}
	eventTestAwaitWorker(t, subscribed.subscriber)
	eventTestRequireNoEvent(t, subscribed.subscriber)

	ninthResult := eventTestEmitAsync(broker, 9)
	if err := eventTestAwaitResult(t, ninthResult); err != nil {
		t.Fatal(err)
	}
	eventTestRequireSeq(t, observer, 9, 9)
	eventTestRequireNoEvent(t, subscribed.subscriber)
}

func TestEventFilterOwnershipLookupTracksPermissionChanges(t *testing.T) {
	ownerID := "u-1"
	server := &Server{assetOwner: func(context.Context, string) (string, error) {
		return ownerID, nil
	}}
	filter := server.eventFilter(&account.Identity{UserID: "u-1", Role: account.RoleUser})
	event := ipc.Event{Event: ipc.TopicConnectProgress, Payload: ipc.ConnectProgressEvent{AssetID: "asset-1"}}
	if !filter(event) {
		t.Fatal("initial owner was rejected")
	}
	ownerID = "u-2"
	if filter(event) {
		t.Fatal("event was accepted after ownership changed")
	}
	ownerID = "u-1"
	if !filter(event) {
		t.Fatal("restored owner was rejected")
	}
}

func eventTestEmitAsync(broker *EventBroker, seq int) <-chan error {
	result := make(chan error, 1)
	go func() {
		result <- broker.Emit(context.Background(), ipc.Event{Event: ipc.TopicSessionStatus, Payload: map[string]int{"seq": seq}})
	}()
	return result
}

func eventTestAwaitResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Emit did not return")
		return nil
	}
}

func eventTestAwaitSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal(message)
	}
}

func eventTestAwaitWorker(t *testing.T, subscriber *eventSubscription) {
	t.Helper()
	if subscriber.workerDone == nil {
		t.Fatal("subscriber has no filtered worker")
	}
	eventTestAwaitSignal(t, subscriber.workerDone, "subscription worker did not exit")
}

func eventTestRequireSeq(t *testing.T, subscriber *eventSubscription, wantID, wantSeq int) {
	t.Helper()
	envelope := drainReplay(t, subscriber, 1)[0]
	if envelope.ID != uint64(wantID) {
		t.Fatalf("event ID = %d, want %d", envelope.ID, wantID)
	}
	var payload map[string]int
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["seq"] != wantSeq {
		t.Fatalf("event %d payload seq = %d, want %d", wantID, payload["seq"], wantSeq)
	}
}

func eventTestRequireNoEvent(t *testing.T, subscriber *eventSubscription) {
	t.Helper()
	select {
	case data := <-subscriber.queue:
		t.Fatalf("unexpected outbound event: %s", data)
	default:
	}
}
