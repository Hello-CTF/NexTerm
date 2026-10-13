package ipc

import (
	"context"
	"errors"
)

type Topic string

const (
	TopicSessionStatus     Topic = "session://status"
	TopicConnectProgress   Topic = "session://connect-progress"
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
	TopicUpdateProgress    Topic = "update://progress"
	TopicAINotification    Topic = "ai://notification"
)

var ErrEventsUnavailable = errors.New("event adapter is not configured")

type LayoutChangedEvent struct {
	Revision int64 `json:"revision"`
}

// AINotificationEvent is an in-app notification emitted by the AI notify tool; OwnerID is the session owner,
// the server filters by subscriber identity (empty owner is not delivered), and the desktop delivers locally when it has no identity.
type AINotificationEvent struct {
	OwnerID string `json:"ownerId"`
	Text    string `json:"text"`
}

// SyncStatusEvent carries the owning account of a sync change when the emitter knows it; an empty OwnerID
// means unscoped (legacy emitters) and passes the server event filter for every session.
type SyncStatusEvent struct {
	OwnerID string `json:"ownerId,omitempty"`
}

type AppErrorEvent struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ConnectProgressEvent struct {
	AssetID string `json:"assetId"`
	Stage   string `json:"stage"`
	Hop     int    `json:"hop"`
	Hops    int    `json:"hops"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
}

// DeviceStatusEvent is the device control channel's online/offline notification; it carries only the device ID and online state,
// the device list itself is still filtered by /fleet/devices along the owner/admin boundary.
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
