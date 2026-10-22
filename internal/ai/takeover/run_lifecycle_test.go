package takeover

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type confirmTransitionStream struct {
	*agent.SliceStream
	emitted chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *confirmTransitionStream) Send(ctx context.Context, event agent.Event) error {
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

func TestR1TakeoverCancelDuringHITLTransitionCompletesExactlyOnce(t *testing.T) {
	chat := sequence(toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"sudo reboot<enter>"}`)))
	h := newHarness(t, chat)
	stream := &confirmTransitionStream{SliceStream: &agent.SliceStream{}, emitted: make(chan struct{}), release: make(chan struct{})}
	response, err := h.manager.Run(context.Background(), RunArgs{TabID: "tab", Instruction: "go"}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	<-stream.emitted
	if err := h.manager.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	close(stream.release)
	events := waitClosed(t, stream.SliceStream)
	done, failed := counts(events)
	if done+failed != 1 {
		t.Fatalf("terminal events = %+v", events)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.manager.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestR1SecondEnterSettlesPausedOldOwnership(t *testing.T) {
	chat := sequence(toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"sudo reboot<enter>"}`)))
	h := newHarness(t, chat)
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	_ = waitEvent(t, stream, "confirmRequired")
	if _, err := h.manager.Enter(context.Background(), "tab"); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	done, failed := counts(events)
	if done+failed != 1 {
		t.Fatalf("old ownership terminal events = %+v", events)
	}
	h.manager.Preempt("tab")
}

func TestR1OwnershipTokenAllowsOnlyOneActiveRun(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	h := newHarness(t, chat)
	token, err := h.manager.Enter(context.Background(), "tab")
	if err != nil {
		t.Fatal(err)
	}
	first := &agent.SliceStream{}
	response, err := h.manager.Run(context.Background(), RunArgs{TabID: "tab", Token: token, Instruction: "go"}, agent.StaticStream(first))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	second := &agent.SliceStream{}
	factoryCalls := 0
	_, err = h.manager.Run(context.Background(), RunArgs{TabID: "tab", Token: token, Instruction: "concurrent"}, func(context.Context, string, string) (agent.Stream, error) {
		factoryCalls++
		return second, nil
	})
	if !errors.Is(err, ErrOwnershipActive) {
		t.Fatalf("concurrent run error = %v", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("rejected run opened a competing producer %d times", factoryCalls)
	}
	if err := h.manager.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	_ = waitClosed(t, first)
}

type terminalBlockingStream struct {
	*agent.SliceStream
	terminal chan struct{}
	once     sync.Once
}

func (s *terminalBlockingStream) Send(ctx context.Context, event agent.Event) error {
	if event.Type != "done" && event.Type != "error" {
		return s.SliceStream.Send(ctx, event)
	}
	s.once.Do(func() { close(s.terminal) })
	<-ctx.Done()
	return ctx.Err()
}

func TestR1TakeoverCloseContextInterruptsBlockedTerminalDelivery(t *testing.T) {
	h := newHarness(t, sequence(schema.AssistantMessage("ok", nil)))
	stream := &terminalBlockingStream{SliceStream: &agent.SliceStream{}, terminal: make(chan struct{})}
	if _, err := h.manager.Run(context.Background(), RunArgs{TabID: "tab", Instruction: "go"}, agent.StaticStream(stream)); err != nil {
		t.Fatal(err)
	}
	<-stream.terminal
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.manager.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, closed := stream.Snapshot(); !closed {
		t.Fatal("stream left open after forced shutdown")
	}
}
