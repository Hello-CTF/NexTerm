package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/schema"
)

type confirmTransitionStream struct {
	*SliceStream
	emitted chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *confirmTransitionStream) Send(ctx context.Context, event Event) error {
	if event.Type != "confirmRequired" {
		return s.SliceStream.Send(ctx, event)
	}
	if err := s.SliceStream.Send(ctx, event); err != nil {
		return err
	}
	s.once.Do(func() { close(s.emitted) })
	<-s.release
	return nil
}

func TestR1CancelDuringHITLTransitionCompletesExactlyOnce(t *testing.T) {
	chat := sequenceModel(toolCallMessage(namedToolCall("call", "docker_control", `{"container_id":"web","action":"start"}`)))
	runner, _ := testRunner(t, chat, tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}, 0)
	stream := &confirmTransitionStream{SliceStream: &SliceStream{}, emitted: make(chan struct{}), release: make(chan struct{})}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "go", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	<-stream.emitted
	if err := runner.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	close(stream.release)
	events := waitClosed(t, stream.SliceStream)
	done, failed := terminalCounts(events)
	if done+failed != 1 {
		t.Fatalf("terminal events = %+v", events)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
}

type terminalBlockingStream struct {
	*SliceStream
	terminal chan struct{}
	once     sync.Once
}

func (s *terminalBlockingStream) Send(ctx context.Context, event Event) error {
	if event.Type != "done" && event.Type != "error" {
		return s.SliceStream.Send(ctx, event)
	}
	s.once.Do(func() { close(s.terminal) })
	<-ctx.Done()
	return ctx.Err()
}

func TestR1CloseContextInterruptsBlockedTerminalDelivery(t *testing.T) {
	runner, _ := testRunner(t, sequenceModel(schema.AssistantMessage("ok", nil)), tools.Dependencies{}, 0)
	stream := &terminalBlockingStream{SliceStream: &SliceStream{}, terminal: make(chan struct{})}
	if _, err := runner.Start(context.Background(), ChatArgs{Message: "go"}, StaticStream(stream)); err != nil {
		t.Fatal(err)
	}
	<-stream.terminal
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	_, closed := stream.Snapshot()
	if !closed {
		t.Fatal("stream left open after forced shutdown")
	}
}
