package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

var ErrEventBrokerClosed = errors.New("event broker is closed")

const DefaultEventQueueSize = 64

type EventBroker struct {
	mu          sync.Mutex
	nextID      uint64
	subscribers map[uint64]*eventSubscription
	closed      bool

	QueueSize        int
	OnSlowSubscriber func()
}

type eventSubscription struct {
	id    uint64
	queue chan []byte
	done  chan struct{}
	once  sync.Once
}

func NewEventBroker() *EventBroker {
	return &EventBroker{subscribers: make(map[uint64]*eventSubscription)}
}

func ParseEventQueueEnv(getenv func(string) string) (int, error) {
	raw := getenv("NEXTERM_EVENT_QUEUE_SIZE")
	if raw == "" {
		return 0, nil
	}
	size, err := strconv.Atoi(raw)
	if err != nil || size <= 0 {
		return 0, fmt.Errorf("NEXTERM_EVENT_QUEUE_SIZE must be a positive integer, got %q", raw)
	}
	return size, nil
}

func (b *EventBroker) Emit(ctx context.Context, event ipc.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrEventBrokerClosed
	}
	subscribers := make([]*eventSubscription, 0, len(b.subscribers))
	for _, subscriber := range b.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	b.mu.Unlock()

	for _, subscriber := range subscribers {
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

func (b *EventBroker) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}

func (b *EventBroker) subscribe() (*eventSubscription, func(), error) {
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
	subscriber := &eventSubscription{id: id, queue: make(chan []byte, size), done: make(chan struct{})}
	b.subscribers[id] = subscriber
	return subscriber, func() { b.drop(subscriber) }, nil
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
