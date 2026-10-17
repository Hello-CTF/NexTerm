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

const (
	DefaultEventQueueSize  = 64
	DefaultEventBufferSize = 256
)

type EventBroker struct {
	dispatchMu  sync.Mutex
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
	b.mu.Unlock()

	var dropped []*eventSubscription
	var result error
	for _, subscriber := range subscribers {
		if subscriber.filter != nil && !subscriber.filter(event) {
			continue
		}
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			result = ErrEventBrokerClosed
			break
		}
		if b.subscribers[subscriber.id] != subscriber {
			b.mu.Unlock()
			continue
		}
		select {
		case subscriber.queue <- data:
		case <-subscriber.done:
		case <-ctx.Done():
			result = ctx.Err()
		default:
			delete(b.subscribers, subscriber.id)
			dropped = append(dropped, subscriber)
		}
		b.mu.Unlock()
		if result != nil {
			break
		}
	}
	b.dispatchMu.Unlock()
	for _, subscriber := range dropped {
		b.drop(subscriber)
	}
	for range dropped {
		if b.OnSlowSubscriber != nil {
			b.OnSlowSubscriber()
		}
	}
	return result
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
	if markedResync {
		subscriber.queue <- b.resyncMarkerLocked()
	}
	b.mu.Unlock()
	if markedResync {
		b.dispatchMu.Unlock()
		return subscriber, func() { b.drop(subscriber) }, nil
	}
	if filter != nil {
		filtered := replay[:0]
		for _, event := range replay {
			if filter(event.event) {
				filtered = append(filtered, event)
			}
		}
		replay = filtered
	}
	b.mu.Lock()
	if b.closed || b.subscribers[id] != subscriber {
		b.mu.Unlock()
		b.drop(subscriber)
		b.dispatchMu.Unlock()
		return nil, nil, ErrEventBrokerClosed
	}
	for _, event := range replay {
		subscriber.queue <- event.data
	}
	b.mu.Unlock()
	b.dispatchMu.Unlock()
	return subscriber, func() { b.drop(subscriber) }, nil
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
