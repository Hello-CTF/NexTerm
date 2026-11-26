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

func watchTestEvent(conversationID string) watches.Event {
	return watches.Event{
		Kind: watches.EventKind, DeliveryID: "delivery-1", RunID: "run-1",
		WatchID: "watch-1", TargetID: "tab-1", Identity: "identity-1", ConversationID: conversationID,
		Condition:   watches.Condition{Type: watches.ConditionContains, Pattern: "ready"},
		MatchedFrom: 10, MatchedTo: 15, Excerpt: "xxready", FiredAt: 1000,
	}
}

// A fired watch reaches the conversation that created it, and only that one,
// and it replays into the next job strictly as tool data: a paired assistant
// tool call and tool result, never a user message.
func TestWatchConsumerDeliversToolDataToCreatingConversation(t *testing.T) {
	backend := openWatchConsumerStore(t)
	ctx := watchConsumerCtx("owner-a")
	conv := createWatchConversation(t, backend, ctx)
	other := createWatchConversation(t, backend, ctx)

	consumer := NewWatchConsumer(backend)
	event := watchTestEvent(conv)
	if err := consumer.DeliverWatchToolData(ctx, watches.NewToolData(event)); err != nil {
		t.Fatal(err)
	}

	for _, conversationID := range []string{conv} {
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
	rows, err := backend.MsgList(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if deliveries := WatchHistoryToolData(rows); len(deliveries) != 0 {
		t.Fatalf("a conversation that did not create the watch received it: %+v", deliveries)
	}
}

// Redelivery of the same DeliveryID is idempotent, within the process and
// across a consumer restart (the durable pair scan is the authoritative
// dedupe).
func TestWatchConsumerDedupesRedelivery(t *testing.T) {
	backend := openWatchConsumerStore(t)
	ctx := watchConsumerCtx("owner-a")
	conv := createWatchConversation(t, backend, ctx)
	event := watchTestEvent(conv)

	consumer := NewWatchConsumer(backend)
	for i := 0; i < 3; i++ {
		if err := consumer.DeliverWatchToolData(ctx, watches.NewToolData(event)); err != nil {
			t.Fatal(err)
		}
	}
	// A fresh consumer over the same store has an empty in-memory index; the
	// durable pair scan must dedupe instead.
	restarted := NewWatchConsumer(backend)
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
	event := watchTestEvent(conv)
	if err := backend.MsgInsert(ctx, conv, historyTypeToolCall, map[string]any{
		"type": historyTypeToolCall, "jobId": "", "id": "watch-delivery-1",
		"name": WatchToolName, "args": json.RawMessage(`{"deliveryId":"delivery-1"}`),
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	consumer := NewWatchConsumer(backend)
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

// A watch with no conversation (one the user created from the watch panel),
// a conversation deleted after the watch was created, and a consumer without
// a store are all acknowledged without writing to any conversation; a
// malformed envelope is rejected so the outbox keeps it visible.
func TestWatchConsumerWithoutConversation(t *testing.T) {
	backend := openWatchConsumerStore(t)
	ctx := watchConsumerCtx("owner-a")
	bystander := createWatchConversation(t, backend, ctx)
	consumer := NewWatchConsumer(backend)
	if err := consumer.DeliverWatchToolData(ctx, watches.NewToolData(watchTestEvent(""))); err != nil {
		t.Fatal(err)
	}
	deleted := createWatchConversation(t, backend, ctx)
	if err := backend.ConvDelete(ctx, deleted); err != nil {
		t.Fatal(err)
	}
	gone := watchTestEvent(deleted)
	gone.DeliveryID = "delivery-deleted"
	if err := consumer.DeliverWatchToolData(ctx, watches.NewToolData(gone)); err != nil {
		t.Fatalf("delivery to a deleted conversation = %v; want it settled", err)
	}
	rows, err := backend.MsgList(ctx, bystander)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("an unaddressed event wrote history: %+v", rows)
	}
	if err := NewWatchConsumer(nil).DeliverWatchToolData(ctx, watches.NewToolData(watchTestEvent(bystander))); err != nil {
		t.Fatal(err)
	}
	bad := watches.NewToolData(watchTestEvent(bystander))
	bad.Event.DeliveryID = ""
	if err := consumer.DeliverWatchToolData(ctx, bad); err == nil {
		t.Fatal("a delivery without an ID must be rejected")
	}
}

// Delivery is owner-scoped: an event carrying another owner's conversation
// never reaches that conversation.
func TestWatchConsumerIsOwnerIsolated(t *testing.T) {
	backend := openWatchConsumerStore(t)
	ctxA := watchConsumerCtx("owner-a")
	ctxB := watchConsumerCtx("owner-b")
	convB := createWatchConversation(t, backend, ctxB)

	consumer := NewWatchConsumer(backend)
	if err := consumer.DeliverWatchToolData(ctxA, watches.NewToolData(watchTestEvent(convB))); err != nil {
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

// The fresh hook hears of a conversation's watch event exactly once, never
// for a redelivery.
func TestWatchConsumerWakesTheCreatingConversationOnce(t *testing.T) {
	backend := openWatchConsumerStore(t)
	ctx := watchConsumerCtx("owner-a")
	conv := createWatchConversation(t, backend, ctx)
	event := watchTestEvent(conv)

	var fresh []string
	consumer := NewWatchConsumer(backend)
	consumer.OnFresh(func(_ context.Context, conversationID string, got watches.Event) {
		fresh = append(fresh, conversationID+"/"+got.DeliveryID)
	})
	for i := 0; i < 2; i++ {
		if err := consumer.DeliverWatchToolData(ctx, watches.NewToolData(event)); err != nil {
			t.Fatal(err)
		}
	}
	restarted := NewWatchConsumer(backend)
	restarted.OnFresh(func(_ context.Context, conversationID string, got watches.Event) {
		fresh = append(fresh, "restarted:"+conversationID)
	})
	if err := restarted.DeliverWatchToolData(ctx, watches.NewToolData(event)); err != nil {
		t.Fatal(err)
	}
	rows, err := backend.MsgList(ctx, conv)
	if err != nil {
		t.Fatal(err)
	}
	if deliveries := WatchHistoryToolData(rows); len(deliveries) != 1 || deliveries[0].Event.ConversationID != conv {
		t.Fatalf("deliveries = %+v", deliveries)
	}
	if len(fresh) != 1 || fresh[0] != conv+"/delivery-1" {
		t.Fatalf("fresh hook calls = %v; want one for the first delivery", fresh)
	}
}
