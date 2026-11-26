package production

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ai/watches"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
)

// A conversation's subscription to a terminal state reaches that
// conversation's history as watch tool data and wakes it once; republishing
// the same state does not wake it again.
func TestTerminalSubscriptionWakesTheConversationOnANewState(t *testing.T) {
	database, sessions, events := newWatchTestFixture(t)
	executor := &wakeExecutor{results: make(chan error, 4), calls: make(chan cron.Trigger, 4)}
	waker := newEventWaker(executor, nil, nil)
	t.Cleanup(waker.Close)
	service := newWatchService(database, sessions, events, agent.NewRoster(database, nil), nil, waker)
	service.dispatchInterval = 5 * time.Millisecond
	service.pollInterval = 5 * time.Millisecond
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })

	ctx := watchOwnerCtx("owner-a")
	tab := registerWatchTargetTab(t, sessions, "owner-a")
	conversation, err := database.ConvCreate(ctx, "subscribed", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Subscribe(ctx, conversation.ID, tools.Subscription{TabID: tab.ID, On: watches.StateJustFinished, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if result.ID == "" {
		t.Fatal("subscription has no id")
	}

	eventCtx := ipc.WithEventOwner(context.Background(), "owner-a")
	since := time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
	finished := session.Event{Topic: session.TopicTerminalAttention, Payload: session.AttentionEvent{TabID: tab.ID, State: watches.StateJustFinished, Since: since}}
	service.onAttention(eventCtx, finished)

	select {
	case trigger := <-executor.calls:
		if trigger.SessionID != conversation.ID || trigger.Source != agent.SourceEvent {
			t.Fatalf("trigger = %+v", trigger)
		}
		executor.results <- nil
	case <-time.After(15 * time.Second):
		t.Fatal("the subscribed conversation was not woken")
	}
	rows, err := database.MsgList(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	deliveries := agent.WatchHistoryToolData(rows)
	if len(deliveries) != 1 || deliveries[0].Event.Condition.Pattern != watches.StateJustFinished || !deliveries[0].Untrusted {
		t.Fatalf("history deliveries = %+v", deliveries)
	}

	// The same state republished (seen, progress) fires nothing new.
	service.onAttention(eventCtx, finished)
	time.Sleep(100 * time.Millisecond)
	select {
	case trigger := <-executor.calls:
		t.Fatalf("republished state woke the conversation again: %+v", trigger)
	default:
	}
}
