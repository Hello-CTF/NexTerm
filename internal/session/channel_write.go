package session

import (
	"context"
	"errors"
	"io"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// channelContextWriter is the optional capability of a channel whose writes
// honor caller cancellation (supervisor streams); channels without it keep
// the plain blocking Write behavior.
type channelContextWriter interface {
	WriteContext(context.Context, []byte) (int, error)
}

// writeChannelContext writes data to the channel honoring ctx when the
// channel supports context writes; channels without the capability keep the
// plain blocking write. A cancelled context write terminates the underlying
// stream (reattach-only, per the supervisor contract): the unconfirmed
// bytes are never replayed, so the caller must not retry blindly.
func writeChannelContext(ctx context.Context, channel *channelHandle, data []byte) error {
	if writer, ok := channel.Channel.(channelContextWriter); ok {
		for len(data) > 0 {
			written, err := writer.WriteContext(ctx, data)
			if err != nil {
				return convertChannelWriteError(err)
			}
			if written <= 0 {
				return io.ErrShortWrite
			}
			data = data[written:]
		}
		return nil
	}
	return writeAll(channel, data)
}

// convertChannelWriteError maps channel write failures onto session
// sentinels: a disconnected channel surfaces as ErrDisconnected and a
// remotely closed durable stream as ErrTabClosed; caller cancellation and
// every other failure pass through with their evidence intact.
func convertChannelWriteError(err error) error {
	switch {
	case errors.Is(err, base.ErrDisconnected):
		return ErrDisconnected
	case errors.Is(err, durable.ErrClosed), errors.Is(err, supervisor.ErrClosed):
		return ErrTabClosed
	}
	return err
}
