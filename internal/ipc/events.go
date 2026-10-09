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
	TopicDeviceStatus      Topic = "device://status"
)

var ErrEventsUnavailable = errors.New("event adapter is not configured")

type LayoutChangedEvent struct {
	Revision int64 `json:"revision"`
}

type AppErrorEvent struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// DeviceStatusEvent 是设备控制通道上下线通知; 只带设备 ID 与在线状态,
// 设备清单本身仍由 /fleet/devices 按 owner/超管边界过滤。
type DeviceStatusEvent struct {
	DeviceID string `json:"deviceId"`
	Online   bool   `json:"online"`
}

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
