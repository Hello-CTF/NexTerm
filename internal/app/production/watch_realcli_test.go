//go:build darwin || linux

package production

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
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

// The r2 live-replacement regression on the real CLI boundary: the tab is
// closed and recreated under the same target ID while the old runner is still
// alive (its poll is parked for the whole close/recreate window). The
// reconcile must retire the live stale runner and attach the new stream in
// the same executor: the old watch settles honestly, the target is never
// recorded gone, and a new watch fires on the recreated tab's output.
func TestProductionWatchLiveStaleRunnerReplacedOnRecreate(t *testing.T) {
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	events := &watchEventCapture{}
	attached := make(chan string, 4)
	var service *watchService
	sessionsWithHook := session.NewManager(session.Config{
		IdleTimeout: -1,
		Connector: session.ConnectorFunc(func(context.Context, session.Asset, uint64) (base.Transport, error) {
			return local.NewWithConfig(local.Config{Shell: "/bin/sh"}), nil
		}),
		Emitter: session.EmitterFunc(func(ctx context.Context, event session.Event) error {
			if service != nil {
				service.onSessionEvent(ctx, event)
			}
			return nil
		}),
	})
	t.Cleanup(func() { _ = sessionsWithHook.Close() })
	service, _ = newWatchTestService(t, database, sessionsWithHook, events, 10*time.Millisecond)
	// Park the runner's poll far beyond the close/recreate window so the
	// stale runner is provably alive when the recovery reconcile arrives.
	service.pollInterval = 30 * time.Second
	service.attachHook = func(targetID string) { attached <- targetID }
	ctx := watchOwnerCtx("owner-a")

	connected, err := sessionsWithHook.Connect(ctx, session.Asset{ID: "asset-local", Kind: session.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	tab, err := sessionsWithHook.OpenTabFor(ctx, session.OpenTabOptions{
		TabID: "watch-live-target", SessionID: connected.ID, ClientID: "client-a", ChannelID: "a-1", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	conversationID := provisionWatchConversation(t, database, ctx, tab.ID)
	dispatcher := ipc.NewDispatcher()
	if err := service.registerCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	create := dispatchWatch(t, ctx, dispatcher, "ai_watch_create", `{"targetId":"`+tab.ID+`","condition":{"type":"contains","pattern":"nexterm-live-one"},"repeat":false,"expiresAt":0}`)
	if !create.OK {
		t.Fatalf("ai_watch_create = %+v", create.Error)
	}
	var summary WatchSummary
	if err := json.Unmarshal(create.Data, &summary); err != nil {
		t.Fatal(err)
	}
	drainAttachHook(attached, tab.ID)
	executor, err := service.executorFor(ctx, "owner-a")
	if err != nil {
		t.Fatal(err)
	}

	// Close and recreate the tab under the same target ID while the old
	// runner is parked: the identity advances with the recreated ring.
	before, ok := sessionsWithHook.WatchStreamState(ctx, tab.ID)
	if !ok || before.WatchIdentity() == "" {
		t.Fatal("watch state before recreate failed")
	}
	if err := sessionsWithHook.CloseTabFor(ctx, tab.ID); err != nil {
		t.Fatal(err)
	}
	if !executor.RunnerAlive(tab.ID) {
		t.Fatal("test premise failed: the stale runner died before the recreate")
	}
	recreated, err := sessionsWithHook.OpenTabFor(ctx, session.OpenTabOptions{
		TabID: tab.ID, SessionID: connected.ID, ClientID: "client-a", ChannelID: "a-2", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	after, ok := sessionsWithHook.WatchStreamState(ctx, recreated.ID)
	if !ok || after.WatchIdentity() == "" {
		t.Fatal("watch state after recreate failed")
	}
	if after.WatchIdentity() == before.WatchIdentity() {
		t.Fatal("a recreated ring must advance the watch identity")
	}

	select {
	case targetID := <-attached:
		if targetID != tab.ID {
			t.Fatalf("reconcile attached %s; want %s", targetID, tab.ID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the recovery event never replaced the live stale runner")
	}
	// The old runner was retired for the replacement and the old watch
	// settled honestly against the advanced identity.
	if !executor.RunnerAlive(tab.ID) {
		t.Fatal("no live runner consumes the recreated stream after the replacement")
	}
	row, err := database.AgentWatchGet(ctx, summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.AgentWatchStatusUnverifiable {
		t.Fatalf("old watch after live replacement = %s; want unverifiable", row.Status)
	}
	targetRow, err := database.AgentWatchTargetGet(ctx, tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if targetRow.Gone {
		t.Fatal("a closed-then-recreated tab must never be recorded gone")
	}

	// A new watch on the recreated stream fires on its output. The runner that
	// proved the replacement still polls on the slow test interval, so swap it
	// for a fast one: a reversible detach keeps the watches armed and the next
	// attach opens a fresh source with the restored interval.
	service.pollInterval = 5 * time.Millisecond
	if err := executor.DetachTarget(ctx, tab.ID, false); err != nil {
		t.Fatal(err)
	}
	create2 := dispatchWatch(t, ctx, dispatcher, "ai_watch_create", `{"targetId":"`+tab.ID+`","condition":{"type":"contains","pattern":"nexterm-live-two"},"repeat":false,"expiresAt":0}`)
	if !create2.OK {
		t.Fatalf("ai_watch_create after recreate = %+v", create2.Error)
	}
	if err := sessionsWithHook.Write(ctx, recreated.ID, "client-a", []byte("printf 'nexterm-live-two\\n'\r")); err != nil {
		t.Fatal(err)
	}
	waitWatchHistoryDeliveries(t, database, ctx, conversationID, 1)
	rows, err := database.MsgList(ctx, conversationID)
	if err != nil {
		t.Fatal(err)
	}
	deliveries := agent.WatchHistoryToolData(rows)
	if deliveries[0].Event.TargetID != tab.ID || deliveries[0].Event.Identity != after.WatchIdentity() {
		t.Fatalf("delivered event after live replacement = %+v; want the recreated identity", deliveries[0].Event)
	}
}
