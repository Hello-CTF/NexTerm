package takeover

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type takeoverJSONStream struct {
	stream *agent.SliceStream
}

func (s *takeoverJSONStream) SendJSON(ctx context.Context, data json.RawMessage) error {
	var event agent.Event
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	var wire struct {
		ConfirmationNonce string `json:"confirmationNonce"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	event.Nonce = wire.ConfirmationNonce
	return s.stream.Send(ctx, event)
}

func (s *takeoverJSONStream) Close() error {
	return s.stream.Close()
}

func takeoverStreamEnvironment(stream *agent.SliceStream) ipc.Environment {
	return ipc.Environment{Streams: ipc.StreamFactoryFuncs{JSON: func(context.Context, ipc.ChannelRef) (ipc.JSONStream, error) {
		return &takeoverJSONStream{stream: stream}, nil
	}}}
}

func dispatchTakeover(t *testing.T, dispatcher *ipc.Dispatcher, ctx context.Context, command string, args any, environment ipc.Environment) ipc.Response {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: data, Channel: ipc.ChannelRef{ID: "takeover-channel"}}, environment)
}

func takeoverResponseData[T any](t *testing.T, response ipc.Response) T {
	t.Helper()
	var result T
	if !response.OK || response.Error != nil {
		t.Fatalf("takeover IPC response = %+v", response)
	}
	if err := json.Unmarshal(response.Data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func enterTakeoverIPC(t *testing.T, dispatcher *ipc.Dispatcher, ctx context.Context, tabID string) string {
	t.Helper()
	response := dispatchTakeover(t, dispatcher, ctx, "ai_takeover_enter", map[string]any{"tabId": tabID}, ipc.Environment{})
	return takeoverResponseData[EnterResponse](t, response).Token
}

func runTakeoverIPC(t *testing.T, dispatcher *ipc.Dispatcher, ctx context.Context, args RunArgs, stream *agent.SliceStream) RunResponse {
	t.Helper()
	response := dispatchTakeover(t, dispatcher, ctx, "ai_takeover_run", args, takeoverStreamEnvironment(stream))
	return takeoverResponseData[RunResponse](t, response)
}

func waitTakeoverPauseReady(t *testing.T, manager *Manager, jobID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		state := manager.jobs[jobID]
		manager.mu.Unlock()
		if state != nil {
			state.pendingMu.Lock()
			ready := state.userPaused && !state.running && state.pauseReady
			state.pendingMu.Unlock()
			if ready {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("takeover did not become resumable")
}

func TestDispatcherTakeoverConfirmChecksOwner(t *testing.T) {
	chat := sequence(
		toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"sudo reboot<enter>"}`)),
		toolCallMessage(doneCall("finished")),
	)
	h := newHarness(t, chat)
	dispatcher := ipc.NewDispatcher()
	if err := h.manager.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	alice := ipc.WithUserID(context.Background(), "account-a")
	bob := ipc.WithUserID(context.Background(), "account-b")
	token := enterTakeoverIPC(t, dispatcher, alice, "tab")
	stream := &agent.SliceStream{}
	run := runTakeoverIPC(t, dispatcher, alice, RunArgs{TabID: "tab", Token: token, Instruction: "go"}, stream)
	confirmation := waitEvent(t, stream, "confirmRequired")
	args := agent.Confirmation{JobID: run.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}
	if response := dispatchTakeover(t, dispatcher, bob, "ai_takeover_confirm", args, ipc.Environment{}); response.OK {
		t.Fatal("cross-owner takeover confirmation succeeded")
	}
	if response := dispatchTakeover(t, dispatcher, alice, "ai_takeover_confirm", args, ipc.Environment{}); !response.OK {
		t.Fatalf("owner confirmation failed: %+v, args = %+v, event = %+v", response, args, confirmation)
	}
	events := waitClosed(t, stream)
	if done, failed := counts(events); done != 1 || failed != 0 {
		t.Fatalf("events = %+v", events)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 {
		t.Fatalf("writes = %q", h.aiWrites)
	}
}

func TestDispatcherTakeoverPauseResumeFactoryRetryAndSequence(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`))}), nil
		}
		select {
		case <-release:
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(doneCall("finished"))}), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	h := newHarness(t, chat)
	dispatcher := ipc.NewDispatcher()
	if err := h.manager.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	alice := ipc.WithUserID(context.Background(), "account-a")
	bob := ipc.WithUserID(context.Background(), "account-b")
	token := enterTakeoverIPC(t, dispatcher, alice, "tab")
	initial := &agent.SliceStream{}
	run := runTakeoverIPC(t, dispatcher, alice, RunArgs{TabID: "tab", Token: token, Instruction: "go"}, initial)
	_ = waitEvent(t, initial, "toolResult")
	pauseArgs := map[string]any{"tabId": "tab", "token": token}
	if response := dispatchTakeover(t, dispatcher, bob, "ai_takeover_pause", pauseArgs, ipc.Environment{}); response.OK {
		t.Fatal("cross-owner takeover pause succeeded")
	}
	if response := dispatchTakeover(t, dispatcher, alice, "ai_takeover_pause", pauseArgs, ipc.Environment{}); !response.OK {
		t.Fatalf("owner pause failed: %+v", response)
	}
	close(release)
	paused := waitEvent(t, initial, "paused")
	waitTakeoverPauseReady(t, h.manager, run.JobID)
	resumeArgs := ResumeArgs{TabID: "tab", Token: token, JobID: run.JobID}
	if response := dispatchTakeover(t, dispatcher, bob, "ai_takeover_resume", resumeArgs, takeoverStreamEnvironment(&agent.SliceStream{})); response.OK {
		t.Fatal("cross-owner takeover resume succeeded")
	}
	factoryFailure := ipc.Environment{Streams: ipc.StreamFactoryFuncs{JSON: func(context.Context, ipc.ChannelRef) (ipc.JSONStream, error) {
		return nil, errors.New("stream factory unavailable")
	}}}
	if response := dispatchTakeover(t, dispatcher, alice, "ai_takeover_resume", resumeArgs, factoryFailure); response.OK {
		t.Fatal("resume unexpectedly succeeded with a failing stream factory")
	}
	waitTakeoverPauseReady(t, h.manager, run.JobID)
	if _, closed := initial.Snapshot(); closed {
		t.Fatal("failed resume closed the paused stream")
	}
	resumed := &agent.SliceStream{}
	response := dispatchTakeover(t, dispatcher, alice, "ai_takeover_resume", resumeArgs, takeoverStreamEnvironment(resumed))
	resumedRun := takeoverResponseData[RunResponse](t, response)
	if resumedRun.JobID != run.JobID {
		t.Fatalf("resumed job = %s, want %s", resumedRun.JobID, run.JobID)
	}
	final := waitClosed(t, resumed)
	if len(final) == 0 || final[0].Seq != paused.Seq+1 {
		t.Fatalf("first resumed event = %+v, paused seq = %d", final, paused.Seq)
	}
	for index, event := range final {
		if want := paused.Seq + uint64(index) + 1; event.Seq != want {
			t.Fatalf("resumed event %d seq = %d, want %d: %+v", index, event.Seq, want, final)
		}
	}
	if done, failed := counts(final); done != 1 || failed != 0 {
		t.Fatalf("resumed events = %+v", final)
	}
}

func TestDispatcherTakeoverCancelChecksOwner(t *testing.T) {
	entered := make(chan struct{})
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	h := newHarness(t, chat)
	dispatcher := ipc.NewDispatcher()
	if err := h.manager.RegisterCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	alice := ipc.WithUserID(context.Background(), "account-a")
	bob := ipc.WithUserID(context.Background(), "account-b")
	token := enterTakeoverIPC(t, dispatcher, alice, "tab")
	stream := &agent.SliceStream{}
	run := runTakeoverIPC(t, dispatcher, alice, RunArgs{TabID: "tab", Token: token, Instruction: "go"}, stream)
	<-entered
	args := map[string]any{"jobId": run.JobID}
	if response := dispatchTakeover(t, dispatcher, bob, "ai_takeover_cancel", args, ipc.Environment{}); response.OK {
		t.Fatal("cross-owner takeover cancel succeeded")
	}
	if response := dispatchTakeover(t, dispatcher, alice, "ai_takeover_cancel", args, ipc.Environment{}); !response.OK {
		t.Fatalf("owner cancel failed: %+v", response)
	}
	events := waitClosed(t, stream)
	if done, failed := counts(events); done != 1 || failed != 0 {
		t.Fatalf("events = %+v", events)
	}
}
