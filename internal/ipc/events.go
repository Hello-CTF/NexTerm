package ipc

import (
	"context"
	"errors"
)

type Topic string

const (
	TopicSessionStatus     Topic = "session://status"
	TopicTerminalExit      Topic = "terminal://exit"
	TopicTerminalThrottled Topic = "terminal://throttled"
	TopicTerminalControl   Topic = "terminal://control"
	TopicFSProgress        Topic = "fs://progress"
	TopicDockerStats       Topic = "docker://stats"
	TopicAIEvent           Topic = "ai://event"
	TopicSyncStatus        Topic = "sync://status"
	TopicAppError          Topic = "app://error"
	TopicLayoutChanged     Topic = "layout://changed"
)

var ErrEventsUnavailable = errors.New("event adapter is not configured")

type Event struct {
	Event   Topic `json:"event"`
	Payload any   `json:"payload"`
}

type Emitter interface {
	Emit(context.Context, Event) error
}

type EmitterFunc func(context.Context, Event) error

func (f EmitterFunc) Emit(ctx context.Context, event Event) error {
	return f(ctx, event)
}

func Emit[T any](ctx context.Context, emitter Emitter, topic Topic, payload T) error {
	if emitter == nil {
		return ErrEventsUnavailable
	}
	return emitter.Emit(ctx, Event{Event: topic, Payload: payload})
}
