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
	mu          sync.Mutex
	nextID      uint64
	nextEventID uint64
	subscribers map[uint64]*eventSubscription
	buffer      []bufferedEvent
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
	// filter 为 nil 表示全量接收; 非 nil 时 Emit 与重放都逐事件判定,
	// 被拒事件不下发 (如 device://status 按订阅者身份过滤)。
	filter func(ipc.Event) bool
}

type bufferedEvent struct {
	id   uint64
	data []byte
	// event 保留原始事件, 供订阅重放时按订阅者 filter 重新判定。
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
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrEventBrokerClosed
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
		return err
	}
	b.appendBufferLocked(id, event, data)
	subscribers := make([]*eventSubscription, 0, len(b.subscribers))
	for _, subscriber := range b.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	b.mu.Unlock()

	for _, subscriber := range subscribers {
		if subscriber.filter != nil && !subscriber.filter(event) {
			continue
		}
		select {
		case subscriber.queue <- data:
		case <-subscriber.done:
		case <-ctx.Done():
			return ctx.Err()
		default:
			if b.drop(subscriber) && b.OnSlowSubscriber != nil {
				b.OnSlowSubscriber()
			}
		}
	}
	return nil
}

func (b *EventBroker) appendBufferLocked(id uint64, event ipc.Event, data []byte) {
	size := b.BufferSize
	if size <= 0 {
		size = DefaultEventBufferSize
	}
	b.buffer = append(b.buffer, bufferedEvent{id: id, data: data, event: event})
	if len(b.buffer) > size {
		b.buffer = append([]bufferedEvent(nil), b.buffer[len(b.buffer)-size:]...)
	}
}

func (b *EventBroker) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}

func (b *EventBroker) subscribe(since uint64, present, resync bool, filter func(ipc.Event) bool) (*eventSubscription, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
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
	if resync {
		subscriber.queue <- b.resyncMarkerLocked()
		return subscriber, func() { b.drop(subscriber) }, nil
	}
	if present {
		switch {
		case since > b.nextEventID:
			subscriber.queue <- b.resyncMarkerLocked()
		case since < b.nextEventID:
			missed := b.nextEventID - since
			if missed > uint64(len(b.buffer)) || b.buffer[0].id > since+1 || missed > uint64(size) {
				subscriber.queue <- b.resyncMarkerLocked()
				break
			}
			for _, event := range b.buffer {
				if event.id > since && (filter == nil || filter(event.event)) {
					subscriber.queue <- event.data
				}
			}
		}
	}
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
