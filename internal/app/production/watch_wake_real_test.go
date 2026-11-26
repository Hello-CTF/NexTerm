package production

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ai/watches"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// scriptedModel reads the screen once, then answers.
type scriptedModel struct {
	mu    sync.Mutex
	calls int
}

func (m *scriptedModel) reply() *schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.calls == 1 {
		return schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "read_screen", Arguments: `{}`}}})
	}
	return schema.AssistantMessage("build finished", nil)
}

func (m *scriptedModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return m.reply(), nil
}

func (m *scriptedModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.reply()}), nil
}

type wakeScreenTerminal struct {
	mu    sync.Mutex
	reads []string
}

func (t *wakeScreenTerminal) Snapshot(_ context.Context, tabID string) (tools.Screen, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reads = append(t.reads, tabID)
	return tools.Screen{Text: "$ make\nok\n$ "}, nil
}

func (t *wakeScreenTerminal) Write(context.Context, string, []byte) error { return nil }

func (t *wakeScreenTerminal) readTabs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.reads...)
}

// A woken conversation runs for real: the cron executor starts an unattended run through the agent runner, the
// model reads the tab that fired, the run finishes, and its trigger note is stored as an event row, for an account
// owner and for open access.
func TestEventWakeRunsTheRealAgentAndReadsTheTab(t *testing.T) {
	for name, owner := range map[string]string{"account": "user-1", "open": ""} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if owner != "" {
				ctx = ipc.WithUserID(ctx, owner)
			}
			storage, err := store.OpenInMemory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			terminal := &wakeScreenTerminal{}
			chat := &scriptedModel{}
			runner := agent.NewRunner(agent.Config{
				Model: func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
				Tools: tools.NewRegistry(tools.Dependencies{Terminal: terminal}), Store: storage, Settings: storage, MaxTurns: 8,
			})
			t.Cleanup(func() {
				_ = runner.Close()
				_ = storage.Close()
			})
			conversation, err := storage.ConvCreate(ctx, "build", map[string]any{"scope": tools.Scope{TabID: "tab-1"}})
			if err != nil {
				t.Fatal(err)
			}
			executor := &cron.AgentExecutor{
				Runner: runner,
				ScopeFor: func(context.Context, cron.Trigger) (tools.Scope, error) {
					return tools.Scope{TabID: "tab-1"}, nil
				},
			}
			waker := newEventWaker(executor, nil, nil)
			t.Cleanup(waker.Close)

			waker.Wake(ctx, conversation.ID, watches.Event{DeliveryID: "d1", WatchID: "w1", TargetID: "tab-1", FiredAt: time.Now().UnixMilli()})

			deadline := time.Now().Add(15 * time.Second)
			var rows []store.MessageRow
			for time.Now().Before(deadline) {
				rows, err = storage.MsgList(ctx, conversation.ID)
				if err != nil {
					t.Fatal(err)
				}
				if hasAssistantText(rows, "build finished") {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !hasAssistantText(rows, "build finished") {
				t.Fatalf("the woken run did not finish; rows: %d", len(rows))
			}
			if got := terminal.readTabs(); len(got) == 0 || got[0] != "tab-1" {
				t.Fatalf("the woken run read tabs %v; want tab-1", got)
			}
			var trigger map[string]any
			for _, row := range rows {
				var content map[string]any
				if json.Unmarshal([]byte(row.ContentJSON), &content) == nil && content["role"] == "user" {
					trigger = content
				}
			}
			if trigger == nil || trigger["source"] != agent.SourceEvent || !strings.Contains(trigger["content"].(string), "watch_event") {
				t.Fatalf("trigger row = %v; want the fixed event note tagged source=event", trigger)
			}
		})
	}
}

func hasAssistantText(rows []store.MessageRow, text string) bool {
	for _, row := range rows {
		var content map[string]any
		if json.Unmarshal([]byte(row.ContentJSON), &content) == nil && content["role"] == "assistant" && content["content"] == text {
			return true
		}
	}
	return false
}
