//go:build darwin || linux

package production

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/Hello-CTF/NexTerm/internal/transport/local"
)

// The real CLI boundary: a real local shell's output flows through the real
// session stack into the watch engine. Nothing in this file simulates the
// stream — the only synthetic surface is the captured UI emitter.
func newWatchRealCLIFixture(t *testing.T) (*store.Store, *session.Manager, *watchEventCapture) {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager(session.Config{
		IdleTimeout: -1,
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
		}),
	})
	t.Cleanup(func() {
		_ = sessions.Close()
		_ = database.Close()
	})
	return database, sessions, &watchEventCapture{}
}

func openWatchRealCLITab(t *testing.T, ctx context.Context, sessions *session.Manager) session.TabInfo {
	t.Helper()
	connected, err := sessions.Connect(ctx, session.Asset{ID: "asset-local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := sessions.OpenTabFor(ctx, session.OpenTabOptions{
		SessionID: connected.ID, ClientID: "client-a", ChannelID: "a-1", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

func provisionWatchConversation(t *testing.T, database *store.Store, ctx context.Context, tabID string) string {
	t.Helper()
	row, err := database.ConvCreate(ctx, "watch conv", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.AgentRosterProvision(ctx, store.AgentRosterRow{
		ConversationID: row.ID, Handle: "handle-1", Alias: "agent", TabID: tabID,
		State: string(agent.RosterStateActive), Incarnation: "inc-1",
	}); err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func waitWatchHistoryDeliveries(t *testing.T, database *store.Store, ctx context.Context, conversationID string, count int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		rows, err := database.MsgList(ctx, conversationID)
		if err != nil {
			t.Fatal(err)
		}
		if deliveries := agent.WatchHistoryToolData(rows); len(deliveries) >= count {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("conversation %s did not receive %d watch deliveries", conversationID, count)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitWatchOutboxPending(t *testing.T, database *store.Store, ctx context.Context) []byte {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		rows, err := database.AgentWatchEventPendingList(ctx, 4)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 0 {
			return []byte(rows[0].DeliveryID)
		}
		if time.Now().After(deadline) {
			t.Fatal("watch event did not fire into the outbox")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// End to end over the real CLI boundary: a watch created through the real IPC
// on a real local tab fires on real shell output, is consumed by the agent
// runtime as durable untrusted tool data, and is emitted to the UI as the
// same envelope — with true absolute stream positions.
func TestProductionWatchEndToEndRealCLI(t *testing.T) {
	database, sessions, events := newWatchRealCLIFixture(t)
	_, dispatcher := newWatchTestService(t, database, sessions, events, 10*time.Millisecond)
	ctx := watchOwnerCtx("owner-a")
	tab := openWatchRealCLITab(t, ctx, sessions)
	conversationID := provisionWatchConversation(t, database, ctx, tab.ID)

	create := dispatchWatch(t, ctx, dispatcher, "ai_watch_create", `{"targetId":"`+tab.ID+`","name":"ready watch","condition":{"type":"contains","pattern":"nexterm-watch-ready"},"repeat":false,"expiresAt":0}`)
	if !create.OK {
		t.Fatalf("ai_watch_create = %+v", create.Error)
	}
	var summary WatchSummary
	if err := json.Unmarshal(create.Data, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Status != "armed" {
		t.Fatalf("created summary = %+v", summary)
	}
	if err := sessions.Write(ctx, tab.ID, "client-a", []byte("printf 'nexterm-watch-ready\\n'\r")); err != nil {
		t.Fatal(err)
	}

	waitWatchHistoryDeliveries(t, database, ctx, conversationID, 1)
	rows, err := database.MsgList(ctx, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	deliveries := agent.WatchHistoryToolData(rows)
	event := deliveries[0].Event
	if !deliveries[0].Untrusted || deliveries[0].Channel != "tool" {
		t.Fatalf("delivery envelope = %+v; want untrusted tool data", deliveries[0])
	}
	if event.WatchID != summary.ID || event.TargetID != tab.ID || !strings.Contains(event.Excerpt, "nexterm-watch-ready") {
		t.Fatalf("delivered event = %+v", event)
	}
	if event.MatchedFrom <= 0 || event.MatchedTo <= event.MatchedFrom {
		t.Fatalf("match positions = %d..%d; want true absolute stream positions", event.MatchedFrom, event.MatchedTo)
	}

	// The UI notification carries the same envelope, filtered by the owner.
	uiEvent := waitWatchToolDataEvent(t, events, event.DeliveryID)
	if uiEvent.OwnerID != "owner-a" || uiEvent.Data.Event.DeliveryID != event.DeliveryID {
		t.Fatalf("UI tool data event = %+v", uiEvent)
	}

	// The IPC list reflects the matched watch with its revision advanced.
	listed := dispatchWatch(t, ctx, dispatcher, "ai_watch_list", `{}`)
	if !listed.OK || !strings.Contains(string(listed.Data), `"status":"matched"`) {
		t.Fatalf("list after fire = %+v", listed)
	}
}

// A pending outbox event survives a service restart with its fixed
// DeliveryID: the next service's executor delivers exactly that event, and a
// later pass finds nothing new (the ack is durable).
func TestProductionWatchRestartResumesDeliveryWithFixedDeliveryID(t *testing.T) {
	database, sessions, events := newWatchRealCLIFixture(t)
	ctx := watchOwnerCtx("owner-a")
	tab := openWatchRealCLITab(t, ctx, sessions)
	conversationID := provisionWatchConversation(t, database, ctx, tab.ID)

	// The first service never dispatches (hourly tick), so the fired event
	// stays pending in the outbox across the shutdown.
	service1, dispatcher1 := newWatchTestService(t, database, sessions, events, time.Hour)
	create := dispatchWatch(t, ctx, dispatcher1, "ai_watch_create", `{"targetId":"`+tab.ID+`","condition":{"type":"contains","pattern":"nexterm-watch-restart"},"repeat":false,"expiresAt":0}`)
	if !create.OK {
		t.Fatalf("ai_watch_create = %+v", create.Error)
	}
	if err := sessions.Write(ctx, tab.ID, "client-a", []byte("printf 'nexterm-watch-restart\\n'\r")); err != nil {
		t.Fatal(err)
	}
	deliveryID := waitWatchOutboxPending(t, database, ctx)
	if err := service1.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The restarted service re-attaches the target from the durable rows and
	// delivers the pending event with the same fixed DeliveryID.
	service2, dispatcher2 := newWatchTestService(t, database, sessions, events, 10*time.Millisecond)
	if listed := dispatchWatch(t, ctx, dispatcher2, "ai_watch_list", `{}`); !listed.OK {
		t.Fatalf("ai_watch_list after restart = %+v", listed.Error)
	}
	waitWatchHistoryDeliveries(t, database, ctx, conversationID, 1)
	rows, err := database.MsgList(ctx, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	deliveries := agent.WatchHistoryToolData(rows)
	if len(deliveries) != 1 || deliveries[0].Event.DeliveryID != string(deliveryID) {
		t.Fatalf("deliveries after restart = %+v; want the fixed DeliveryID %s", deliveries, deliveryID)
	}
	uiEvent := waitWatchToolDataEvent(t, events, string(deliveryID))
	if uiEvent.Data.Event.DeliveryID != string(deliveryID) {
		t.Fatalf("UI delivery after restart = %+v; want the fixed DeliveryID", uiEvent)
	}

	// The ack is durable: a further dispatch pass delivers nothing new.
	executor, err := service2.executorFor(ctx, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if delivered, err := executor.DispatchOnce(ctx); err != nil || delivered != 0 {
		t.Fatalf("dispatch after ack = %d, %v; want 0, nil", delivered, err)
	}
	if rows, err := database.MsgList(ctx, conversationID); err != nil || len(agent.WatchHistoryToolData(rows)) != 1 {
		t.Fatalf("history deliveries after ack = %+v, %v; want exactly 1", rows, err)
	}
}
