package server

import (
	"context"
	"fmt"

	"github.com/ProbiusOfficial/NexTerm/internal/hub"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

type HubAdapter struct {
	Hub *hub.Hub
}

func NewHubAdapter(shared *hub.Hub) *HubAdapter {
	return &HubAdapter{Hub: shared}
}

func (a *HubAdapter) BindChannel(id string) (ChannelReceiver, error) {
	if a == nil || a.Hub == nil {
		return nil, ipc.ErrStreamsUnavailable
	}
	receiver, err := a.Hub.Bind(id)
	if err != nil {
		return nil, err
	}
	return &hubChannelReceiver{receiver: receiver}, nil
}

func (a *HubAdapter) Stats() ChannelStats {
	if a == nil || a.Hub == nil {
		return ChannelStats{}
	}
	stats := a.Hub.Stats()
	return ChannelStats{LiveChannels: stats.LiveChannels, PendingChannels: stats.PendingChannels}
}

func (a *HubAdapter) StreamFactory() ipc.StreamFactory {
	if a == nil {
		return ipc.StreamFactoryFuncs{}
	}
	return hub.StreamFactory{Hub: a.Hub}
}

type hubChannelReceiver struct {
	receiver *hub.Receiver
}

func (r *hubChannelReceiver) Next(ctx context.Context) (Frame, error) {
	frame, err := r.receiver.Next(ctx)
	if err != nil {
		return Frame{}, err
	}
	kind := FrameKind(0)
	switch frame.Kind {
	case hub.FrameBinary:
		kind = FrameBinary
	case hub.FrameJSON:
		kind = FrameJSON
	default:
		return Frame{}, fmt.Errorf("unknown hub frame kind %d", frame.Kind)
	}
	return Frame{Sequence: frame.Sequence, Kind: kind, Data: frame.Data}, nil
}

func (r *hubChannelReceiver) Ack(sequence uint64) error {
	return r.receiver.Ack(sequence)
}

func (r *hubChannelReceiver) Close() error {
	return r.receiver.Close()
}

var _ ChannelBinder = (*HubAdapter)(nil)
var _ ChannelReceiver = (*hubChannelReceiver)(nil)
