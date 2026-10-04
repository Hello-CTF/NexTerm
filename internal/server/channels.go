package server

import "context"

type FrameKind uint8

const (
	FrameBinary FrameKind = iota + 1
	FrameJSON
)

type Frame struct {
	Sequence uint64
	Kind     FrameKind
	Data     []byte
}

type ChannelReceiver interface {
	Next(context.Context) (Frame, error)
	Ack(sequence uint64) error
	Close() error
}

type ChannelBinder interface {
	BindChannel(string) (ChannelReceiver, error)
}

type ChannelBinderFunc func(string) (ChannelReceiver, error)

func (f ChannelBinderFunc) BindChannel(id string) (ChannelReceiver, error) {
	return f(id)
}

type ChannelStats struct {
	LiveChannels    int
	PendingChannels int
}

type ChannelStatsFunc func() ChannelStats
