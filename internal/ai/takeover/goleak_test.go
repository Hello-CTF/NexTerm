package takeover

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/goleak"
)

// TestMain verifies the package does not leak goroutines after its tests
// finish. This package never re-executes the test binary, so every run goes
// through m.Run with no helper-process branch to exempt.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// The scenario tests below are the minimal leak reproductions for the
// goroutine-owning run paths. Run them in isolation for precise stacks:
//
//	go test -run TestGoleak ./internal/ai/takeover/
//
// Each closes its manager before goleak.VerifyNone so the check attributes
// only the goroutines the scenario itself left behind.

// 正常完成: a run that finishes through the done tool must not leave
// goroutines behind.
func TestGoleakNormalCompletion(t *testing.T) {
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

// 取消: a user pause cancels the in-flight model iteration; the escalation
// watchdog and cancel-outcome goroutines it spawns must not outlive the run.
func TestGoleakCancellation(t *testing.T) {
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
	waitClosed(t, stream)
	if err := h.manager.Close(); err != nil {
		t.Fatal(err)
	}
	goleak.VerifyNone(t)
}

// 断连/超时: a model stream that drops mid-run (after one successful tool
// round trip) must not leave goroutines behind.
func TestGoleakMidRunDisconnect(t *testing.T) {
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

// 并发: concurrent runs on distinct tabs must not leak goroutines or
// misattribute each other's terminal events. The model builds a fresh message
// per call: eino assigns message IDs by mutating Message.Extra, so sharing
// one *schema.Message across concurrent runs races under -race.
func TestGoleakConcurrentRuns(t *testing.T) {
	chat := &fakeModel{stream: func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(doneCall("finished"))}), nil
	}}
	h := newHarness(t, chat)
	const runners = 4
	var wg sync.WaitGroup
	streams := make([]*agent.SliceStream, runners)
	for index := 0; index < runners; index++ {
		streams[index] = &agent.SliceStream{}
		wg.Add(1)
		go func(tabID string, stream *agent.SliceStream) {
			defer wg.Done()
			if _, err := h.manager.Run(context.Background(), RunArgs{TabID: tabID, Instruction: "go"}, agent.StaticStream(stream)); err != nil {
				t.Error(err)
			}
		}(fmt.Sprintf("tab-%d", index), streams[index])
	}
	wg.Wait()
	for index, stream := range streams {
		events, closed := waitStreamClosed(stream)
		if !closed {
			t.Fatalf("run %d stream never closed", index)
		}
		if done, failed := counts(events); done != 1 || failed != 0 {
			t.Fatalf("run %d events = %+v", index, events)
		}
	}
	if err := h.manager.Close(); err != nil {
		t.Fatal(err)
	}
	goleak.VerifyNone(t)
}

func waitStreamClosed(stream *agent.SliceStream) ([]agent.Event, bool) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, closed := stream.Snapshot()
		if closed {
			return events, true
		}
		time.Sleep(time.Millisecond)
	}
	return nil, false
}
