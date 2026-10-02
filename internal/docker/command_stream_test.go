package docker

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
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

func TestCombinedCommandOutputFallbackDeliversBothStreams(t *testing.T) {
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
		if string(output) != "stdoutstderr" && string(output) != "stderrstdout" {
			t.Fatalf("fallback output = %q", output)
		}
	}
}

func TestCombinedCommandOutputStderrReadyWhileStdoutBlocked(t *testing.T) {
	stdout, stdoutWriter := io.Pipe()
	stderr, stderrWriter := io.Pipe()
	stream := &pipeCommandStream{ReadCloser: stdout, stderr: stderr}
	reader := combinedCommandOutput(stream)
	defer func() {
		_ = reader.Close()
		_ = stdoutWriter.Close()
		_ = stderrWriter.Close()
	}()
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
	go func() { _, _ = stderrWriter.Write([]byte("ready")) }()
	select {
	case got := <-result:
		if got.err != nil || got.output != "ready" {
			t.Fatalf("read = %q, %v", got.output, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stderr starved behind open stdout")
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

type pipeCommandStream struct {
	io.ReadCloser
	stderr io.ReadCloser
}

func (s *pipeCommandStream) Stderr() io.Reader                        { return s.stderr }
func (s *pipeCommandStream) Write(p []byte) (int, error)              { return len(p), nil }
func (s *pipeCommandStream) CloseWrite() error                        { return nil }
func (s *pipeCommandStream) Resize(context.Context, uint, uint) error { return nil }
func (s *pipeCommandStream) Wait(context.Context) (int, error)        { return 0, nil }
func (s *pipeCommandStream) Close() error {
	_ = s.ReadCloser.Close()
	return s.stderr.Close()
}
