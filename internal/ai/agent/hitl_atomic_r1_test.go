package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/schema"
)

func TestR1ImmediateHITLResponseDoesNotGoStale(t *testing.T) {
	var actions atomic.Int64
	chat := sequenceModel(
		toolCallMessage(namedToolCall("call", "docker_control", `{"container_id":"web","action":"start"}`)),
		schema.AssistantMessage("done", nil),
	)
	runner, _ := testRunner(t, chat, tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { actions.Add(1); return nil }}, 0)
	stream := &confirmTransitionStream{SliceStream: &SliceStream{}, emitted: make(chan struct{}), release: make(chan struct{})}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "go", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	<-stream.emitted
	confirmation := waitEvent(t, stream.SliceStream, "confirmRequired")
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	close(stream.release)
	events := waitClosed(t, stream.SliceStream)
	if actions.Load() != 1 {
		t.Fatalf("actions = %d", actions.Load())
	}
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("events = %+v", events)
	}
}

func TestR1ResumeAfterCancelIsRejected(t *testing.T) {
	chat := sequenceModel(toolCallMessage(namedToolCall("call", "docker_control", `{"container_id":"web","action":"start"}`)))
	runner, _ := testRunner(t, chat, tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}, 0)
	stream := &confirmTransitionStream{SliceStream: &SliceStream{}, emitted: make(chan struct{}), release: make(chan struct{})}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "go", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	<-stream.emitted
	confirmation := waitEvent(t, stream.SliceStream, "confirmRequired")
	if err := runner.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("resume after cancel = %v", err)
	}
	close(stream.release)
	events := waitClosed(t, stream.SliceStream)
	if done, failed := terminalCounts(events); done+failed != 1 {
		t.Fatalf("events = %+v", events)
	}
}
