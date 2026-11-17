package takeover

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// gatedTwoStepModel types one command, waits for release before typing a
// second, then calls done. Each call's prompt is handed over on prompts.
type gatedTwoStepModel struct {
	calls   atomic.Int32
	release chan struct{}
	prompts chan []*schema.Message
}

func newGatedTwoStepModel() *gatedTwoStepModel {
	return &gatedTwoStepModel{release: make(chan struct{}), prompts: make(chan []*schema.Message, 8)}
}

func (g *gatedTwoStepModel) chat() *fakeModel {
	return &fakeModel{stream: func(ctx context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		select {
		case g.prompts <- append([]*schema.Message(nil), messages...):
		default:
		}
		if g.calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`))}), nil
		}
		if g.calls.Load() > 2 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(doneCall("finished"))}), nil
		}
		select {
		case <-g.release:
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("pwd", "send_keys", `{"keys":"pwd","enter":true}`))}), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
}

func requireContiguous(t *testing.T, events []agent.Event, from uint64) uint64 {
	t.Helper()
	next := from
	for _, event := range events {
		if event.Seq != next+1 {
			t.Fatalf("event %s has seq %d, want %d: %+v", event.Type, event.Seq, next+1, events)
		}
		next = event.Seq
	}
	return next
}

func countType(events []agent.Event, kind string) int {
	n := 0
	for _, event := range events {
		if event.Type == kind {
			n++
		}
	}
	return n
}

// A resume clicked right after pause, before the model reached its safe
// point, waits for the pause and continues the same job; the event sequence
// stays contiguous across both channels and the model learns of the resume
// from its screen message.
func TestPauseThenImmediateResumeContinuesSameTakeover(t *testing.T) {
	gate := newGatedTwoStepModel()
	h := newHarness(t, gate.chat())
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	waitEvent(t, stream, "toolResult")
	pause, err := h.manager.PauseFor(context.Background(), "tab", response.Token)
	if err != nil {
		t.Fatal(err)
	}
	if pause.Phase != PhasePausing {
		t.Fatalf("pause phase = %q, want %q", pause.Phase, PhasePausing)
	}
	type resumeResult struct {
		response RunResponse
		err      error
	}
	resumed := &agent.SliceStream{}
	result := make(chan resumeResult, 1)
	go func() {
		r, err := h.manager.Resume(context.Background(), ResumeArgs{TabID: "tab", Token: response.Token, JobID: response.JobID}, agent.StaticStream(resumed))
		result <- resumeResult{r, err}
	}()
	close(gate.release)
	got := <-result
	if got.err != nil {
		t.Fatalf("resume during pausing: %v", got.err)
	}
	if got.response.JobID != response.JobID {
		t.Fatalf("resume job = %s, want %s", got.response.JobID, response.JobID)
	}
	final := waitClosed(t, resumed)
	first, _ := stream.Snapshot()
	last := requireContiguous(t, first, 0)
	requireContiguous(t, final, last)
	if countType(first, "paused") != 1 || final[0].Type != "resumed" {
		t.Fatalf("pause/resume events: first=%+v final=%+v", first, final)
	}
	if done, failed := counts(final); done != 1 || failed != 0 || final[len(final)-1].Status != EndCompleted {
		t.Fatalf("resumed run end = %+v", final)
	}
	sawNote := false
	for len(gate.prompts) > 0 {
		for _, message := range <-gate.prompts {
			if strings.Contains(message.Content, resumeNote) {
				sawNote = true
			}
		}
	}
	if !sawNote {
		t.Fatal("the model was not told about the resume")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 2 {
		t.Fatalf("AI writes = %q, want each command once", h.aiWrites)
	}
}

// Every keystroke the user types pauses through the input hook; the run
// pauses once and reports it once.
func TestRepeatedUserInputPausesOnce(t *testing.T) {
	gate := newGatedTwoStepModel()
	h := newHarness(t, gate.chat())
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	waitEvent(t, stream, "toolResult")
	for range 20 {
		h.manager.Pause("tab")
	}
	close(gate.release)
	waitTakeoverPauseReady(t, h.manager, response.JobID)
	h.manager.Pause("tab")
	status, err := h.manager.Status(context.Background(), "tab", response.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Active || status.Phase != PhasePaused || status.JobID != response.JobID {
		t.Fatalf("status = %+v", status)
	}
	events, closed := stream.Snapshot()
	if closed || countType(events, "paused") != 1 {
		t.Fatalf("pause events = %+v closed=%v", events, closed)
	}
}

// Reclaiming ends the takeover exactly once with the user's reason and a
// stopped status; later reclaim and cancel calls are no-ops reported as
// already ended, and nothing is written to the terminal.
func TestReclaimEndsOnceWithReason(t *testing.T) {
	for _, paused := range []bool{false, true} {
		name := "running"
		if paused {
			name = "paused"
		}
		t.Run(name, func(t *testing.T) {
			gate := newGatedTwoStepModel()
			h := newHarness(t, gate.chat())
			stream := &agent.SliceStream{}
			response := h.run(t, stream, RunArgs{})
			waitEvent(t, stream, "toolResult")
			if paused {
				h.manager.Pause("tab")
				close(gate.release)
				waitTakeoverPauseReady(t, h.manager, response.JobID)
			}
			if err := h.manager.Exit(context.Background(), "tab", response.Token, "用户收回控制权"); err != nil {
				t.Fatal(err)
			}
			events := waitClosed(t, stream)
			done, failed := counts(events)
			last := events[len(events)-1]
			if done != 1 || failed != 0 || last.Reason != "用户收回控制权" || last.Answer != "用户收回控制权" || last.Status != EndStopped {
				t.Fatalf("reclaim events = %+v", events)
			}
			if err := h.manager.Exit(context.Background(), "tab", response.Token, "用户收回控制权"); !errors.Is(err, ErrStaleOwnership) {
				t.Fatalf("second reclaim error = %v", err)
			}
			if err := h.manager.Cancel(response.JobID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("cancel after reclaim error = %v", err)
			}
			if status, err := h.manager.Status(context.Background(), "tab", response.Token); err != nil || status.Active {
				t.Fatalf("status after reclaim = %+v, %v", status, err)
			}
			again, _ := stream.Snapshot()
			if len(again) != len(events) {
				t.Fatalf("events after the end: %+v", again[len(events):])
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if len(h.aiWrites) != 1 {
				t.Fatalf("terminal writes = %q, want only the AI command", h.aiWrites)
			}
		})
	}
}

// A takeover waiting for the user's confirmation has nothing in flight:
// pausing reports that instead of claiming a pause a resume cannot undo.
func TestPauseWhileAwaitingConfirmationReportsConfirm(t *testing.T) {
	chat := sequence(toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"sudo reboot","enter":true}`)), toolCallMessage(doneCall("finished")))
	h := newHarness(t, chat)
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	confirmation := waitEvent(t, stream, "confirmRequired")
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := h.manager.Status(context.Background(), "tab", response.Token)
		if err != nil {
			t.Fatal(err)
		}
		if status.Phase == PhaseConfirm {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status never reached confirm: %+v", status)
		}
		time.Sleep(time.Millisecond)
	}
	pause, err := h.manager.PauseFor(context.Background(), "tab", response.Token)
	if err != nil || pause.Phase != PhaseConfirm {
		t.Fatalf("pause while confirming = %+v, %v", pause, err)
	}
	if err := h.manager.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := counts(events); done != 1 || failed != 0 || countType(events, "paused") != 0 {
		t.Fatalf("events = %+v", events)
	}
}

type failingOnceStream struct {
	mu     sync.Mutex
	fail   bool
	events []agent.Event
}

func (s *failingOnceStream) Send(_ context.Context, event agent.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		s.fail = false
		return context.Canceled
	}
	s.events = append(s.events, event)
	return nil
}

func (s *failingOnceStream) Close() error { return nil }

// An undelivered event must not spend a sequence number: the frontend holds
// every event after a gap, which froze the banner and the conversation.
func TestFailedSendDoesNotLeaveSequenceGap(t *testing.T) {
	stream := &failingOnceStream{}
	state := &runState{stream: stream, ctx: context.Background()}
	if err := state.emit(context.Background(), agent.Event{Type: "delta"}); err != nil {
		t.Fatal(err)
	}
	stream.mu.Lock()
	stream.fail = true
	stream.mu.Unlock()
	if err := state.emit(context.Background(), agent.Event{Type: "delta"}); err == nil {
		t.Fatal("failed send reported success")
	}
	if err := state.emit(context.Background(), agent.Event{Type: "paused"}); err != nil {
		t.Fatal(err)
	}
	state.finish(agent.Event{Type: "done"})
	stream.mu.Lock()
	defer stream.mu.Unlock()
	requireContiguous(t, stream.events, 0)
}
