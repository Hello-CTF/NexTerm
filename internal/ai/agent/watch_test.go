package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/watches"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/schema"
)

func watchConsumerCtx(owner string) context.Context {
	return ipc.WithUserID(context.Background(), owner)
}

func openWatchConsumerStore(t *testing.T) *store.Store {
	t.Helper()
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

func createWatchConversation(t *testing.T, backend *store.Store, ctx context.Context) string {
	t.Helper()
	row, err := backend.ConvCreate(ctx, "watch conv", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	return row.ID
}

func provisionWatchRoster(t *testing.T, backend *store.Store, ctx context.Context, conversationID, handle, tabID, state string) {
	t.Helper()
	_, err := backend.AgentRosterProvision(ctx, store.AgentRosterRow{
		ConversationID: conversationID, Handle: handle, Alias: handle, TabID: tabID,
		State: state, Incarnation: "inc-" + handle,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func watchTestEvent(targetID string) watches.Event {
	return watches.Event{
		Kind: watches.EventKind, DeliveryID: "delivery-1", RunID: "run-1",
		WatchID: "watch-1", TargetID: targetID, Identity: "identity-1",
		Condition:   watches.Condition{Type: watches.ConditionContains, Pattern: "ready"},
		MatchedFrom: 10, MatchedTo: 15, Excerpt: "xxready", FiredAt: 1000,
	}
}

// A fired watch reaches every conversation whose active roster entry binds
// the target, and it replays into the next job strictly as tool data: a
// paired assistant tool call and tool result, never a user message.
func TestWatchConsumerDeliversToolDataToBoundConversations(t *testing.T) {
	backend := openWatchConsumerStore(t)
	ctx := watchConsumerCtx("owner-a")
	conv1 := createWatchConversation(t, backend, ctx)
	conv2 := createWatchConversation(t, backend, ctx)
	convReleased := createWatchConversation(t, backend, ctx)
	convOtherTab := createWatchConversation(t, backend, ctx)
	provisionWatchRoster(t, backend, ctx, conv1, "handle-1", "tab-1", string(RosterStateActive))
	provisionWatchRoster(t, backend, ctx, conv2, "handle-2", "tab-1", string(RosterStateActive))
	provisionWatchRoster(t, backend, ctx, convReleased, "handle-3", "tab-1", string(RosterStateReleased))
	provisionWatchRoster(t, backend, ctx, convOtherTab, "handle-4", "tab-2", string(RosterStateActive))

	roster := NewRoster(backend, nil)
	consumer := NewWatchConsumer(backend, roster)
	event := watchTestEvent("tab-1")
	if err := consumer.DeliverWatchToolData(ctx, watches.NewToolData(event)); err != nil {
		t.Fatal(err)
	}

	for _, conversationID := range []string{conv1, conv2} {
		rows, err := backend.MsgList(ctx, conversationID)
		if err != nil {
			t.Fatal(err)
		}
		deliveries := WatchHistoryToolData(rows)
		if len(deliveries) != 1 || deliveries[0].Event.DeliveryID != "delivery-1" {
			t.Fatalf("conversation %s deliveries = %+v", conversationID, deliveries)
		}
		if !deliveries[0].Untrusted || deliveries[0].Channel != "tool" {
			t.Fatalf("delivery envelope must stay untrusted tool data: %+v", deliveries[0])
		}
		// The job-start replay pairs the synthesized call with its result and
		// keeps the payload out of every user message.
		messages := historyMessages(rows, "job-new")
		toolSeen := false
		for _, message := range messages {
			if message.Role == schema.User && strings.Contains(message.Content, "delivery-1") {
				t.Fatalf("watch event leaked into a user message: %q", message.Content)
			}
			if message.Role == schema.Tool && message.ToolCallID == "watch-delivery-1" {
				toolSeen = true
				// The model reads the standard tools.Output envelope whose
				// text is the untrusted watch tool-data payload.
				var envelope struct {
					OK   bool   `json:"ok"`
					Text string `json:"text"`
				}
				if err := json.Unmarshal([]byte(message.Content), &envelope); err != nil || !envelope.OK {
					t.Fatalf("tool message is not the tools.Output envelope: %q", message.Content)
				}
				var payload watches.ToolData
				if err := json.Unmarshal([]byte(envelope.Text), &payload); err != nil {
					t.Fatalf("envelope text is not the watch tool data: %q", envelope.Text)
				}
				if !payload.Untrusted || payload.Channel != "tool" || payload.Event.DeliveryID != "delivery-1" {
					t.Fatalf("replayed payload = %+v", payload)
				}
			}
			if message.Role == schema.Assistant {
				for _, call := range message.ToolCalls {
					if call.ID == "watch-delivery-1" && call.Function.Name != WatchToolName {
						t.Fatalf("synthesized call name = %q", call.Function.Name)
					}
				}
			}
		}
		if !toolSeen {
			t.Fatalf("conversation %s replay has no paired tool message", conversationID)
		}
	}
	for _, conversationID := range []string{convReleased, convOtherTab} {
		rows, err := backend.MsgList(ctx, conversationID)
		if err != nil {
			t.Fatal(err)
		}
		if deliveries := WatchHistoryToolData(rows); len(deliveries) != 0 {
			t.Fatalf("conversation %s must not receive the event: %+v", conversationID, deliveries)
		}
	}
}

// Redelivery of the same DeliveryID is idempotent, within the process and
// across a consumer restart (the durable pair scan is the authoritative
// dedupe).
func TestWatchConsumerDedupesRedelivery(t *testing.T) {
	backend := openWatchConsumerStore(t)
	ctx := watchConsumerCtx("owner-a")
	conv := createWatchConversation(t, backend, ctx)
	provisionWatchRoster(t, backend, ctx, conv, "handle-1", "tab-1", string(RosterStateActive))
	roster := NewRoster(backend, nil)
	event := watchTestEvent("tab-1")

	consumer := NewWatchConsumer(backend, roster)
	for i := 0; i < 3; i++ {
		if err := consumer.DeliverWatchToolData(ctx, watches.NewToolData(event)); err != nil {
			t.Fatal(err)
		}
	}
	// A fresh consumer over the same store has an empty in-memory index; the
	// durable pair scan must dedupe instead.
	restarted := NewWatchConsumer(backend, roster)
	if err := restarted.DeliverWatchToolData(ctx, watches.NewToolData(event)); err != nil {
		t.Fatal(err)
	}
	rows, err := backend.MsgList(ctx, conv)
	if err != nil {
		t.Fatal(err)
	}
	if deliveries := WatchHistoryToolData(rows); len(deliveries) != 1 {
		t.Fatalf("deliveries after redeliveries = %d; want 1", len(deliveries))
	}
	callCount := 0
	for _, row := range rows {
		if strings.Contains(row.ContentJSON, "watch-delivery-1") {
			callCount++
		}
	}
	if callCount != 2 {
		t.Fatalf("history rows carrying the call ID = %d; want exactly the call/result pair", callCount)
	}
}

// A crash between the call insert and the result insert leaves half the
// pair: the redelivery completes exactly the missing half.
func TestWatchConsumerCompletesAPartialPair(t *testing.T) {
	backend := openWatchConsumerStore(t)
	ctx := watchConsumerCtx("owner-a")
	conv := createWatchConversation(t, backend, ctx)
	provisionWatchRoster(t, backend, ctx, conv, "handle-1", "tab-1", string(RosterStateActive))
	event := watchTestEvent("tab-1")
	if err := backend.MsgInsert(ctx, conv, historyTypeToolCall, map[string]any{
		"type": historyTypeToolCall, "jobId": "", "id": "watch-delivery-1",
		"name": WatchToolName, "args": json.RawMessage(`{"deliveryId":"delivery-1"}`),
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	consumer := NewWatchConsumer(backend, NewRoster(backend, nil))
	if err := consumer.DeliverWatchToolData(ctx, watches.NewToolData(event)); err != nil {
		t.Fatal(err)
	}
	rows, err := backend.MsgList(ctx, conv)
	if err != nil {
		t.Fatal(err)
	}
	if deliveries := WatchHistoryToolData(rows); len(deliveries) != 1 {
		t.Fatalf("deliveries = %+v; want the completed pair", deliveries)
	}
	messages := historyMessages(rows, "job-new")
	toolSeen := false
	for _, message := range messages {
		if message.Role == schema.Tool && message.ToolCallID == "watch-delivery-1" {
			toolSeen = true
		}
	}
	if !toolSeen {
		t.Fatal("the completed pair does not replay as a tool message")
	}
}

// Events for a target with no roster binding — and runners without a roster
// — are acknowledged without an invented consumer; a malformed envelope is
// rejected so the outbox keeps it visible.
func TestWatchConsumerWithoutBindingOrRoster(t *testing.T) {
	backend := openWatchConsumerStore(t)
	ctx := watchConsumerCtx("owner-a")
	consumer := NewWatchConsumer(backend, NewRoster(backend, nil))
	if err := consumer.DeliverWatchToolData(ctx, watches.NewToolData(watchTestEvent("tab-unbound"))); err != nil {
		t.Fatal(err)
	}
	if err := NewWatchConsumer(nil, nil).DeliverWatchToolData(ctx, watches.NewToolData(watchTestEvent("tab-1"))); err != nil {
		t.Fatal(err)
	}
	bad := watches.NewToolData(watchTestEvent("tab-1"))
	bad.Event.DeliveryID = ""
	if err := consumer.DeliverWatchToolData(ctx, bad); err == nil {
		t.Fatal("a delivery without an ID must be rejected")
	}
}

// The consumer's roster listing is owner-scoped: another owner's binding
// never receives the event.
func TestWatchConsumerIsOwnerIsolated(t *testing.T) {
	backend := openWatchConsumerStore(t)
	ctxA := watchConsumerCtx("owner-a")
	ctxB := watchConsumerCtx("owner-b")
	convB := createWatchConversation(t, backend, ctxB)
	provisionWatchRoster(t, backend, ctxB, convB, "handle-b", "tab-1", string(RosterStateActive))

	consumer := NewWatchConsumer(backend, NewRoster(backend, nil))
	if err := consumer.DeliverWatchToolData(ctxA, watches.NewToolData(watchTestEvent("tab-1"))); err != nil {
		t.Fatal(err)
	}
	rows, err := backend.MsgList(ctxB, convB)
	if err != nil {
		t.Fatal(err)
	}
	if deliveries := WatchHistoryToolData(rows); len(deliveries) != 0 {
		t.Fatalf("owner-b conversation received owner-a's event: %+v", deliveries)
	}
}
