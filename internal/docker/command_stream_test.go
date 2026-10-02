package docker

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestCombinedCommandOutputPreservesTransportEventOrder(t *testing.T) {
	events := make(chan CommandOutputEvent, 4)
	events <- CommandOutputEvent{Data: []byte("out-1")}
	events <- CommandOutputEvent{Data: []byte("err-1"), Stderr: true}
	events <- CommandOutputEvent{Data: []byte("out-2")}
	events <- CommandOutputEvent{Data: []byte("err-2"), Stderr: true}
	close(events)
	stream := &eventCommandStream{events: events}
	reader := combinedCommandOutput(stream)
	defer reader.Close()
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "out-1err-1out-2err-2" {
		t.Fatalf("ordered output = %q", output)
	}
}

func TestCombinedCommandOutputFallbackIsDeterministic(t *testing.T) {
	for range 20 {
		stream := &dualCommandStream{
			Reader: strings.NewReader("stdout"),
			stderr: strings.NewReader("stderr"),
		}
		reader := combinedCommandOutput(stream)
		output, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		_ = reader.Close()
		if string(output) != "stdoutstderr" {
			t.Fatalf("fallback output = %q", output)
		}
	}
}

type eventCommandStream struct {
	events <-chan CommandOutputEvent
}

func (s *eventCommandStream) NextOutput(ctx context.Context) (CommandOutputEvent, error) {
	select {
	case event, ok := <-s.events:
		if !ok {
			return CommandOutputEvent{}, io.EOF
		}
		return event, nil
	case <-ctx.Done():
		return CommandOutputEvent{}, ctx.Err()
	}
}
func (s *eventCommandStream) Read([]byte) (int, error)                 { return 0, io.EOF }
func (s *eventCommandStream) Write(p []byte) (int, error)              { return len(p), nil }
func (s *eventCommandStream) Close() error                             { return nil }
func (s *eventCommandStream) CloseWrite() error                        { return nil }
func (s *eventCommandStream) Resize(context.Context, uint, uint) error { return nil }
func (s *eventCommandStream) Wait(context.Context) (int, error)        { return 0, nil }

type dualCommandStream struct {
	*strings.Reader
	stderr *strings.Reader
}

func (s *dualCommandStream) Stderr() io.Reader                        { return s.stderr }
func (s *dualCommandStream) Write(p []byte) (int, error)              { return len(p), nil }
func (s *dualCommandStream) Close() error                             { return nil }
func (s *dualCommandStream) CloseWrite() error                        { return nil }
func (s *dualCommandStream) Resize(context.Context, uint, uint) error { return nil }
func (s *dualCommandStream) Wait(context.Context) (int, error)        { return 0, nil }
