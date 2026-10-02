package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
)

type Stream interface {
	Send(context.Context, Event) error
	Close() error
}

type StreamFactory func(context.Context, string, string) (Stream, error)

type sequencedStream struct {
	mu     sync.Mutex
	stream Stream
	seq    uint64
}

func WithEventSequence(stream Stream) Stream {
	return &sequencedStream{stream: stream}
}

func (s *sequencedStream) Send(ctx context.Context, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	event.Seq = s.seq
	return s.stream.Send(ctx, event)
}

func (s *sequencedStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Close()
}

type ipcStream struct {
	stream *ipc.TypedJSONStream[Event]
}

func (s *ipcStream) Send(ctx context.Context, event Event) error {
	return s.stream.Send(ctx, event)
}

func (s *ipcStream) Close() error {
	return s.stream.Close()
}

func IPCStreamFactory(factory ipc.StreamFactory) StreamFactory {
	return func(ctx context.Context, channelID, _ string) (Stream, error) {
		stream, err := ipc.OpenTypedJSONStream[Event](ctx, factory, ipc.ChannelRef{ID: channelID})
		if err != nil {
			return nil, err
		}
		return &ipcStream{stream: stream}, nil
	}
}

type SliceStream struct {
	mu     sync.Mutex
	Events []Event
	Closed bool
}

func (s *SliceStream) Send(_ context.Context, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Closed {
		return errors.New("stream closed")
	}
	s.Events = append(s.Events, event)
	return nil
}

func (s *SliceStream) Close() error {
	s.mu.Lock()
	s.Closed = true
	s.mu.Unlock()
	return nil
}

func (s *SliceStream) Snapshot() ([]Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.Events...), s.Closed
}

func StaticStream(stream Stream) StreamFactory {
	return func(context.Context, string, string) (Stream, error) {
		return stream, nil
	}
}
