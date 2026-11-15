package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// loopingModel requests a new tool call on every turn and records the input of
// each model call.
type loopingModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
}

func (l *loopingModel) model() *fakeModel {
	return &fakeModel{stream: func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		l.mu.Lock()
		l.inputs = append(l.inputs, append([]*schema.Message(nil), messages...))
		id := fmt.Sprintf("call-%d", len(l.inputs))
		l.mu.Unlock()
		return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall(id, "todo_write", `{"todos":[]}`))}), nil
	}}
}

func (l *loopingModel) calls() [][]*schema.Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][]*schema.Message(nil), l.inputs...)
}

func settingsRunner(t *testing.T, chat model.BaseChatModel, serverMode bool) (*Runner, *store.Store) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model:      func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools:      tools.NewRegistry(tools.Dependencies{}),
		Store:      storage,
		Settings:   storage,
		ServerMode: serverMode,
	})
	t.Cleanup(func() {
		_ = runner.Close()
		_ = storage.Close()
	})
	return runner, storage
}

func stepLimitTerminal(t *testing.T, events []Event) Event {
	t.Helper()
	terminal := events[len(events)-1]
	if terminal.Type != "error" || !terminal.MaxIterations || terminal.Retryable {
		t.Fatalf("terminal event = %+v", terminal)
	}
	return terminal
}

func TestStepLimitSettingAppliesToNextRun(t *testing.T) {
	loop := &loopingModel{}
	runner, _ := settingsRunner(t, loop.model(), false)
	ctx := context.Background()

	if settings, err := runner.RunSettings(ctx); err != nil || settings.MaxTurns != DefaultMaxTurns {
		t.Fatalf("default settings = %+v, %v", settings, err)
	}
	if _, err := runner.SetRunSettings(ctx, RunSettingsInput{MaxTurns: 3}); err != nil {
		t.Fatal(err)
	}
	first := &SliceStream{}
	response := startTestJob(t, runner, first, "go")
	terminal := stepLimitTerminal(t, waitClosed(t, first))
	if !strings.Contains(terminal.Message, "（3）") {
		t.Fatalf("first terminal message = %q", terminal.Message)
	}
	if got := len(loop.calls()); got != 3 {
		t.Fatalf("first run model calls = %d, want 3", got)
	}

	// A changed limit applies to the next run of the same runner, and the
	// continuation resumes from the persisted tool history.
	if _, err := runner.SetRunSettings(ctx, RunSettingsInput{MaxTurns: 2}); err != nil {
		t.Fatal(err)
	}
	second := &SliceStream{}
	if _, err := runner.Start(ctx, ChatArgs{ConversationID: response.ConversationID, Message: "继续", Scope: tools.Scope{SessionID: "session"}}, StaticStream(second)); err != nil {
		t.Fatal(err)
	}
	terminal = stepLimitTerminal(t, waitClosed(t, second))
	if !strings.Contains(terminal.Message, "（2）") {
		t.Fatalf("second terminal message = %q", terminal.Message)
	}
	calls := loop.calls()
	if len(calls) != 5 {
		t.Fatalf("total model calls = %d, want 5", len(calls))
	}
	assertContinuationHistory(t, calls[3])
}

// assertContinuationHistory checks that the continuation input pairs every
// earlier tool call with its result and ends the old turns with the new user
// message.
func assertContinuationHistory(t *testing.T, messages []*schema.Message) {
	t.Helper()
	calls := 0
	continued := false
	for index, message := range messages {
		if message.Role == schema.User && message.Content == "继续" {
			continued = true
			continue
		}
		if message.Role != schema.Assistant || len(message.ToolCalls) == 0 {
			continue
		}
		if continued {
			t.Fatalf("tool call after the continuation message: %s", describeMessages(messages))
		}
		for offset, call := range message.ToolCalls {
			next := index + 1 + offset
			if next >= len(messages) || messages[next].Role != schema.Tool || messages[next].ToolCallID != call.ID {
				t.Fatalf("tool call %s has no paired result: %s", call.ID, describeMessages(messages))
			}
			calls++
		}
	}
	if !continued || calls != 3 {
		t.Fatalf("continuation input: continued=%v calls=%d: %s", continued, calls, describeMessages(messages))
	}
}

func TestStepLimitSettingRejectsAndIgnoresOutOfRange(t *testing.T) {
	runner, storage := settingsRunner(t, sequenceModel(), false)
	ctx := context.Background()

	for _, value := range []int{0, -1, 1000} {
		_, err := runner.SetRunSettings(ctx, RunSettingsInput{MaxTurns: value})
		var appErr *ipc.Error
		if !errors.As(err, &appErr) || appErr.Code != ipc.CodeBadParam || !errors.Is(err, ErrMaxTurnsRange) {
			t.Fatalf("SetRunSettings(%d) error = %v", value, err)
		}
	}
	if _, found, err := storage.SettingGet(ctx, MaxTurnsSettingKey); err != nil || found {
		t.Fatalf("rejected value was stored: found=%v err=%v", found, err)
	}

	for _, raw := range []string{"0", "1000", "-5", "abc", "", "12.5"} {
		if err := storage.SettingSet(ctx, MaxTurnsSettingKey, raw); err != nil {
			t.Fatal(err)
		}
		if got := runner.maxTurns(ctx); got != DefaultMaxTurns {
			t.Fatalf("stored %q: limit = %d, want %d", raw, got, DefaultMaxTurns)
		}
		if settings, err := runner.RunSettings(ctx); err != nil || settings.MaxTurns != DefaultMaxTurns {
			t.Fatalf("stored %q: settings = %+v, %v", raw, settings, err)
		}
	}
	for _, value := range []int{1, 999} {
		settings, err := runner.SetRunSettings(ctx, RunSettingsInput{MaxTurns: value})
		if err != nil || settings.MaxTurns != value || runner.maxTurns(ctx) != value {
			t.Fatalf("SetRunSettings(%d) = %+v, %v", value, settings, err)
		}
	}
}

func TestStepLimitSettingIsPerServerAccount(t *testing.T) {
	runner, _ := settingsRunner(t, sequenceModel(), true)
	alice := ipc.WithUserID(context.Background(), "alice")
	bob := ipc.WithUserID(context.Background(), "bob")

	if _, err := runner.SetRunSettings(alice, RunSettingsInput{MaxTurns: 5}); err != nil {
		t.Fatal(err)
	}
	if settings, err := runner.RunSettings(alice); err != nil || settings.MaxTurns != 5 {
		t.Fatalf("alice settings = %+v, %v", settings, err)
	}
	if settings, err := runner.RunSettings(bob); err != nil || settings.MaxTurns != DefaultMaxTurns {
		t.Fatalf("bob settings = %+v, %v", settings, err)
	}
	var appErr *ipc.Error
	if _, err := runner.RunSettings(context.Background()); !errors.As(err, &appErr) || appErr.Code != ipc.CodeForbidden {
		t.Fatalf("anonymous read error = %v", err)
	}
	if settings, err := runner.RunSettings(owner.WithOpenAccess(context.Background())); err != nil || settings.MaxTurns != DefaultMaxTurns {
		t.Fatalf("open access settings = %+v, %v", settings, err)
	}
}
