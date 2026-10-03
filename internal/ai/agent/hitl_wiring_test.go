package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/hitl"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func confirmRunner(t *testing.T, chat model.BaseChatModel, deps tools.Dependencies) (*Runner, *SliceStream, StartResponse) {
	t.Helper()
	runner, _ := testRunner(t, chat, deps, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	return runner, stream, response
}

func dockerConfirmChat() model.BaseChatModel {
	return sequenceModel(
		toolCallMessage(namedToolCall("call", "docker_control", `{"container_id":"web","action":"start"}`)),
		schema.AssistantMessage("done", nil),
	)
}

func TestHITLWiringSnapshotEventsReplay(t *testing.T) {
	var actions atomic.Int64
	runner, stream, response := confirmRunner(t, dockerConfirmChat(), tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { actions.Add(1); return nil }})
	confirmation := waitEvent(t, stream, "confirmRequired")
	if confirmation.RequestID == "" || confirmation.Attempt != 1 {
		t.Fatalf("confirmRequired missing stable binding: %+v", confirmation)
	}

	first, err := runner.HITLSnapshot(response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.HITLSnapshot(response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != hitl.RunStatusInterrupted || len(first.Pending) != 1 {
		t.Fatalf("snapshot = %+v", first)
	}
	pending := first.Pending[0]
	if pending.ID != confirmation.RequestID || pending.CallID != confirmation.ID || pending.Nonce != confirmation.Nonce || pending.Attempt != 1 {
		t.Fatalf("pending request does not match the emitted event: %+v vs %+v", pending, confirmation)
	}
	if second.Pending[0].ID != pending.ID {
		t.Fatalf("request ID is not stable across snapshots: %q vs %q", second.Pending[0].ID, pending.ID)
	}

	events, err := runner.HITLEvents(response.JobID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != hitl.EventInterrupted || events[0].Sequence != 1 || events[0].RequestID != pending.ID {
		t.Fatalf("interrupt events = %+v", events)
	}

	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	resumed, err := runner.HITLEvents(response.JobID, events[0].Sequence)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) < 1 || resumed[0].Kind != hitl.EventResumed || resumed[0].Sequence != 2 || resumed[0].Attempt != 2 || resumed[0].RequestID != pending.ID {
		t.Fatalf("resumed events = %+v", resumed)
	}

	_ = waitClosed(t, stream)
	if actions.Load() != 1 {
		t.Fatalf("actions = %d", actions.Load())
	}
	all, err := runner.HITLEvents(response.JobID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[2].Kind != hitl.EventTerminal || all[2].Reason != hitl.TerminalCompleted || all[2].Sequence != 3 {
		t.Fatalf("terminal events = %+v", all)
	}
	for index := 1; index < len(all); index++ {
		if all[index].Sequence != all[index-1].Sequence+1 {
			t.Fatalf("event sequence is not per-run monotonic: %+v", all)
		}
	}
	if tail, err := runner.HITLEvents(response.JobID, all[2].Sequence); err != nil || len(tail) != 0 {
		t.Fatalf("events after terminal = %+v err=%v", tail, err)
	}
	final, err := runner.HITLSnapshot(response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != hitl.RunStatusCompleted || final.Terminal == nil || final.Terminal.Reason != hitl.TerminalCompleted || len(final.Pending) != 0 {
		t.Fatalf("final snapshot = %+v", final)
	}
}

func TestHITLWiringExpiredRequestTerminatesRun(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	checkpoints := NewMemoryCheckpoints()
	manager, err := hitl.NewManager(hitl.Config{Checkpoints: checkpoints, TTL: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model:       func(context.Context) (model.BaseChatModel, uint64, error) { return dockerConfirmChat(), 32768, nil },
		Tools:       tools.NewRegistry(tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}),
		Store:       storage,
		Checkpoints: checkpoints,
		HITL:        manager,
	})
	t.Cleanup(func() { _ = runner.Close() })
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")
	time.Sleep(200 * time.Millisecond)
	err = runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"})
	if err == nil || (!errors.Is(err, ErrConfirmationStale) && !errors.Is(err, ErrJobNotFound)) {
		t.Fatalf("expired confirmation error = %v", err)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("terminal counts done=%d error=%d events=%+v", done, failed, events)
	}
	for _, event := range events {
		if event.Type == "error" && !strings.Contains(event.Message, "过期") {
			t.Fatalf("terminal error does not report expiry: %+v", event)
		}
	}
	snapshot, err := runner.HITLSnapshot(response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != hitl.RunStatusExpired || snapshot.Terminal == nil || snapshot.Terminal.Reason != hitl.TerminalExpired {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestHITLWiringConcurrentConfirmSingleConsumption(t *testing.T) {
	var actions atomic.Int64
	runner, stream, response := confirmRunner(t, dockerConfirmChat(), tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { actions.Add(1); return nil }})
	confirmation := waitEvent(t, stream, "confirmRequired")
	const racers = 8
	var wg sync.WaitGroup
	successes := make(chan error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			successes <- runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"})
		}()
	}
	wg.Wait()
	close(successes)
	accepted := 0
	for err := range successes {
		if err == nil {
			accepted++
			continue
		}
		if !errors.Is(err, ErrConfirmationStale) && !errors.Is(err, ErrJobNotFound) {
			t.Fatalf("unexpected confirm error: %v", err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted confirmations = %d", accepted)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("terminal counts done=%d error=%d events=%+v", done, failed, events)
	}
	if actions.Load() != 1 {
		t.Fatalf("actions = %d", actions.Load())
	}
}

func TestHITLWiringConfirmCancelRaceSingleTerminal(t *testing.T) {
	var actions atomic.Int64
	runner, stream, response := confirmRunner(t, dockerConfirmChat(), tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { actions.Add(1); return nil }})
	confirmation := waitEvent(t, stream, "confirmRequired")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"})
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = runner.Cancel(response.JobID)
	}()
	wg.Wait()
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done+failed != 1 {
		t.Fatalf("terminal counts done=%d error=%d events=%+v", done, failed, events)
	}
	if actions.Load() > 1 {
		t.Fatalf("actions = %d", actions.Load())
	}
	snapshot, err := runner.HITLSnapshot(response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Terminal == nil {
		t.Fatalf("missing terminal event: %+v", snapshot)
	}
	if snapshot.Terminal.Reason != hitl.TerminalCompleted && snapshot.Terminal.Reason != hitl.TerminalCanceled {
		t.Fatalf("terminal reason = %q", snapshot.Terminal.Reason)
	}
}

func TestHITLWiringQuestionReplay(t *testing.T) {
	runner, _ := testRunner(t, sequenceModel(
		toolCallMessage(namedToolCall("ask", "ask_user", `{"question":"继续？","options":["继续","停止"]}`)),
		schema.AssistantMessage("done", nil),
	), tools.Dependencies{}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	question := waitEvent(t, stream, "questionRequired")
	if question.RequestID == "" {
		t.Fatalf("questionRequired missing request ID: %+v", question)
	}
	snapshot, err := runner.HITLSnapshot(response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Pending) != 1 || snapshot.Pending[0].Kind != hitl.KindQuestion || snapshot.Pending[0].Question == nil || snapshot.Pending[0].Question.ID != question.RequestID {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if err := runner.Answer(Answer{JobID: response.JobID, CallID: question.ID, Nonce: question.Nonce, Text: "继续"}); err != nil {
		t.Fatal(err)
	}
	_ = waitClosed(t, stream)
	events, err := runner.HITLEvents(response.JobID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Kind != hitl.EventInterrupted || events[1].Kind != hitl.EventResumed || events[2].Kind != hitl.EventTerminal {
		t.Fatalf("events = %+v", events)
	}
}

func TestHITLWiringUnknownRunSurfacesNotFound(t *testing.T) {
	runner, _ := testRunner(t, sequenceModel(schema.AssistantMessage("ok", nil)), tools.Dependencies{}, 0)
	if _, err := runner.HITLSnapshot("missing"); !errors.Is(err, hitl.ErrRunNotFound) {
		t.Fatalf("snapshot error = %v", err)
	}
	if _, err := runner.HITLEvents("missing", 0); !errors.Is(err, hitl.ErrRunNotFound) {
		t.Fatalf("events error = %v", err)
	}
}

func TestHITLWiringReplayCommandsDispatch(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) {
			return dockerConfirmChat(), 32768, nil
		},
		Tools: tools.NewRegistry(tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}),
		Store: storage,
	})
	defer runner.Close()
	dispatcher := ipc.NewDispatcher()
	if err := runner.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")

	snapshotResponse := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "ai_hitl_snapshot", Args: json.RawMessage(`{"jobId":"` + response.JobID + `"}`)}, ipc.Environment{})
	if !snapshotResponse.OK {
		t.Fatalf("snapshot dispatch failed: %+v", snapshotResponse.Error)
	}
	var snapshot hitl.Snapshot
	if err := json.Unmarshal(snapshotResponse.Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Pending) != 1 || snapshot.Pending[0].ID != confirmation.RequestID {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	eventsResponse := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "ai_hitl_events", Args: json.RawMessage(`{"jobId":"` + response.JobID + `","afterSeq":0}`)}, ipc.Environment{})
	if !eventsResponse.OK {
		t.Fatalf("events dispatch failed: %+v", eventsResponse.Error)
	}
	var events []hitl.Event
	if err := json.Unmarshal(eventsResponse.Data, &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != hitl.EventInterrupted {
		t.Fatalf("events = %+v", events)
	}

	missing := dispatcher.Dispatch(context.Background(), ipc.Request{Command: "ai_hitl_snapshot", Args: json.RawMessage(`{"jobId":"missing"}`)}, ipc.Environment{})
	if missing.OK {
		t.Fatal("snapshot for unknown run succeeded")
	}
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "deny"}); err != nil {
		t.Fatal(err)
	}
	_ = waitClosed(t, stream)
}

// confirmEmitStream synchronously confirms a confirmRequired event from
// inside Send, so the acceptance lands after hitl.Manager.Interrupt but
// before the consume loop reaches parkOrHandoff: the tightest
// confirm-before-parked boundary, without relying on scheduling luck.
type confirmEmitStream struct {
	*SliceStream
	confirm func(Event) error
}

func (s *confirmEmitStream) Send(ctx context.Context, event Event) error {
	if event.Type == "confirmRequired" && s.confirm != nil {
		if err := s.confirm(event); err != nil {
			return err
		}
	}
	return s.SliceStream.Send(ctx, event)
}

// TestHITLWiringConfirmBeforeParkedResumesExactlyOnce pins the handoff
// boundary found in review: a resume accepted while the interrupted loop is
// still winding down must be drained by exactly one consume loop and reach a
// single terminal state — never stranded with a consumed request and no
// loop driving the resumed stream.
func TestHITLWiringConfirmBeforeParkedResumesExactlyOnce(t *testing.T) {
	const jobID = "hitl-confirm-before-parked"
	var actions atomic.Int64
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return dockerConfirmChat(), 32768, nil },
		Tools: tools.NewRegistry(tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { actions.Add(1); return nil }}),
		Store: storage,
		NewID: func() string { return jobID },
	})
	defer runner.Close()
	stream := &confirmEmitStream{SliceStream: &SliceStream{}, confirm: func(event Event) error {
		return runner.Confirm(Confirmation{JobID: jobID, CallID: event.ID, Nonce: event.Nonce, Decision: "allow"})
	}}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "go", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	if response.JobID != jobID {
		t.Fatalf("job ID = %q", response.JobID)
	}
	events := waitClosed(t, stream.SliceStream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("terminal counts done=%d error=%d events=%+v", done, failed, events)
	}
	if actions.Load() != 1 {
		t.Fatalf("actions = %d", actions.Load())
	}
	replay, err := runner.HITLEvents(jobID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 3 || replay[0].Kind != hitl.EventInterrupted || replay[1].Kind != hitl.EventResumed || replay[2].Kind != hitl.EventTerminal || replay[2].Reason != hitl.TerminalCompleted {
		t.Fatalf("replay = %+v", replay)
	}
	if err := runner.Confirm(Confirmation{JobID: jobID, CallID: "call", Nonce: "stale", Decision: "allow"}); err == nil {
		t.Fatal("post-terminal confirmation accepted")
	}
}

// TestHITLWiringConfirmInsideParkTransitionResumesExactlyOnce forces the
// interleaving from the r1 review: the consume loop is paused inside the
// park transition (scheduling point, pendingMu held) while a Confirm is in
// flight. The atomic transition must exclude the acceptance until running=false
// is set, so the resumed stream is still drained by exactly one consume loop.
// On the pre-fix two-section shape the Confirm completes inside the window
// and the resume is stranded; the blocked-acceptance assertion catches it.
func TestHITLWiringConfirmInsideParkTransitionResumesExactlyOnce(t *testing.T) {
	var actions atomic.Int64
	parkEntered := make(chan struct{})
	releasePark := make(chan struct{})
	var parkOnce sync.Once
	parkOrHandoffTestHook = func() {
		parkOnce.Do(func() { close(parkEntered) })
		<-releasePark
	}
	released := false
	defer func() {
		if !released {
			close(releasePark)
		}
		parkOrHandoffTestHook = nil
	}()
	runner, stream, response := confirmRunner(t, dockerConfirmChat(), tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { actions.Add(1); return nil }})
	confirmation := waitEvent(t, stream, "confirmRequired")
	<-parkEntered

	confirmReturned := make(chan error, 1)
	go func() {
		confirmReturned <- runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"})
	}()
	select {
	case err := <-confirmReturned:
		t.Fatalf("confirm completed inside the park transition; the handoff is not atomic: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releasePark)
	released = true
	select {
	case err := <-confirmReturned:
		if err != nil {
			t.Fatalf("confirm after the park transition: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("confirm did not return after the park transition")
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("terminal counts done=%d error=%d events=%+v", done, failed, events)
	}
	if actions.Load() != 1 {
		t.Fatalf("actions = %d", actions.Load())
	}
	replay, err := runner.HITLEvents(response.JobID, 0)
	if err != nil || len(replay) != 3 || replay[0].Kind != hitl.EventInterrupted || replay[1].Kind != hitl.EventResumed || replay[2].Kind != hitl.EventTerminal || replay[2].Reason != hitl.TerminalCompleted {
		t.Fatalf("replay = %+v err=%v", replay, err)
	}
}

// TestHITLWiringCloseContextCancelsResumedExecution pins the shutdown path:
// a job confirmed and resumed, then blocked inside the model, must still be
// canceled through the manager — the resumed execution runs on the manager's
// run context, so CloseContext must return bounded with a single terminal
// event instead of waiting for the provider forever.
func TestHITLWiringCloseContextCancelsResumedExecution(t *testing.T) {
	blocked := make(chan struct{})
	var blockOnce sync.Once
	var calls atomic.Int64
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("call", "docker_control", `{"container_id":"web","action":"start"}`))}), nil
		}
		blockOnce.Do(func() { close(blocked) })
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	runner, _ := testRunner(t, chat, tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	<-blocked
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runner.CloseContext(ctx); err != nil {
		t.Fatalf("CloseContext must return once the resumed execution is canceled: %v", err)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("terminal counts done=%d error=%d events=%+v", done, failed, events)
	}
	replay, err := runner.HITLEvents(response.JobID, 0)
	if err != nil || len(replay) != 3 || replay[0].Kind != hitl.EventInterrupted || replay[1].Kind != hitl.EventResumed || replay[2].Kind != hitl.EventTerminal || replay[2].Reason != hitl.TerminalCanceled {
		t.Fatalf("replay = %+v err=%v", replay, err)
	}
}
