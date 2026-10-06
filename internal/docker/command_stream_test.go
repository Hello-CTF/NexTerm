package docker

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func TestCombinedCommandOutputPreservesTransportEventOrder(t *testing.T) {
	events := make(chan base.OutputEvent, 4)
	events <- base.OutputEvent{Data: []byte("out-1")}
	events <- base.OutputEvent{Data: []byte("err-1"), Stderr: true}
	events <- base.OutputEvent{Data: []byte("out-2")}
	events <- base.OutputEvent{Data: []byte("err-2"), Stderr: true}
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

func TestCombinedCommandOutputDeliversBothStreams(t *testing.T) {
	events := make(chan base.OutputEvent, 2)
	events <- base.OutputEvent{Data: []byte("stdout")}
	events <- base.OutputEvent{Data: []byte("stderr"), Stderr: true}
	close(events)
	stream := &eventCommandStream{events: events}
	reader := combinedCommandOutput(stream)
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	if string(output) != "stdoutstderr" {
		t.Fatalf("combined output = %q", output)
	}
}

func TestCombinedCommandOutputDeliversStderrEventPromptly(t *testing.T) {
	events := make(chan base.OutputEvent)
	stream := &eventCommandStream{events: events}
	reader := combinedCommandOutput(stream)
	defer reader.Close()
	type readResult struct {
		output string
		err    error
	}
	result := make(chan readResult, 1)
	go func() {
		buffer := make([]byte, 16)
		count, err := reader.Read(buffer)
		result <- readResult{output: string(buffer[:count]), err: err}
	}()
	go func() { events <- base.OutputEvent{Data: []byte("ready"), Stderr: true} }()
	select {
	case got := <-result:
		if got.err != nil || got.output != "ready" {
			t.Fatalf("read = %q, %v", got.output, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stderr event starved")
	}
}

type eventCommandStream struct {
	events <-chan base.OutputEvent
}

func (s *eventCommandStream) NextOutput(ctx context.Context) (base.OutputEvent, error) {
	select {
	case event, ok := <-s.events:
		if !ok {
			return base.OutputEvent{}, io.EOF
		}
		return event, nil
	case <-ctx.Done():
		return base.OutputEvent{}, ctx.Err()
	}
}
func (s *eventCommandStream) Read([]byte) (int, error)                 { return 0, io.EOF }
func (s *eventCommandStream) Write(p []byte) (int, error)              { return len(p), nil }
func (s *eventCommandStream) Close() error                             { return nil }
func (s *eventCommandStream) CloseWrite() error                        { return nil }
func (s *eventCommandStream) Resize(context.Context, uint, uint) error { return nil }
func (s *eventCommandStream) Wait(context.Context) (int, error)        { return 0, nil }
