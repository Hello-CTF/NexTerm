package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/hub"
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

func (s *sequencedStream) CloseGracefully() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if graceful, ok := s.stream.(interface{ CloseGracefully() error }); ok {
		return graceful.CloseGracefully()
	}
	return s.stream.Close()
}

func CloseStreamGracefully(stream Stream) error {
	if graceful, ok := stream.(interface{ CloseGracefully() error }); ok {
		return graceful.CloseGracefully()
	}
	return stream.Close()
}

type ipcStream struct {
	stream ipc.JSONStream
}

func (s *ipcStream) Send(ctx context.Context, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return s.stream.SendJSON(ctx, data)
}

func (s *ipcStream) Close() error {
	return s.stream.Close()
}

func (s *ipcStream) CloseGracefully() error {
	if graceful, ok := s.stream.(interface{ CloseGracefully() error }); ok {
		return graceful.CloseGracefully()
	}
	return s.stream.Close()
}

func IPCStreamFactory(factory ipc.StreamFactory) StreamFactory {
	return func(ctx context.Context, channelID, _ string) (Stream, error) {
		stream, err := factory.OpenJSON(ctx, ipc.ChannelRef{ID: channelID})
		if err != nil {
			return nil, err
		}
		return &ipcStream{stream: stream}, nil
	}
}

type tryJSONSender interface {
	TrySendJSON(context.Context, json.RawMessage) error
}

type resilientIPCStream struct {
	dial   func(context.Context) (ipc.JSONStream, error)
	stream ipc.JSONStream
}

func (s *resilientIPCStream) Send(ctx context.Context, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		try, ok := s.stream.(tryJSONSender)
		if !ok {
			return s.stream.SendJSON(ctx, data)
		}
		err = try.TrySendJSON(ctx, data)
		if errors.Is(err, hub.ErrQueueFull) {
			_ = s.stream.Close()
			return nil
		}
		if !errors.Is(err, hub.ErrClosed) {
			return err
		}
		if attempt >= 2 {
			return nil
		}
		reopened, dialErr := s.dial(ctx)
		if dialErr != nil {
			return dialErr
		}
		s.stream = reopened
	}
}

func (s *resilientIPCStream) Close() error {
	return s.stream.Close()
}

func (s *resilientIPCStream) CloseGracefully() error {
	if graceful, ok := s.stream.(interface{ CloseGracefully() error }); ok {
		return graceful.CloseGracefully()
	}
	return s.stream.Close()
}

func ResilientIPCStreamFactory(factory ipc.StreamFactory) StreamFactory {
	return func(ctx context.Context, channelID, _ string) (Stream, error) {
		stream, err := factory.OpenJSON(ctx, ipc.ChannelRef{ID: channelID})
		if err != nil {
			return nil, err
		}
		return &resilientIPCStream{
			dial: func(ctx context.Context) (ipc.JSONStream, error) {
				return factory.OpenJSON(ctx, ipc.ChannelRef{ID: channelID})
			},
			stream: stream,
		}, nil
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

type discardStream struct{}

func (discardStream) Send(context.Context, Event) error { return nil }
func (discardStream) Close() error                      { return nil }
