package takeover

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/agent"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type harness struct {
	manager  *Manager
	mu       sync.Mutex
	screen   tools.Screen
	aiWrites [][]byte
	banners  []string
	audits   []tools.AuditEntry
	writeAI  func([]byte) error
	snapshot func(context.Context, string) (tools.Screen, error)
}

func newHarness(t *testing.T, chat model.BaseChatModel) *harness {
	return newHarnessWith(t, chat, nil)
}

func newHarnessWith(t *testing.T, chat model.BaseChatModel, configure func(*Dependencies)) *harness {
	h := &harness{screen: tools.Screen{Text: "$ ", Tail: []string{"$ "}, IdleMS: 301, CursorCol: 2}}
	deps := Dependencies{
		Model:      func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Permission: func(context.Context) (guard.Config, error) { return guard.Config{Mode: guard.ReadWrite}, nil },
		Snapshot: func(ctx context.Context, tabID string) (tools.Screen, error) {
			h.mu.Lock()
			custom := h.snapshot
			screen := h.screen
			h.mu.Unlock()
			if custom != nil {
				return custom(ctx, tabID)
			}
			return screen, nil
		},
		PollInterval: time.Millisecond,
	}
	deps.WriteAI = func(_ context.Context, _ string, data []byte) error {
		h.mu.Lock()
		h.aiWrites = append(h.aiWrites, append([]byte(nil), data...))
		custom := h.writeAI
		h.mu.Unlock()
		if custom != nil {
			return custom(data)
		}
		return nil
	}
	deps.Inject = func(_ context.Context, _ string, data []byte) error {
		h.mu.Lock()
		h.banners = append(h.banners, string(data))
		h.mu.Unlock()
		return nil
	}
	deps.Audit = func(_ context.Context, entry tools.AuditEntry) error {
		h.mu.Lock()
		h.audits = append(h.audits, entry)
		h.mu.Unlock()
		return nil
	}
	if configure != nil {
		configure(&deps)
	}
	h.manager = NewManager(deps)
	t.Cleanup(func() { _ = h.manager.Close() })
	return h
}

func (h *harness) run(t *testing.T, stream *agent.SliceStream, args RunArgs) RunResponse {
	t.Helper()
	if args.TabID == "" {
		args.TabID = "tab"
	}
	if args.Instruction == "" {
		args.Instruction = "do it"
	}
	response, err := h.manager.Run(context.Background(), args, agent.StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func waitClosed(t *testing.T, stream *agent.SliceStream) []agent.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, closed := stream.Snapshot()
		if closed {
			return events
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("takeover stream did not close")
	return nil
}

func waitEvent(t *testing.T, stream *agent.SliceStream, kind string) agent.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, _ := stream.Snapshot()
		for _, event := range events {
			if event.Type == kind {
				return event
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("missing event %s", kind)
	return agent.Event{}
}

func counts(events []agent.Event) (done, failed int) {
	for _, event := range events {
		if event.Type == "done" {
			done++
		}
		if event.Type == "error" {
			failed++
		}
	}
	return
}

func TestDoneAndProviderFailureHaveSingleTerminal(t *testing.T) {
	t.Run("done", func(t *testing.T) {
		h := newHarness(t, sequence(toolCallMessage(doneCall("finished"))))
		stream := &agent.SliceStream{}
		h.run(t, stream, RunArgs{})
		events := waitClosed(t, stream)
		done, failed := counts(events)
		if done != 1 || failed != 0 || events[len(events)-1].Answer != "finished" {
			t.Fatalf("events=%+v", events)
		}
		h.mu.Lock()
		banners := strings.Join(h.banners, "\n")
		h.mu.Unlock()
		if !strings.Contains(banners, "AI 正在操作") || !strings.Contains(banners, "接管结束: 任务完成") {
			t.Fatalf("banners=%q", banners)
		}
	})

	t.Run("provider error", func(t *testing.T) {
		h := newHarness(t, &fakeModel{stream: func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return nil, errors.New("provider failed")
		}})
		stream := &agent.SliceStream{}
		h.run(t, stream, RunArgs{})
		events := waitClosed(t, stream)
		done, failed := counts(events)
		if done != 0 || failed != 1 {
			t.Fatalf("events=%+v", events)
		}
	})
}

func TestUserPreemptionPreventsSubsequentAIWrite(t *testing.T) {
	chat := sequence(toolCallMessage(
		namedToolCall("one", "send_keys", `{"keys":"ls","enter":true}`),
		namedToolCall("two", "send_keys", `{"keys":"pwd","enter":true}`),
	))
	h := newHarness(t, chat)
	entered := make(chan struct{})
	release := make(chan struct{})
	h.writeAI = func([]byte) error {
		close(entered)
		<-release
		return nil
	}
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	<-entered
	userDone := make(chan struct{})
	go func() {
		h.manager.Preempt("tab")
		close(userDone)
	}()
	time.Sleep(5 * time.Millisecond)
	close(release)
	<-userDone
	events := waitClosed(t, stream)
	done, failed := counts(events)
	if done != 1 || failed != 0 {
		t.Fatalf("events=%+v", events)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 {
		t.Fatalf("ai writes=%d", len(h.aiWrites))
	}
}

func TestStaleExitCannotCancelNewerTakeover(t *testing.T) {
	h := newHarness(t, sequence(toolCallMessage(doneCall("ok"))))
	first, err := h.manager.Enter(context.Background(), "tab")
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.manager.Enter(context.Background(), "tab")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.manager.Exit(context.Background(), "tab", first, "stale"); !errors.Is(err, ErrStaleOwnership) {
		t.Fatalf("stale exit: %v", err)
	}
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{Token: second})
	events := waitClosed(t, stream)
	done, failed := counts(events)
	if done != 1 || failed != 0 {
		t.Fatalf("events=%+v", events)
	}
}

func TestAllowWriteFalseRejectsSendKeys(t *testing.T) {
	chat := sequence(toolCallMessage(
		namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`),
		doneCall("stopped"),
	))
	h := newHarness(t, chat)
	stream := &agent.SliceStream{}
	allow := false
	h.run(t, stream, RunArgs{AllowWrite: &allow})
	events := waitClosed(t, stream)
	result := waitEvent(t, stream, "toolResult")
	if result.OK {
		t.Fatal("send_keys succeeded with allowWrite=false")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 0 {
		t.Fatal("terminal was written")
	}
	done, _ := counts(events)
	if done != 1 {
		t.Fatal("takeover did not finish")
	}
}

func TestTakeoverConfirmationAndActualEnter(t *testing.T) {
	chat := sequence(toolCallMessage(
		namedToolCall("keys", "send_keys", `{"keys":"sudo reboot<enter>","enter":false}`),
		doneCall("ok"),
	))
	h := newHarness(t, chat)
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	confirmation := waitEvent(t, stream, "confirmRequired")
	if confirmation.Risk != "danger" {
		t.Fatalf("risk=%s", confirmation.Risk)
	}
	if err := h.manager.Confirm(agent.Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, stream)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 1 || string(h.aiWrites[0]) != "sudo reboot\r" {
		t.Fatalf("writes=%q", h.aiWrites)
	}
}

func TestWaitForCancelAndInvalidRegex(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		chat := sequence(toolCallMessage(namedToolCall("wait", "wait_for", `{"pattern":"never","timeout_ms":60000}`)))
		h := newHarness(t, chat)
		stream := &agent.SliceStream{}
		response := h.run(t, stream, RunArgs{})
		_ = waitEvent(t, stream, "toolCall")
		if err := h.manager.Cancel(response.JobID); err != nil {
			t.Fatal(err)
		}
		events := waitClosed(t, stream)
		done, failed := counts(events)
		if done != 1 || failed != 0 {
			t.Fatalf("events=%+v", events)
		}
	})

	t.Run("invalid regex", func(t *testing.T) {
		chat := sequence(
			toolCallMessage(namedToolCall("wait", "wait_for", `{"pattern":"[","timeout_ms":60000}`)),
			toolCallMessage(doneCall("ok")),
		)
		h := newHarness(t, chat)
		stream := &agent.SliceStream{}
		h.run(t, stream, RunArgs{})
		events := waitClosed(t, stream)
		result := waitEvent(t, stream, "toolResult")
		if !strings.Contains(result.Text, "正则无效") {
			t.Fatalf("result=%+v", result)
		}
		done, _ := counts(events)
		if done != 1 {
			t.Fatal("no done")
		}
	})
}
