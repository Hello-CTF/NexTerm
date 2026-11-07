package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

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

// channelWindowedWriter is the optional capability of a channel that can put
// input on the wire without waiting for the host's acknowledgement
// (supervisor streams). A rejection of earlier input is reported by the next
// write instead.
type channelWindowedWriter interface {
	WriteWindowed(context.Context, []byte) (int, error)
}

// writeChannelInput sends interactive keystrokes. Waiting for the host to
// acknowledge each batch would hold the next keystroke for a full round trip
// behind the previous one, so pipelining channels only wait for window space;
// other channels fall back to writeChannelContext.
func writeChannelInput(ctx context.Context, channel *channelHandle, data []byte) error {
	writer, ok := channel.Channel.(channelWindowedWriter)
	if !ok {
		return writeChannelContext(ctx, channel, data)
	}
	for len(data) > 0 {
		written, err := writer.WriteWindowed(ctx, data)
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
	if err := writeAll(channel, data); err != nil {
		return convertChannelWriteError(err)
	}
	return nil
}

// convertChannelWriteError maps channel write failures onto session
// sentinels. A write that fails because the stream or connection is closed,
// cancelled or cut mid-frame is a transport failure the tab recovers from by
// reattaching, so it surfaces as ErrDisconnected: our own pump closes the
// stream when it hands over to a reconnect, which is indistinguishable from a
// remote close here. Only a process the host reports as exited is a finished
// terminal (ErrTabClosed); callers that destroyed the tab report ErrTabClosed
// before they write.
func convertChannelWriteError(err error) error {
	switch {
	case errors.Is(err, base.ErrDisconnected):
		return ErrDisconnected
	case errors.Is(err, durable.ErrExited), errors.Is(err, supervisor.ErrExited):
		return ErrTabClosed
	case errors.Is(err, durable.ErrClosed), errors.Is(err, supervisor.ErrClosed),
		errors.Is(err, base.ErrClosed), errors.Is(err, net.ErrClosed), errors.Is(err, io.ErrClosedPipe),
		errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF),
		errors.Is(err, context.Canceled):
		return fmt.Errorf("%w: %w", ErrDisconnected, err)
	}
	return err
}
