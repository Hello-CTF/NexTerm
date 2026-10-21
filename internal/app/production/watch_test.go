package production

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/watches"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type watchEventCapture struct {
	mu     sync.Mutex
	events []ipc.Event
}

func (c *watchEventCapture) Emit(_ context.Context, event ipc.Event) error {
	c.mu.Lock()
	c.events = append(c.events, event)
	c.mu.Unlock()
	return nil
}

func (c *watchEventCapture) toolDataEvents() []watches.ToolDataEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []watches.ToolDataEvent
	for _, event := range c.events {
		if event.Event != ipc.TopicAIWatchToolData {
			continue
		}
		if payload, ok := event.Payload.(watches.ToolDataEvent); ok {
			out = append(out, payload)
		}
	}
	return out
}

// waitWatchToolDataEvent waits for the UI notification of one delivery: the
// sink emits after the durable consumer accepts, so the notification trails
// the history row and must be polled for.
func waitWatchToolDataEvent(t *testing.T, events *watchEventCapture, deliveryID string) watches.ToolDataEvent {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		for _, event := range events.toolDataEvents() {
			if event.Data.Event.DeliveryID == deliveryID {
				return event
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no UI tool data event for delivery %s", deliveryID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newWatchTestFixture(t *testing.T) (*store.Store, *session.Manager, *watchEventCapture) {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager(session.Config{IdleTimeout: -1})
	t.Cleanup(func() {
		_ = sessions.Close()
		_ = database.Close()
	})
	return database, sessions, &watchEventCapture{}
}

func newWatchTestService(t *testing.T, database *store.Store, sessions *session.Manager, events ipc.Emitter, dispatchInterval time.Duration) (*watchService, *ipc.Dispatcher) {
	t.Helper()
	service := newWatchService(database, sessions, events, agent.NewRoster(database, nil), nil)
	service.dispatchInterval = dispatchInterval
	service.pollInterval = 5 * time.Millisecond
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	dispatcher := ipc.NewDispatcher()
	if err := service.registerCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	return service, dispatcher
}

func watchOwnerCtx(owner string) context.Context {
	return ipc.WithUserID(context.Background(), owner)
}

func dispatchWatch(t *testing.T, ctx context.Context, dispatcher *ipc.Dispatcher, command, args string) ipc.Response {
	t.Helper()
	return dispatcher.Dispatch(ctx, ipc.Request{Command: command, Args: json.RawMessage(args)}, ipc.Environment{})
}

func registerWatchTargetTab(t *testing.T, sessions *session.Manager, owner string) session.TabInfo {
	t.Helper()
	tab, err := sessions.RegisterRemoteSession(context.Background(), session.RemoteSessionOptions{OwnerID: owner, AssetID: "asset-1", Title: "watched"})
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

// Owner isolation and cancellation go through the real IPC surface against
// the real store: another owner's tabs, watches and revisions are never
// visible, and cancellation is CAS-guarded by the durable revision.
func TestProductionWatchOwnerIsolationAndCancel(t *testing.T) {
	database, sessions, events := newWatchTestFixture(t)
	_, dispatcher := newWatchTestService(t, database, sessions, events, time.Hour)
	ctxA := watchOwnerCtx("owner-a")
	ctxB := watchOwnerCtx("owner-b")
	tab := registerWatchTargetTab(t, sessions, "owner-a")

	create := dispatchWatch(t, ctxA, dispatcher, "ai_watch_create", `{"targetId":"`+tab.ID+`","name":"ready watch","condition":{"type":"contains","pattern":"ready"},"repeat":false,"expiresAt":0}`)
	if !create.OK {
		t.Fatalf("ai_watch_create = %+v", create.Error)
	}
	var summary WatchSummary
	if err := json.Unmarshal(create.Data, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Status != "armed" || summary.Revision == 0 || summary.TargetID != tab.ID {
		t.Fatalf("created summary = %+v", summary)
	}

	// The targets list carries the live episode evidence.
	targets := dispatchWatch(t, ctxA, dispatcher, "ai_watch_targets", `{}`)
	if !targets.OK {
		t.Fatalf("ai_watch_targets = %+v", targets.Error)
	}
	var targetSummaries []WatchTargetSummary
	if err := json.Unmarshal(targets.Data, &targetSummaries); err != nil {
		t.Fatal(err)
	}
	if len(targetSummaries) != 1 || targetSummaries[0].State != "attached" || targetSummaries[0].Evidence == "" {
		t.Fatalf("targets = %+v", targetSummaries)
	}

	// Another owner sees nothing and cannot touch the watch.
	if createB := dispatchWatch(t, ctxB, dispatcher, "ai_watch_create", `{"targetId":"`+tab.ID+`","condition":{"type":"contains","pattern":"ready"},"repeat":false,"expiresAt":0}`); createB.OK || createB.Error.Code != ipc.CodeNotFound {
		t.Fatalf("owner-b create on owner-a tab = %+v; want not_found", createB.Error)
	}
	if listB := dispatchWatch(t, ctxB, dispatcher, "ai_watch_list", `{}`); !listB.OK || string(listB.Data) != `[]` {
		t.Fatalf("owner-b list = %+v; want empty", listB)
	}
	cancelForeign := dispatchWatch(t, ctxB, dispatcher, "ai_watch_cancel", `{"watchId":"`+summary.ID+`","expectedRevision":1}`)
	if cancelForeign.OK || cancelForeign.Error.Code != ipc.CodeNotFound {
		t.Fatalf("owner-b cancel = %+v; want not_found", cancelForeign.Error)
	}

	// Cancellation is guarded by the durable revision, never the generation.
	stale := dispatchWatch(t, ctxA, dispatcher, "ai_watch_cancel", `{"watchId":"`+summary.ID+`","expectedRevision":999}`)
	if stale.OK || stale.Error.Code != ipc.Code("conflict") {
		t.Fatalf("cancel with stale revision = %+v; want conflict", stale.Error)
	}
	cancelled := dispatchWatch(t, ctxA, dispatcher, "ai_watch_cancel", `{"watchId":"`+summary.ID+`","expectedRevision":`+jsonNumber(summary.Revision)+`}`)
	if !cancelled.OK {
		t.Fatalf("ai_watch_cancel = %+v", cancelled.Error)
	}
	var cancelledSummary WatchSummary
	if err := json.Unmarshal(cancelled.Data, &cancelledSummary); err != nil {
		t.Fatal(err)
	}
	if cancelledSummary.Status != "cancelled" {
		t.Fatalf("cancelled summary = %+v", cancelledSummary)
	}
	listed := dispatchWatch(t, ctxA, dispatcher, "ai_watch_list", `{}`)
	if !listed.OK || !strings.Contains(string(listed.Data), `"status":"cancelled"`) {
		t.Fatalf("list after cancel = %+v", listed)
	}
}

func jsonNumber(value uint64) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// A closed tab is honestly disconnected, never reported gone: its watches
// stay armed because a durable tab can return with the same incarnation.
func TestProductionWatchMissingTabIsDisconnectedNotGone(t *testing.T) {
	database, sessions, events := newWatchTestFixture(t)
	service, dispatcher := newWatchTestService(t, database, sessions, events, time.Hour)
	ctxA := watchOwnerCtx("owner-a")
	tab := registerWatchTargetTab(t, sessions, "owner-a")
	if create := dispatchWatch(t, ctxA, dispatcher, "ai_watch_create", `{"targetId":"`+tab.ID+`","condition":{"type":"contains","pattern":"ready"},"repeat":false,"expiresAt":0}`); !create.OK {
		t.Fatalf("ai_watch_create = %+v", create.Error)
	}
	if err := sessions.CloseTabFor(ctxA, tab.ID); err != nil {
		t.Fatal(err)
	}
	service.reconcileOwner("owner-a")
	targets := dispatchWatch(t, ctxA, dispatcher, "ai_watch_targets", `{}`)
	if !targets.OK {
		t.Fatalf("ai_watch_targets = %+v", targets.Error)
	}
	var summaries []WatchTargetSummary
	if err := json.Unmarshal(targets.Data, &summaries); err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].State != "disconnected" {
		t.Fatalf("closed tab target = %+v; want disconnected, never gone", summaries)
	}
	rows, err := database.AgentWatchListByOwner(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != store.AgentWatchStatusArmed {
		t.Fatalf("watch of a closed tab must stay armed: %+v", rows)
	}
}
