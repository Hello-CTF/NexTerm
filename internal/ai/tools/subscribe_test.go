package tools

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

func TestValidateSubscribeArgs(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	got, err := ValidateSubscribeArgs(now, TerminalSubscribeArgs{On: "finished"})
	if err != nil || got.On != "just_finished" || !got.ExpiresAt.Equal(now.Add(time.Hour)) || got.Repeat {
		t.Fatalf("finished = %+v, %v", got, err)
	}
	got, err = ValidateSubscribeArgs(now, TerminalSubscribeArgs{On: "output", Pattern: "ERROR \\d+", Regex: true, Repeat: true, ExpiresInMinutes: 5})
	if err != nil || got.On != "output" || !got.Regex || !got.Repeat || !got.ExpiresAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("output = %+v, %v", got, err)
	}
	for name, args := range map[string]TerminalSubscribeArgs{
		"unknown on":          {On: "running"},
		"state with pattern":  {On: "needs_me", Pattern: "x"},
		"output without text": {On: "output"},
		"expiry too long":     {On: "exited", ExpiresInMinutes: 24*60 + 1},
		"negative expiry":     {On: "exited", ExpiresInMinutes: -1},
	} {
		if _, err := ValidateSubscribeArgs(now, args); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// A subscription starts unattended runs later, so creating one is never
// silent, and an unattended run cannot create another.
func TestSubscribeNeedsConfirmationAndIsDeniedUnattended(t *testing.T) {
	ruling := guard.ClassifyTool(SubscribeTool, []byte(`{"on":"finished"}`), guard.Config{})
	if ruling.Risk != guard.NeedsConfirm {
		t.Fatalf("ruling = %+v", ruling)
	}
	decision := guard.Decide(guard.Config{Mode: guard.Unattended}, ruling, guard.NewMemory())
	if decision.Action != guard.ActionDeny {
		t.Fatalf("unattended decision = %+v", decision)
	}
}

type fakeSubscriber struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeSubscriber) Subscribe(context.Context, string, Subscription) (SubscriptionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return SubscriptionResult{ID: "watch-1", ExpiresAt: time.Date(2026, 11, 25, 13, 0, 0, 0, time.UTC)}, nil
}

func (f *fakeSubscriber) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// A session approval of reminders does not approve subscriptions: each fire of
// a subscription starts an unattended run, so it has its own approval kind.
func TestRememberedReminderApprovalDoesNotApproveSubscriptions(t *testing.T) {
	t.Parallel()
	reminder := guard.ClassifyTool(ReminderTool, json.RawMessage(`{"message":"检查服务","delay_minutes":30}`), guard.Config{})
	subscribe := guard.ClassifyTool(SubscribeTool, json.RawMessage(`{"on":"finished"}`), guard.Config{})
	if reminder.Kind == subscribe.Kind {
		t.Fatalf("reminder and subscription share approval kind %q", reminder.Kind)
	}
	call := Call{ID: "c1", Name: SubscribeTool, Args: json.RawMessage(`{"on":"finished"}`)}
	run := func(memory *guard.Memory) (*fakeSubscriber, Output, error) {
		subscriber := &fakeSubscriber{}
		execution := &Execution{
			JobID: "run-1", ConversationID: "conv-1", Scope: Scope{TabID: "tab-1"},
			Registry:   NewRegistry(Dependencies{Terminal: &fakeTerminal{}, Subscriptions: subscriber}),
			Permission: guard.Config{Mode: guard.ReadWrite}, Memory: memory,
		}
		output, err := execution.initial(context.Background(), call)
		return subscriber, output, err
	}

	rememberedReminder := guard.NewMemory()
	rememberedReminder.Remember(reminder)
	subscriber, output, err := run(rememberedReminder)
	if err == nil {
		t.Fatalf("subscription ran without confirmation under a remembered reminder approval: %+v", output)
	}
	if subscriber.count() != 0 {
		t.Fatalf("subscriber called %d times before confirmation", subscriber.count())
	}

	// Control: a session approval of subscriptions does cover the next one.
	rememberedSubscribe := guard.NewMemory()
	rememberedSubscribe.Remember(subscribe)
	subscriber, output, err = run(rememberedSubscribe)
	if err != nil || !output.OK || subscriber.count() != 1 {
		t.Fatalf("remembered subscription approval: output = %+v err = %v calls = %d", output, err, subscriber.count())
	}
}
