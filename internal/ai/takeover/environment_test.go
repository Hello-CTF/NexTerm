package takeover

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

func takeoverEnvironmentGrants(t *testing.T, resolver guard.EnvironmentResolver) *guard.Grants {
	t.Helper()
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	grants, err := guard.NewGrants(ipc.WithUserID(context.Background(), "owner-a"), database, nil,
		guard.WithShared(true), guard.WithEnvironmentResolver(resolver))
	if err != nil {
		t.Fatal(err)
	}
	return grants
}

// TestSendKeysProductionNeverAutoAllowedFromSessionMemory covers the takeover
// write path: after one confirmed send_keys, the session memory covers the
// kind, but a production-classified target still asks for every write — the
// classification is resolved from the real asset through the owner's
// configured list, never from client claims.
func TestSendKeysProductionNeverAutoAllowedFromSessionMemory(t *testing.T) {
	grants := takeoverEnvironmentGrants(t, func(_ context.Context, assetID string) (string, error) {
		if assetID == "asset-prod" {
			return "production", nil
		}
		return "staging", nil
	})
	chat := sequence(
		toolCallMessage(namedToolCall("keys-1", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)),
		toolCallMessage(namedToolCall("keys-2", "send_keys", `{"keys":"touch /tmp/y","enter":true}`)),
		toolCallMessage(doneCall("finished")),
	)
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Grants = grants
		deps.TabAsset = func(context.Context, string) (string, error) { return "asset-prod", nil }
	})
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})

	// The first write asks; allowing it for the session covers the kind in
	// the session memory.
	first := waitEvent(t, stream, "confirmRequired")
	if err := h.manager.Confirm(agent.Confirmation{JobID: response.JobID, CallID: first.ID, Nonce: first.Nonce, Decision: "allow_session"}); err != nil {
		t.Fatal(err)
	}
	// The second write must ask again: production targets are never
	// auto-allowed from session memory.
	second := waitEventUnlike(t, stream, "confirmRequired", first.ID)
	if err := h.manager.Confirm(agent.Confirmation{JobID: response.JobID, CallID: second.ID, Nonce: second.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := counts(events); done != 1 || failed != 0 {
		t.Fatalf("events = %+v", events)
	}
	confirmations := 0
	for _, event := range events {
		if event.Type == "confirmRequired" {
			confirmations++
		}
	}
	if confirmations != 2 {
		t.Fatalf("confirmations = %d, want 2", confirmations)
	}
	h.mu.Lock()
	writes := len(h.aiWrites)
	h.mu.Unlock()
	if writes != 2 {
		t.Fatalf("writes = %d, want 2", writes)
	}
}

// TestSendKeysNonProductionKeepsSessionMemoryAutoAllow pins the contrast: on
// a non-production target the remembered kind still auto-allows the second
// write, so the production gate is the classification, not the memory.
func TestSendKeysNonProductionKeepsSessionMemoryAutoAllow(t *testing.T) {
	grants := takeoverEnvironmentGrants(t, func(_ context.Context, assetID string) (string, error) {
		return "staging", nil
	})
	chat := sequence(
		toolCallMessage(namedToolCall("keys-1", "send_keys", `{"keys":"touch /tmp/x","enter":true}`)),
		toolCallMessage(namedToolCall("keys-2", "send_keys", `{"keys":"touch /tmp/y","enter":true}`)),
		toolCallMessage(doneCall("finished")),
	)
	h := newHarnessWith(t, chat, func(deps *Dependencies) {
		deps.Grants = grants
		deps.TabAsset = func(context.Context, string) (string, error) { return "asset-stage", nil }
	})
	stream := &agent.SliceStream{}
	response := h.run(t, stream, RunArgs{})
	first := waitEvent(t, stream, "confirmRequired")
	if err := h.manager.Confirm(agent.Confirmation{JobID: response.JobID, CallID: first.ID, Nonce: first.Nonce, Decision: "allow_session"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := counts(events); done != 1 || failed != 0 {
		t.Fatalf("events = %+v", events)
	}
	confirmations := 0
	for _, event := range events {
		if event.Type == "confirmRequired" {
			confirmations++
		}
	}
	if confirmations != 1 {
		t.Fatalf("confirmations = %d, want 1 (session memory auto-allows the second write)", confirmations)
	}
	h.mu.Lock()
	writes := len(h.aiWrites)
	h.mu.Unlock()
	if writes != 2 {
		t.Fatalf("writes = %d, want 2", writes)
	}
}

// waitEventUnlike waits for an event of the given kind whose ID differs from
// the excluded one, so a second confirmation is not mistaken for the first.
func waitEventUnlike(t *testing.T, stream *agent.SliceStream, kind, excludeID string) agent.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, _ := stream.Snapshot()
		for _, event := range events {
			if event.Type == kind && event.ID != excludeID {
				return event
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("takeover stream never produced %s with a new id", kind)
	return agent.Event{}
}
