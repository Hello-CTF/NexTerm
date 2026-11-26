package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/ai/watches"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// WatchToolName is the synthesized tool identity whose call/result pair
// carries a watch event through the conversation history. The event is data
// produced by the watch engine, never an instruction: it travels as an
// untrusted tool result, exactly like any real tool's output, and never as a
// user message.
const WatchToolName = "watch_event"

// watchDeliveryBound caps the consumer's in-memory redelivery index; the
// durable history scan remains the authoritative idempotency check.
const watchDeliveryBound = 4096

// WatchConsumer is the agent-runtime consumer of watch tool data. Delivery is
// at-least-once, so every hop is idempotent on the fixed DeliveryID: an
// in-memory index dedupes the common redelivery, and the persisted
// call/result pair dedupes the crash window between delivery and
// acknowledgement. Events reach the runtime through the roster: the
// conversations whose active roster entries bind the fired target receive
// the paired history rows, so the next job start replays the event inline as
// tool data — the same job-start injection discipline as roster context. A
// target with no roster binding has no agent consumer; delivery still
// succeeds so the outbox can settle (the UI surface receives it separately).
type WatchConsumer struct {
	store  ConversationStore
	roster *Roster
	// onFresh, when set, learns of each event this consumer newly wrote into
	// a conversation's history (never a redelivery), so it can wake that
	// conversation.
	onFresh func(ctx context.Context, conversationID string, event watches.Event)

	mu    sync.Mutex
	seen  map[string]struct{}
	order []string
}

// NewWatchConsumer builds the consumer over the runner's conversation store
// and roster. A nil store or roster disables runtime delivery: every event
// reports success with no consumer rather than inventing one.
func NewWatchConsumer(store ConversationStore, roster *Roster) *WatchConsumer {
	return &WatchConsumer{store: store, roster: roster, seen: map[string]struct{}{}}
}

// OnFresh registers the hook told about each newly persisted event. It must
// not block: delivery holds the consumer lock while it runs.
func (c *WatchConsumer) OnFresh(hook func(ctx context.Context, conversationID string, event watches.Event)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onFresh = hook
}

// watchCallID derives the synthesized tool call ID of a delivery: stable
// across redeliveries, so the persisted pair is naturally idempotent.
func watchCallID(deliveryID string) string {
	return "watch-" + deliveryID
}

// DeliverWatchToolData implements watches.Sink.
func (c *WatchConsumer) DeliverWatchToolData(ctx context.Context, data watches.ToolData) error {
	event := data.Event
	if event.Kind != watches.EventKind || event.DeliveryID == "" {
		return watches.ErrEventCorrupt
	}
	if c.store == nil || c.roster == nil && event.ConversationID == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.seen[event.DeliveryID]; ok {
		return nil
	}
	conversations, err := c.watchConversations(ctx, event)
	if err != nil {
		return err
	}
	for _, conversationID := range conversations {
		fresh, err := c.persistPair(ctx, conversationID, event, data)
		if err == nil && fresh && c.onFresh != nil {
			c.onFresh(ctx, conversationID, event)
		}
		if err != nil {
			// A conversation deleted after the roster listing is gone for
			// good: skipping it keeps the delivery honest instead of
			// poisoning the outbox with an unretryable recipient.
			var appErr *ipc.Error
			if errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound {
				continue
			}
			return err
		}
	}
	c.seen[event.DeliveryID] = struct{}{}
	c.order = append(c.order, event.DeliveryID)
	for len(c.order) > watchDeliveryBound {
		delete(c.seen, c.order[0])
		c.order = c.order[1:]
	}
	return nil
}

// watchConversations resolves who receives an event: the conversation that
// created the watch, or for a watch without one, the conversations whose
// active roster entries bind the target, owner-scoped through the roster
// store.
func (c *WatchConsumer) watchConversations(ctx context.Context, event watches.Event) ([]string, error) {
	if event.ConversationID != "" {
		return []string{event.ConversationID}, nil
	}
	if c.roster == nil {
		return nil, nil
	}
	targetID := event.TargetID
	entries, err := c.roster.List(ctx, "")
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var conversations []string
	for _, entry := range entries {
		if entry.TabID != targetID || entry.ConversationID == "" {
			continue
		}
		if RosterStateTerminal(entry.State) || entry.State == RosterStateProvisioning {
			continue
		}
		if _, ok := seen[entry.ConversationID]; ok {
			continue
		}
		seen[entry.ConversationID] = struct{}{}
		conversations = append(conversations, entry.ConversationID)
	}
	return conversations, nil
}

// persistPair writes the synthesized tool call and its untrusted result into
// the conversation history, skipping whichever half a previous delivery
// already persisted, so a partial crash never duplicates and never orphans.
func (c *WatchConsumer) persistPair(ctx context.Context, conversationID string, event watches.Event, data watches.ToolData) (bool, error) {
	callID := watchCallID(event.DeliveryID)
	haveCall, haveResult, err := c.existingPair(ctx, conversationID, callID)
	if err != nil {
		return false, err
	}
	if !haveCall {
		args, err := json.Marshal(map[string]any{
			"deliveryId": event.DeliveryID, "watchId": event.WatchID, "targetId": event.TargetID,
		})
		if err != nil {
			return false, err
		}
		row := map[string]any{
			"type": historyTypeToolCall, "jobId": "", "id": callID,
			"name": WatchToolName, "args": json.RawMessage(args),
		}
		if err := c.store.MsgInsert(ctx, conversationID, historyTypeToolCall, row, nil, nil); err != nil {
			return false, err
		}
	}
	if !haveResult {
		payload, err := json.Marshal(data)
		if err != nil {
			return false, err
		}
		text := string(payload)
		row := map[string]any{
			"type": historyTypeToolResult, "jobId": "", "id": callID, "tool": WatchToolName,
			"ok": true, "text": text, "summary": summarize(text),
		}
		if err := c.store.MsgInsert(ctx, conversationID, historyTypeToolResult, row, nil, nil); err != nil {
			return false, err
		}
	}
	return !haveResult, nil
}

// existingPair reports whether the conversation history already carries the
// delivery's synthesized call or result row.
func (c *WatchConsumer) existingPair(ctx context.Context, conversationID, callID string) (haveCall, haveResult bool, err error) {
	rows, err := c.store.MsgList(ctx, conversationID)
	if err != nil {
		return false, false, err
	}
	for _, row := range rows {
		var persisted struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if json.Unmarshal([]byte(row.ContentJSON), &persisted) != nil {
			continue
		}
		if persisted.ID != callID {
			continue
		}
		switch persisted.Type {
		case historyTypeToolCall:
			haveCall = true
		case historyTypeToolResult:
			haveResult = true
		}
	}
	return haveCall, haveResult, nil
}

// WatchHistoryToolData extracts the watch tool-data deliveries replayed into
// a job's message history: the untrusted payloads exactly as the model will
// read them. Tests and the IPC surface use it to prove the injection stays
// tool data.
func WatchHistoryToolData(rows []store.MessageRow) []watches.ToolData {
	var out []watches.ToolData
	for _, row := range rows {
		var persisted historyRow
		if json.Unmarshal([]byte(row.ContentJSON), &persisted) != nil {
			continue
		}
		if persisted.Type != historyTypeToolResult || persisted.Tool != WatchToolName {
			continue
		}
		if !strings.HasPrefix(persisted.ID, "watch-") {
			continue
		}
		var data watches.ToolData
		if json.Unmarshal([]byte(persisted.Text), &data) != nil {
			continue
		}
		out = append(out, data)
	}
	return out
}
