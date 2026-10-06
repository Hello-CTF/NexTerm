package hitl

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
)

func TestResumeFailureHasClassifiedTerminalReason(t *testing.T) {
	tests := []struct {
		name   string
		cause  error
		reason TerminalReason
		status RunStatus
	}{
		{name: "failure", cause: errors.New("resume failed"), reason: TerminalFailed, status: RunStatusFailed},
		{name: "context cancellation", cause: context.Canceled, reason: TerminalCanceled, status: RunStatusCanceled},
		{name: "deadline", cause: context.DeadlineExceeded, reason: TerminalCanceled, status: RunStatusCanceled},
		{name: "Eino cancellation", cause: &adk.CancelError{}, reason: TerminalCanceled, status: RunStatusCanceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			request := h.interrupt(t, "target-one", "call-one", `{}`)
			h.resumer.err = test.cause
			if _, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("answer", request)); err == nil {
				t.Fatal("resume unexpectedly succeeded")
			}
			snapshot, err := h.manager.Snapshot("run")
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Status != test.status || snapshot.Terminal == nil || snapshot.Terminal.Reason != test.reason || snapshot.Terminal.Sequence != 3 {
				t.Fatalf("snapshot = %+v", snapshot)
			}
			terminal, err := h.manager.Finish("run", TerminalCompleted, nil)
			if !errors.Is(err, ErrRunFinished) || terminal.Reason != test.reason {
				t.Fatalf("late completion = %+v, %v", terminal, err)
			}
			if h.store.deleteCount("checkpoint") != 1 {
				t.Fatalf("checkpoint deletes = %d", h.store.deleteCount("checkpoint"))
			}
		})
	}
}

func TestMultipleResumeAttemptsPreserveRunSequence(t *testing.T) {
	h := newHarness(t)
	first := h.interrupt(t, "target-one", "call-one", `{"value":1}`)
	firstResume, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("answer-one", first))
	if err != nil {
		t.Fatal(err)
	}
	if firstResume.Sequence != 2 || firstResume.Attempt != 2 {
		t.Fatalf("first resume = %+v", firstResume)
	}
	h.store.put("checkpoint", []byte("checkpoint-two"))
	second := h.interrupt(t, "target-two", "call-two", `{"value":2}`)
	if second.Sequence != 3 || second.Attempt != 2 {
		t.Fatalf("second interrupt = %+v", second)
	}
	secondResume, _, err := h.manager.Resume(context.Background(), h.resumer, answerFor("answer-two", second))
	if err != nil {
		t.Fatal(err)
	}
	if secondResume.Sequence != 4 || secondResume.Attempt != 3 {
		t.Fatalf("second resume = %+v", secondResume)
	}
	terminal, err := h.manager.Finish("run", TerminalCompleted, nil)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Sequence != 5 || terminal.Attempt != 3 {
		t.Fatalf("terminal = %+v", terminal)
	}
	events, err := h.manager.Events("run", 0)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []EventKind{EventInterrupted, EventResumed, EventInterrupted, EventResumed, EventTerminal}
	if len(events) != len(wantKinds) {
		t.Fatalf("events = %+v", events)
	}
	for index, event := range events {
		if event.Sequence != uint64(index+1) || event.Kind != wantKinds[index] {
			t.Fatalf("event %d = %+v", index, event)
		}
	}
}

func TestRegistrationCallerDisconnectDoesNotCancelRun(t *testing.T) {
	store := newFakeCheckpoints()
	store.put("checkpoint", []byte("checkpoint-one"))
	manager, err := NewManager(Config{Checkpoints: store, TTL: time.Hour, NewNonce: func() (string, error) { return "nonce", nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	callerContext, disconnect := context.WithCancel(context.Background())
	if _, err := manager.RegisterRun(callerContext, "run", "checkpoint"); err != nil {
		t.Fatal(err)
	}
	disconnect()
	request, err := manager.Interrupt(context.Background(), &adk.InterruptCtx{ID: "target"}, InterruptInput{
		RunID: "run", CheckpointID: "checkpoint", CallID: "call", Tool: "exec_commands", Kind: KindConfirm,
	})
	if err != nil {
		t.Fatal(err)
	}
	resumer := &fakeResumer{}
	if _, _, err := manager.Resume(context.Background(), resumer, answerFor("answer", request)); err != nil {
		t.Fatal(err)
	}
	if resumer.call(0).ctxErr != nil {
		t.Fatalf("execution inherited caller cancellation: %v", resumer.call(0).ctxErr)
	}
	snapshot, err := manager.Snapshot("run")
	if err != nil || snapshot.Status != RunStatusRunning {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
}

func TestCheckpointCleanupFailureDoesNotDuplicateTerminal(t *testing.T) {
	h := newHarness(t)
	h.interrupt(t, "target-one", "call-one", `{}`)
	h.store.deleteErr = errors.New("delete failed")
	terminal, err := h.manager.Cancel("run")
	if err == nil || terminal.Reason != TerminalCanceled {
		t.Fatalf("cancel = %+v, %v", terminal, err)
	}
	again, secondErr := h.manager.Cancel("run")
	if secondErr == nil || again.Sequence != terminal.Sequence {
		t.Fatalf("duplicate cancel = %+v, %v", again, secondErr)
	}
	events, err := h.manager.Events("run", 0)
	if err != nil {
		t.Fatal(err)
	}
	terminals := 0
	for _, event := range events {
		if event.Kind == EventTerminal {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("terminal count = %d events = %+v", terminals, events)
	}
}
