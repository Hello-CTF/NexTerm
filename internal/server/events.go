package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

var ErrEventBrokerClosed = errors.New("event broker is closed")
var ErrEventFilterPanic = errors.New("event subscriber filter panicked")

const (
	DefaultEventQueueSize  = 64
	DefaultEventBufferSize = 256
)

type EventBroker struct {
	dispatchMu  sync.Mutex
	slowMu      sync.Mutex
	mu          sync.Mutex
	nextID      uint64
	nextEventID uint64
	subscribers map[uint64]*eventSubscription
	buffer      []bufferedEvent
	bufferHead  int
	bufferCount int
	closed      bool

	QueueSize        int
	BufferSize       int
	OnSlowSubscriber func()
}

type eventSubscription struct {
	id    uint64
	queue chan []byte
	done  chan struct{}
	once  sync.Once
	// filter nil means receive everything; when non-nil, both Emit and replay judge each event,
	// and rejected events are not delivered (e.g. device://status filtered by subscriber identity).
	filter func(ipc.Event) bool

	deliveryMu   sync.Mutex
	deliveryCond *sync.Cond
	deliveries   []*eventDelivery
	workerDone   chan struct{}
}

type eventDelivery struct {
	ctx    context.Context
	event  ipc.Event
	data   []byte
	result *eventDispatchResult
}

type eventDispatchResult struct {
	wait sync.WaitGroup
	mu   sync.Mutex
	err  error
}

func (r *eventDispatchResult) fail(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	if r.err == nil {
		r.err = err
	}
	r.mu.Unlock()
}

func (r *eventDispatchResult) complete(err error) {
	r.fail(err)
	r.wait.Done()
}

func (r *eventDispatchResult) waitFor(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		r.wait.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

type bufferedEvent struct {
	id   uint64
	data []byte
	// event keeps the original event for re-judging against the subscriber's filter during replay.
	event ipc.Event
}

func NewEventBroker() *EventBroker {
	return &EventBroker{subscribers: make(map[uint64]*eventSubscription)}
}

func ParseEventQueueEnv(getenv func(string) string) (int, error) {
	return parsePositiveEnv(getenv, "NEXTERM_EVENT_QUEUE_SIZE")
}

func ParseEventBufferEnv(getenv func(string) string) (int, error) {
	return parsePositiveEnv(getenv, "NEXTERM_EVENT_BUFFER_SIZE")
}

func parsePositiveEnv(getenv func(string) string, key string) (int, error) {
	raw := getenv(key)
	if raw == "" {
		return 0, nil
	}
	size, err := strconv.Atoi(raw)
	if err != nil || size <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", key, raw)
	}
	return size, nil
}

func (b *EventBroker) Emit(ctx context.Context, event ipc.Event) error {
	if ownerID := ipc.EventOwnerID(ctx); ownerID != "" {
		event.OwnerID = ownerID
	} else if ownerID, ok := ipc.UserIDFromContext(ctx); ok {
		event.OwnerID = ownerID
	}
	// Keep event IDs and per-subscriber insertion ordered, but never wait for a filter here.
	b.dispatchMu.Lock()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		b.dispatchMu.Unlock()
		return ErrEventBrokerClosed
	}
	if err := ctx.Err(); err != nil {
		b.mu.Unlock()
		b.dispatchMu.Unlock()
		return err
	}
	b.nextEventID++
	id := b.nextEventID
	data, err := json.Marshal(struct {
		ID      uint64    `json:"id"`
		Event   ipc.Topic `json:"event"`
		Payload any       `json:"payload"`
	}{ID: id, Event: event.Event, Payload: event.Payload})
	if err != nil {
		b.nextEventID--
		b.mu.Unlock()
		b.dispatchMu.Unlock()
		return err
	}
	b.appendBufferLocked(id, event, data)
	subscribers := make([]*eventSubscription, 0, len(b.subscribers))
	for _, subscriber := range b.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	var result *eventDispatchResult
	var directErr error
	var dropped []*eventSubscription
	stopped := false
	for _, subscriber := range subscribers {
		if subscriber.filter == nil {
			select {
			case subscriber.queue <- data:
			case <-subscriber.done:
			case <-ctx.Done():
				directErr = ctx.Err()
				stopped = true
			default:
				delete(b.subscribers, subscriber.id)
				dropped = append(dropped, subscriber)
			}
			if stopped {
				break
			}
			continue
		}
		if result == nil {
			result = &eventDispatchResult{}
		}
		delivery := &eventDelivery{ctx: ctx, event: event, data: data, result: result}
		result.wait.Add(1)
		enqueued, overflow := subscriber.enqueue(delivery)
		switch {
		case overflow:
			// The pending backlog exceeded its bound: drop the subscriber so its
			// client resyncs; the event stays buffered for the resync.
			delete(b.subscribers, subscriber.id)
			dropped = append(dropped, subscriber)
			result.complete(nil)
		case !enqueued:
			if b.closed {
				result.complete(ErrEventBrokerClosed)
			} else {
				result.complete(nil)
			}
		}
	}
	b.mu.Unlock()
	b.dispatchMu.Unlock()
	for _, subscriber := range dropped {
		b.drop(subscriber)
		b.notifySlowSubscriber()
	}
	if result == nil {
		return directErr
	}
	result.fail(directErr)
	return result.waitFor(ctx)
}

func (b *EventBroker) appendBufferLocked(id uint64, event ipc.Event, data []byte) {
	size := b.BufferSize
	if size <= 0 {
		size = DefaultEventBufferSize
	}
	if len(b.buffer) != size {
		retained := b.bufferCount
		if retained > size {
			retained = size
		}
		buffer := make([]bufferedEvent, size)
		for i := 0; i < retained; i++ {
			buffer[i] = b.buffer[(b.bufferHead+b.bufferCount-retained+i)%len(b.buffer)]
		}
		b.buffer = buffer
		b.bufferHead = 0
		b.bufferCount = retained
	}
	index := (b.bufferHead + b.bufferCount) % size
	if b.bufferCount == size {
		b.bufferHead = (b.bufferHead + 1) % size
	} else {
		b.bufferCount++
	}
	b.buffer[index] = bufferedEvent{id: id, data: data, event: event}
}

func (b *EventBroker) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}

func (b *EventBroker) subscribe(since uint64, present, resync bool, filter func(ipc.Event) bool) (*eventSubscription, func(), error) {
	b.dispatchMu.Lock()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		b.dispatchMu.Unlock()
		return nil, nil, ErrEventBrokerClosed
	}
	b.nextID++
	id := b.nextID
	size := b.QueueSize
	if size <= 0 {
		size = DefaultEventQueueSize
	}
	subscriber := &eventSubscription{id: id, queue: make(chan []byte, size), done: make(chan struct{}), filter: filter}
	if filter != nil {
		subscriber.deliveryCond = sync.NewCond(&subscriber.deliveryMu)
		subscriber.workerDone = make(chan struct{})
	}
	b.subscribers[id] = subscriber
	markedResync := resync
	var replay []bufferedEvent
	if !markedResync && present {
		switch {
		case since > b.nextEventID:
			markedResync = true
		case since < b.nextEventID:
			missed := b.nextEventID - since
			if missed > uint64(b.bufferCount) || b.buffer[b.bufferHead].id > since+1 || missed > uint64(size) {
				markedResync = true
			} else {
				for i := 0; i < b.bufferCount; i++ {
					event := b.buffer[(b.bufferHead+i)%len(b.buffer)]
					if event.id > since {
						replay = append(replay, event)
					}
				}
			}
		}
	}
	replayResult := &eventDispatchResult{}
	if markedResync {
		subscriber.queue <- b.resyncMarkerLocked()
	} else if filter == nil {
		for _, buffered := range replay {
			subscriber.queue <- buffered.data
		}
	} else {
		for _, buffered := range replay {
			delivery := &eventDelivery{ctx: context.Background(), event: buffered.event, data: buffered.data, result: replayResult}
			replayResult.wait.Add(1)
			if enqueued, _ := subscriber.enqueue(delivery); !enqueued {
				replayResult.complete(nil)
			}
		}
	}
	b.mu.Unlock()
	b.dispatchMu.Unlock()
	if filter != nil {
		go b.runSubscription(subscriber)
	}
	if markedResync || filter == nil {
		return subscriber, func() { b.drop(subscriber) }, nil
	}
	if err := replayResult.waitFor(context.Background()); err != nil {
		b.drop(subscriber)
		return nil, nil, err
	}
	b.mu.Lock()
	active := !b.closed && b.subscribers[id] == subscriber
	b.mu.Unlock()
	if !active {
		b.drop(subscriber)
		return nil, nil, ErrEventBrokerClosed
	}
	return subscriber, func() { b.drop(subscriber) }, nil
}

// enqueue returns (enqueued, overflow). overflow means the pending backlog
// exceeded its bound (twice the queue capacity, which also absorbs a full
// replay): the caller drops the subscriber so its client resyncs, while the
// event stays buffered for the resync.
func (s *eventSubscription) enqueue(delivery *eventDelivery) (bool, bool) {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	if eventSubscriptionStopped(s.done) {
		return false, false
	}
	if len(s.deliveries) >= 2*cap(s.queue) {
		return false, true
	}
	s.deliveries = append(s.deliveries, delivery)
	s.deliveryCond.Signal()
	return true, false
}

func eventSubscriptionStopped(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func (b *EventBroker) runSubscription(subscriber *eventSubscription) {
	defer close(subscriber.workerDone)
	for {
		subscriber.deliveryMu.Lock()
		for len(subscriber.deliveries) == 0 {
			if eventSubscriptionStopped(subscriber.done) {
				subscriber.deliveryMu.Unlock()
				return
			}
			subscriber.deliveryCond.Wait()
		}
		if eventSubscriptionStopped(subscriber.done) {
			pending := subscriber.deliveries
			subscriber.deliveries = nil
			subscriber.deliveryMu.Unlock()
			b.completeInactive(pending...)
			return
		}
		delivery := subscriber.deliveries[0]
		subscriber.deliveries[0] = nil
		subscriber.deliveries = subscriber.deliveries[1:]
		if len(subscriber.deliveries) == 0 {
			subscriber.deliveries = nil
		}
		subscriber.deliveryMu.Unlock()
		result := b.deliver(subscriber, delivery)
		if errors.Is(result, ErrEventFilterPanic) {
			b.drop(subscriber)
			subscriber.deliveryMu.Lock()
			pendingDeliveries := subscriber.deliveries
			subscriber.deliveries = nil
			subscriber.deliveryMu.Unlock()
			delivery.result.complete(result)
			for _, pending := range pendingDeliveries {
				pending.result.complete(result)
			}
			return
		}
		delivery.result.complete(result)
	}
}

func (b *EventBroker) inactiveError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrEventBrokerClosed
	}
	return nil
}

func (b *EventBroker) completeInactive(deliveries ...*eventDelivery) {
	err := b.inactiveError()
	for _, delivery := range deliveries {
		delivery.result.complete(err)
	}
}

func evaluateEventFilter(filter func(ipc.Event) bool, event ipc.Event) (accepted bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			accepted = false
			err = fmt.Errorf("%w: %v", ErrEventFilterPanic, recovered)
		}
	}()
	return filter(event), nil
}

func (b *EventBroker) deliver(subscriber *eventSubscription, delivery *eventDelivery) error {
	if eventSubscriptionStopped(subscriber.done) {
		return b.inactiveError()
	}
	// Ownership filters may query the DB; no broker lock is held while they run.
	if subscriber.filter != nil {
		accepted, err := evaluateEventFilter(subscriber.filter, delivery.event)
		if err != nil {
			return err
		}
		if !accepted {
			return nil
		}
	}
	var result error
	slow := false
	// A delivery whose producer context is already canceled is dropped
	// deterministically instead of racing the queue push against ctx.Done.
	if err := delivery.ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	switch {
	case b.closed:
		result = ErrEventBrokerClosed
	case b.subscribers[subscriber.id] != subscriber:
	default:
		select {
		case subscriber.queue <- delivery.data:
		case <-subscriber.done:
		case <-delivery.ctx.Done():
			result = delivery.ctx.Err()
		default:
			delete(b.subscribers, subscriber.id)
			slow = true
		}
	}
	b.mu.Unlock()
	if slow {
		b.drop(subscriber)
		b.notifySlowSubscriber()
	}
	return result
}

func (b *EventBroker) notifySlowSubscriber() {
	if b.OnSlowSubscriber == nil {
		return
	}
	b.slowMu.Lock()
	defer b.slowMu.Unlock()
	b.OnSlowSubscriber()
}

func (b *EventBroker) resyncMarkerLocked() []byte {
	data, _ := json.Marshal(struct {
		Resync bool   `json:"resync"`
		ID     uint64 `json:"id"`
	}{Resync: true, ID: b.nextEventID})
	return data
}

func (b *EventBroker) drop(subscriber *eventSubscription) bool {
	dropped := false
	subscriber.once.Do(func() {
		dropped = true
		b.mu.Lock()
		delete(b.subscribers, subscriber.id)
		b.mu.Unlock()
		close(subscriber.done)
		subscriber.deliveryMu.Lock()
		if subscriber.deliveryCond != nil {
			subscriber.deliveryCond.Broadcast()
		}
		subscriber.deliveryMu.Unlock()
	})
	return dropped
}

func (b *EventBroker) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	subscribers := b.subscribers
	b.subscribers = make(map[uint64]*eventSubscription)
	b.mu.Unlock()
	for _, subscriber := range subscribers {
		b.drop(subscriber)
	}
	return nil
}

var _ ipc.Emitter = (*EventBroker)(nil)
