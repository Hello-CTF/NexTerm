package takeover

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/hub"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestR1TakeoverUserCancelUnblocksFullHubQueue(t *testing.T) {
	backpressured := make(chan struct{})
	var once sync.Once
	bus := hub.New(hub.Options{QueueFrames: 1, OnBackpressure: func(string, int) {
		once.Do(func() { close(backpressured) })
	}})
	t.Cleanup(func() { _ = bus.Close() })
	chat := &fakeModel{stream: func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		return schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("first", nil),
			schema.AssistantMessage("second", nil),
		}), nil
	}}
	h := newHarness(t, chat)
	response, err := h.manager.Run(context.Background(), RunArgs{TabID: "tab", ChannelID: "channel", Instruction: "go"}, agent.IPCStreamFactory(hub.StreamFactory{Hub: bus}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-backpressured:
	case <-time.After(time.Second):
		t.Fatal("producer did not reach full queue")
	}
	canceled := make(chan error, 1)
	go func() { canceled <- h.manager.Cancel(response.JobID) }()
	select {
	case err := <-canceled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("user cancel blocked on full queue")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.manager.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	if stats := bus.Stats(); stats.QueuedFrames != 0 || stats.LiveChannels != 0 || stats.PendingChannels != 0 {
		t.Fatalf("canceled channel remains: %+v", stats)
	}
}

func TestR1TakeoverCancelDuringFinalizationDoesNotBlock(t *testing.T) {
	h := newHarness(t, sequence(schema.AssistantMessage("ok", nil)))
	stream := &terminalBlockingStream{SliceStream: &agent.SliceStream{}, terminal: make(chan struct{})}
	response, err := h.manager.Run(context.Background(), RunArgs{TabID: "tab", Instruction: "go"}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	<-stream.terminal
	canceled := make(chan error, 1)
	go func() { canceled <- h.manager.Cancel(response.JobID) }()
	select {
	case err := <-canceled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel during finalization blocked on terminal delivery")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.manager.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, closed := stream.Snapshot(); !closed {
		t.Fatal("stream left open after forced shutdown")
	}
}

func TestR1TakeoverLateConfirmDuringFinalizationResumes(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"sudo reboot<enter>"}`)),
		toolCallMessage(doneCall("finished")),
	)
	h := newHarness(t, chat)
	stream := &agent.SliceStream{}
	confirmed := make(chan error, 1)
	ready := make(chan struct{})
	var once sync.Once
	var confirmation agent.Event
	var response RunResponse
	var err error
	previous := h.manager.hooks.afterResumeCheck
	h.manager.hooks.afterResumeCheck = func() {
		once.Do(func() {
			<-ready
			confirmed <- h.manager.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"})
		})
	}
	t.Cleanup(func() { h.manager.hooks.afterResumeCheck = previous })
	response, err = h.manager.Run(context.Background(), RunArgs{TabID: "tab", Instruction: "go"}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation = waitEvent(t, stream, "confirmRequired")
	close(ready)
	if err := <-confirmed; err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := counts(events); done != 1 || failed != 0 {
		t.Fatalf("events = %+v", events)
	}
	if events[len(events)-1].Answer != "finished" {
		t.Fatalf("final answer = %q, want finished", events[len(events)-1].Answer)
	}
}

func TestR1TakeoverLateConfirmDoesNotStartAfterWatchdogCompletes(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"sudo reboot<enter>"}`)),
		toolCallMessage(doneCall("finished")),
	)
	h := newHarness(t, chat)
	stream := &agent.SliceStream{}
	confirmed := make(chan error, 1)
	ready := make(chan struct{})
	afterChecks := make(chan struct{}, 2)
	var once sync.Once
	var confirmation agent.Event
	var response RunResponse
	var err error
	previousAfter := h.manager.hooks.afterResumeCheck
	h.manager.hooks.afterResumeCheck = func() {
		afterChecks <- struct{}{}
		once.Do(func() {
			<-ready
			confirmed <- h.manager.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"})
		})
	}
	previousBefore := h.manager.hooks.beforeResumeStart
	h.manager.hooks.beforeResumeStart = func() {
		h.manager.completeLocked(h.manager.jobs[response.JobID], runResult{answer: "watchdog", reason: "watchdog"})
	}
	t.Cleanup(func() {
		h.manager.hooks.afterResumeCheck = previousAfter
		h.manager.hooks.beforeResumeStart = previousBefore
	})
	response, err = h.manager.Run(context.Background(), RunArgs{TabID: "tab", Instruction: "go"}, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	state := h.manager.jobs[response.JobID]
	confirmation = waitEvent(t, stream, "confirmRequired")
	close(ready)
	if err := <-confirmed; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		state.pendingMu.Lock()
		running := state.running
		state.pendingMu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("finalization did not settle")
		}
		time.Sleep(time.Millisecond)
	}
	if len(afterChecks) != 1 {
		t.Fatalf("resume started after watchdog completion: afterChecks=%d", len(afterChecks))
	}
	events := waitClosed(t, stream)
	if done, failed := counts(events); done != 1 || failed != 0 {
		t.Fatalf("events = %+v", events)
	}
	if events[len(events)-1].Answer != "watchdog" {
		t.Fatalf("final answer = %q, want watchdog", events[len(events)-1].Answer)
	}
}
