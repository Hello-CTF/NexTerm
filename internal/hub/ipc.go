package hub

import (
	"context"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

type StreamFactory struct {
	Hub *Hub
}

func (f StreamFactory) OpenBinary(ctx context.Context, channel ipc.ChannelRef) (ipc.BinaryStream, error) {
	if f.Hub == nil {
		return nil, ipc.ErrStreamsUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.Hub.Producer(channel.ID)
}

func (f StreamFactory) OpenJSON(ctx context.Context, channel ipc.ChannelRef) (ipc.JSONStream, error) {
	if f.Hub == nil {
		return nil, ipc.ErrStreamsUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.Hub.Producer(channel.ID)
}

var _ ipc.StreamFactory = StreamFactory{}
var _ ipc.BinaryStream = (*Producer)(nil)
var _ ipc.JSONStream = (*Producer)(nil)
