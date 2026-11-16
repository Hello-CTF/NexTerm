package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

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

// ErrWriteStalled reports a channel write the caller stopped waiting for, or
// one refused because an earlier abandoned write is still blocked in the
// transport. The abandoned bytes may still reach the terminal later, so the
// caller must not retry blindly.
var ErrWriteStalled = errors.New("terminal input write is blocked: the terminal is not accepting input; earlier input may still be delivered later, do not retry blindly")

// stalledWriteWait bounds how long a write waits behind an abandoned write
// that is still blocked in the transport before it fails with
// ErrWriteStalled, so a stalled channel never parks later writers (and the
// tab write gate they hold) indefinitely.
var stalledWriteWait = 5 * time.Second

// writeChannelContext writes data to the channel honoring ctx. Channels with
// the context-write capability abort the write itself; a cancelled context
// write terminates the underlying stream (reattach-only, per the supervisor
// contract): the unconfirmed bytes are never replayed, so the caller must not
// retry blindly. Other channels cannot abort a blocked Write, so the caller
// stops waiting for it instead (see writeBlocking).
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
	return channel.writeBlocking(ctx, data)
}

// writeBlocking performs a plain blocking channel write that the caller stops
// waiting for when ctx ends. The transport Write cannot be recalled, so it
// keeps running and still delivers its bytes if the channel recovers; until
// it returns, later writes wait for it (bounded by their context and
// stalledWriteWait) so input is never interleaved into its remainder.
func (c *channelHandle) writeBlocking(ctx context.Context, data []byte) error {
	done, err := c.beginWrite(ctx)
	if err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		// Nothing was sent: report the plain context error, not a stall.
		c.endWrite(done)
		return err
	}
	if ctx.Done() == nil {
		err := writeAll(c.Channel, data)
		c.endWrite(done)
		return convertWriteResult(err)
	}
	result := make(chan error, 1)
	go func() {
		err := writeAll(c.Channel, data)
		c.endWrite(done)
		result <- err
	}()
	select {
	case err := <-result:
		return convertWriteResult(err)
	case <-ctx.Done():
	}
	select {
	case err := <-result:
		return convertWriteResult(err)
	default:
		return fmt.Errorf("%w: %w", ErrWriteStalled, context.Cause(ctx))
	}
}

// beginWrite claims the channel for one blocking write, first waiting for a
// write that is still in flight.
func (c *channelHandle) beginWrite(ctx context.Context) (chan struct{}, error) {
	var expired <-chan time.Time
	for {
		c.writeMu.Lock()
		previous := c.writing
		if previous == nil {
			done := make(chan struct{})
			c.writing = done
			c.writeMu.Unlock()
			return done, nil
		}
		c.writeMu.Unlock()
		if expired == nil {
			timer := time.NewTimer(stalledWriteWait)
			defer timer.Stop()
			expired = timer.C
		}
		select {
		case <-previous:
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %w", ErrWriteStalled, context.Cause(ctx))
		case <-expired:
			return nil, ErrWriteStalled
		}
	}
}

func (c *channelHandle) endWrite(done chan struct{}) {
	c.writeMu.Lock()
	if c.writing == done {
		c.writing = nil
	}
	c.writeMu.Unlock()
	close(done)
}

func convertWriteResult(err error) error {
	if err != nil {
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
