package takeover

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/goleak"
)

// Deterministic goroutine-lifecycle regressions for the pause/cancel/exit
// paths. Each scenario drives the run to a terminal state, closes the
// manager, and then requires goleak to find nothing left behind: watchdogs,
// cancel-outcome senders, checkpoint waiters, and the Eino graph must all
// exit when the run ends.

func TestLifecycleNormalCompletionLeavesNoGoroutines(t *testing.T) {
	h := newHarness(t, sequence(toolCallMessage(doneCall("finished"))))
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	events := waitClosed(t, stream)
	if done, failed := counts(events); done != 1 || failed != 0 {
		t.Fatalf("events = %+v", events)
	}
	if err := h.manager.Close(); err != nil {
		t.Fatal(err)
	}
	goleak.VerifyNone(t)
}

func TestLifecycleEscalatedPauseLeavesNoGoroutines(t *testing.T) {
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.PauseEscalationTimeout = 50 * time.Millisecond
	})
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	h.manager.Pause("tab")
	events := waitClosed(t, stream)
	done, failed := counts(events)
	if done+failed != 1 {
		t.Fatalf("events = %+v", events)
	}
	if err := h.manager.Close(); err != nil {
		t.Fatal(err)
	}
	goleak.VerifyNone(t)
}

func TestLifecycleCanceledPauseLeavesNoGoroutines(t *testing.T) {
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.PauseEscalationTimeout = time.Hour
	})
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	waitEvent(t, stream, "screen")
	h.manager.Pause("tab")
	h.manager.Pause("tab")
	if err := h.manager.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := counts(events); done != 1 || failed != 0 {
		t.Fatalf("events = %+v", events)
	}
	if err := h.manager.Close(); err != nil {
		t.Fatal(err)
	}
	goleak.VerifyNone(t)
}

func TestLifecycleMidRunDisconnectLeavesNoGoroutines(t *testing.T) {
	var calls atomic.Int32
	chat := &fakeModel{stream: func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`))}), nil
		}
		return nil, errors.New("model stream dropped")
	}}
	h := newHarness(t, chat)
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	events := waitClosed(t, stream)
	if done, failed := counts(events); done != 0 || failed != 1 {
		t.Fatalf("events = %+v", events)
	}
	if err := h.manager.Close(); err != nil {
		t.Fatal(err)
	}
	goleak.VerifyNone(t)
}

func TestAwaitCheckpointReturnsWhenRunContextEnds(t *testing.T) {
	h := newHarness(t, sequence(toolCallMessage(doneCall("finished"))))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	state := &runState{id: "job", ctx: ctx, cancel: func() {}}
	start := time.Now()
	if h.manager.awaitCheckpoint(state) {
		t.Fatal("awaitCheckpoint succeeded without a checkpoint")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("awaitCheckpoint parked %v after the run context ended", elapsed)
	}
}
