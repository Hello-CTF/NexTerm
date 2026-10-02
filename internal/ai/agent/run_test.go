package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func testRunner(t *testing.T, chat model.BaseChatModel, deps tools.Dependencies, maxTurns int) (*Runner, *store.Store) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools: tools.NewRegistry(deps), Store: storage, MaxTurns: maxTurns,
	})
	t.Cleanup(func() {
		_ = runner.Close()
		_ = storage.Close()
	})
	return runner, storage
}

func startTestJob(t *testing.T, runner *Runner, stream *SliceStream, message string) StartResponse {
	t.Helper()
	response, err := runner.Start(context.Background(), ChatArgs{Message: message, Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func waitClosed(t *testing.T, stream *SliceStream) []Event {
	t.Helper()
	return waitClosedTimeout(t, stream, 5*time.Second)
}

func waitClosedTimeout(t *testing.T, stream *SliceStream, timeout time.Duration) []Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		events, closed := stream.Snapshot()
		if closed {
			return events
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for AI stream close")
	return nil
}

func waitEvent(t *testing.T, stream *SliceStream, kind string) Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	seen := 0
	for time.Now().Before(deadline) {
		events, _ := stream.Snapshot()
		for ; seen < len(events); seen++ {
			if events[seen].Type == kind {
				return events[seen]
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", kind)
	return Event{}
}

func terminalCounts(events []Event) (done, failed int) {
	for _, event := range events {
		switch event.Type {
		case "done":
			done++
		case "error":
			failed++
		}
	}
	return
}

func TestRunSuccessPersistsConversationAndSingleDone(t *testing.T) {
	first := schema.AssistantMessage("完", nil)
	first.ReasoningContent = "分析"
	second := assistantWithUsage("成", 3, 2)
	chat := &fakeModel{steps: []fakeStep{{chunks: []*schema.Message{first, second}}}}
	runner, storage := testRunner(t, chat, tools.Dependencies{}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, " hello\nworld ")
	events := waitClosed(t, stream)
	done, failed := terminalCounts(events)
	if done != 1 || failed != 0 {
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
	if events[len(events)-1].Answer != "完成" || events[len(events)-1].TokensIn != 3 || events[len(events)-1].TokensOut != 2 {
		t.Fatalf("unexpected done: %+v", events[len(events)-1])
	}
	messages, err := runner.Messages(context.Background(), response.ConversationID)
	if err != nil || len(messages) != 2 {
		t.Fatalf("messages=%d err=%v", len(messages), err)
	}
	conversations, err := storage.ConvList(context.Background())
	if err != nil || len(conversations) != 1 || conversations[0].Title != "hello" {
		t.Fatalf("conversations=%+v err=%v", conversations, err)
	}
}

func TestTerminalExactlyOnceForFailureModes(t *testing.T) {
	toolCall := namedToolCall("loop", "todo_write", `{"todos":[]}`)
	tests := []struct {
		name string
		chat model.BaseChatModel
		max  int
	}{
		{"provider-error", &fakeModel{stream: func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return nil, errors.New("boom")
		}}, 0},
		{"panic", &fakeModel{stream: func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			panic("boom")
		}}, 0},
		{"max-turn", sequenceModel(toolCallMessage(toolCall)), 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, _ := testRunner(t, test.chat, tools.Dependencies{}, test.max)
			stream := &SliceStream{}
			startTestJob(t, runner, stream, "go")
			events := waitClosed(t, stream)
			done, failed := terminalCounts(events)
			if done != 0 || failed != 1 {
				t.Fatalf("terminal counts done=%d error=%d events=%+v", done, failed, events)
			}
		})
	}
}

func TestCancelProviderAndConfirmation(t *testing.T) {
	t.Run("provider", func(t *testing.T) {
		chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		runner, _ := testRunner(t, chat, tools.Dependencies{}, 0)
		stream := &SliceStream{}
		response := startTestJob(t, runner, stream, "go")
		if err := runner.Cancel(response.JobID); err != nil {
			t.Fatal(err)
		}
		events := waitClosed(t, stream)
		done, failed := terminalCounts(events)
		if done != 0 || failed != 1 {
			t.Fatalf("done=%d error=%d", done, failed)
		}
	})

	t.Run("confirmation", func(t *testing.T) {
		chat := sequenceModel(toolCallMessage(namedToolCall("call", "docker_control", `{"container_id":"web","action":"start"}`)))
		runner, _ := testRunner(t, chat, tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}, 0)
		stream := &SliceStream{}
		response := startTestJob(t, runner, stream, "go")
		_ = waitEvent(t, stream, "confirmRequired")
		if err := runner.Cancel(response.JobID); err != nil {
			t.Fatal(err)
		}
		events := waitClosed(t, stream)
		done, failed := terminalCounts(events)
		if done != 0 || failed != 1 {
			t.Fatalf("done=%d error=%d", done, failed)
		}
	})
}

func TestConfirmationBoundToCallAndNonce(t *testing.T) {
	var actions atomic.Int64
	chat := sequenceModel(
		toolCallMessage(namedToolCall("call", "docker_control", `{"container_id":"web","action":"start"}`)),
		schema.AssistantMessage("done", nil),
	)
	runner, _ := testRunner(t, chat, tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { actions.Add(1); return nil }}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")
	if confirmation.Nonce == "" {
		t.Fatal("missing confirmation nonce")
	}
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: "other", Nonce: confirmation.Nonce, Decision: "allow"}); !errors.Is(err, ErrConfirmationStale) {
		t.Fatalf("wrong call confirmation: %v", err)
	}
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if actions.Load() != 1 {
		t.Fatalf("actions=%d", actions.Load())
	}
	done, failed := terminalCounts(events)
	if done != 1 || failed != 0 {
		t.Fatalf("done=%d error=%d", done, failed)
	}
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err == nil {
		t.Fatal("duplicate confirmation accepted")
	}
}

func TestAskUserWaitsForBoundAnswer(t *testing.T) {
	var calls atomic.Int64
	chat := &fakeModel{stream: func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("ask", "ask_user", `{"question":"继续？","options":["继续","停止"]}`))}), nil
		}
		found := false
		for _, message := range messages {
			if message.Role == schema.Tool && strings.Contains(message.Content, "用户回答：继续") {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("missing resumed answer in model input")
		}
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
	}}
	runner, _ := testRunner(t, chat, tools.Dependencies{}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	question := waitEvent(t, stream, "questionRequired")
	if err := runner.Answer(Answer{JobID: response.JobID, CallID: question.ID, Nonce: "wrong", Text: "继续"}); !errors.Is(err, ErrConfirmationStale) {
		t.Fatalf("wrong nonce: %v", err)
	}
	if err := runner.Answer(Answer{JobID: response.JobID, CallID: question.ID, Nonce: question.Nonce, Text: "继续"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	done, failed := terminalCounts(events)
	if done != 1 || failed != 0 {
		t.Fatalf("done=%d error=%d", done, failed)
	}
}

func TestPlanMode(t *testing.T) {
	t.Run("submit", func(t *testing.T) {
		chat := sequenceModel(toolCallMessage(namedToolCall("plan", "exit_plan_mode", `{"plan":"先读取，再修改"}`)))
		runner, _ := testRunner(t, chat, tools.Dependencies{}, 0)
		stream := &SliceStream{}
		_, err := runner.Start(context.Background(), ChatArgs{Message: "plan", PlanMode: true}, StaticStream(stream))
		if err != nil {
			t.Fatal(err)
		}
		events := waitClosed(t, stream)
		_ = waitEvent(t, stream, "planSubmitted")
		done, failed := terminalCounts(events)
		if done != 1 || failed != 0 || events[len(events)-1].Answer != "先读取，再修改" {
			t.Fatalf("events=%+v", events)
		}
	})

	t.Run("side effect denied", func(t *testing.T) {
		called := atomic.Bool{}
		chat := sequenceModel(
			toolCallMessage(namedToolCall("exec", "exec_commands", `{"commands":["ls"]}`)),
			schema.AssistantMessage("done", nil),
		)
		runner, _ := testRunner(t, chat, tools.Dependencies{Transport: func(context.Context, string) (base.Transport, error) { called.Store(true); return nil, nil }}, 0)
		stream := &SliceStream{}
		_, err := runner.Start(context.Background(), ChatArgs{Message: "plan", PlanMode: true, Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream))
		if err != nil {
			t.Fatal(err)
		}
		events := waitClosed(t, stream)
		if called.Load() {
			t.Fatal("plan mode resolved command transport")
		}
		result := waitEvent(t, stream, "toolResult")
		if result.OK {
			t.Fatal("exec succeeded in plan mode")
		}
		done, _ := terminalCounts(events)
		if done != 1 {
			t.Fatal("run did not finish")
		}
	})
}

func TestConcurrentJobsTerminalRace(t *testing.T) {
	chat := sequenceModel(schema.AssistantMessage("ok", nil))
	runner, _ := testRunner(t, chat, tools.Dependencies{}, 0)
	const count = 12
	streams := make([]*SliceStream, count)
	var wg sync.WaitGroup
	errorsCh := make(chan error, count)
	for i := 0; i < count; i++ {
		streams[i] = &SliceStream{}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := runner.Start(context.Background(), ChatArgs{Message: fmt.Sprintf("job-%d", index)}, StaticStream(streams[index]))
			errorsCh <- err
		}(i)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, stream := range streams {
		events := waitClosed(t, stream)
		done, failed := terminalCounts(events)
		if done != 1 || failed != 0 {
			t.Fatalf("done=%d error=%d", done, failed)
		}
	}
}
