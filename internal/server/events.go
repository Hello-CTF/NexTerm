package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

var ErrEventBrokerClosed = errors.New("event broker is closed")

type EventBroker struct {
	mu          sync.Mutex
	nextID      uint64
	subscribers map[uint64]*eventSubscription
	closed      bool
}

type eventSubscription struct {
	queue chan []byte
	done  chan struct{}
	once  sync.Once
}

func NewEventBroker() *EventBroker {
	return &EventBroker{subscribers: make(map[uint64]*eventSubscription)}
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
	subscriber := &eventSubscription{queue: make(chan []byte, 64), done: make(chan struct{})}
	b.subscribers[id] = subscriber
	return subscriber, func() {
		subscriber.once.Do(func() {
			b.mu.Lock()
			delete(b.subscribers, id)
			b.mu.Unlock()
			close(subscriber.done)
		})
	}, nil
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
		subscriber.once.Do(func() { close(subscriber.done) })
	}
	return nil
}

var _ ipc.Emitter = (*EventBroker)(nil)
