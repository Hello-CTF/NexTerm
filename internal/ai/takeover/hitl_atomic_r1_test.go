package takeover

import (
	"context"
	"errors"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
)

func TestR1TakeoverImmediateHITLResponseDoesNotGoStale(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"sudo reboot<enter>"}`)),
		toolCallMessage(doneCall("finished")),
	)
	h := newHarness(t, chat)
	stream := &confirmTransitionStream{SliceStream: &agent.SliceStream{}, emitted: make(chan struct{}), release: make(chan struct{})}
	response, err := h.manager.Run(context.Background(), RunArgs{TabID: "tab", Instruction: "go"}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	<-stream.emitted
	confirmation := waitEvent(t, stream.SliceStream, "confirmRequired")
	if err := h.manager.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	close(stream.release)
	events := waitClosed(t, stream.SliceStream)
	if done, failed := counts(events); done != 1 || failed != 0 {
		t.Fatalf("events = %+v", events)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 {
		t.Fatalf("AI writes = %q", h.aiWrites)
	}
}

func TestR1TakeoverResumeAfterCancelIsRejected(t *testing.T) {
	chat := sequence(toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"sudo reboot<enter>"}`)))
	h := newHarness(t, chat)
	stream := &confirmTransitionStream{SliceStream: &agent.SliceStream{}, emitted: make(chan struct{}), release: make(chan struct{})}
	response, err := h.manager.Run(context.Background(), RunArgs{TabID: "tab", Instruction: "go"}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	<-stream.emitted
	confirmation := waitEvent(t, stream.SliceStream, "confirmRequired")
	if err := h.manager.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	if err := h.manager.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resume after cancel = %v", err)
	}
	close(stream.release)
	events := waitClosed(t, stream.SliceStream)
	if done, failed := counts(events); done+failed != 1 {
		t.Fatalf("events = %+v", events)
	}
}
