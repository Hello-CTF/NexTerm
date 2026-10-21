package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

// R20: a durable fresh attempt after a failure must survive the loss of the
// originally connected session. When the retry's resolver returns
// ErrNoLiveSession, the pre-connect adjudication must honor the durable
// fresh-after-failure verdict and its attempt scope instead of replaying the
// previous attempt's finished failure: the retry connects a new session and
// executes the create exactly once, and the durable history preserves both
// the old failed attempt's residual and the new attempt's residual.

// r20DurableRows returns the durable attempt rows under the operation
// prefix, newest first.
func r20DurableRows(t *testing.T, database *store.Store, family string) []store.AgentOutcomeRow {
	t.Helper()
	rows, err := database.AgentOutcomeListByPrefix(context.Background(), family, 0)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// R20-F1: a connected create fails; the session then vanishes. The
// same-identity retry — resolver returning ErrNoLiveSession — must establish
// a fresh session and create exactly once, never replay the old failure, and
// the durable history must keep both residuals: the failed attempt's original
// session and the fresh attempt's newly connected one.
func TestTerminalCreateDurableFailedRetryReconnectsAfterSessionLoss(t *testing.T) {
	_, idempotency, _ := outcomeFixture(t)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	durable := newR16DurableOutcomes(t, database)
	connected := false
	vacated := false
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	fake.createErr = errors.New("injected create failure")
	resolver := func(context.Context, string) (string, AssetIdentity, error) {
		if !connected || vacated {
			return "", AssetIdentity{}, ErrNoLiveSession
		}
		return "s9", identityA, nil
	}
	connector := &r13Connector{sessionIDs: []string{"s9", "s10"}, onConnect: func(string) { connected = true }}
	registry := r16Registry(t, fake, connector, resolver, idempotency, durable)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"
	family := operationID("job", "c", "terminal_create")

	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if first.OK || !strings.Contains(first.Text, "injected create failure") {
		t.Fatalf("first create = %+v", first)
	}
	if states := r19DurableStates(t, database, family); len(states) != 1 || states[0] != store.AgentOutcomeStateFailed {
		t.Fatalf("durable history after failure = %v, want one failed", states)
	}
	// The create fault clears, but the originally connected session is gone:
	// the retry must connect a fresh session and execute the create once.
	fake.createErr = nil
	vacated = true
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if !second.OK {
		t.Fatalf("failed retry with a lost session did not execute fresh: %+v", second)
	}
	if strings.Contains(second.Text, "调用已执行过") || strings.Contains(second.Text, "injected create failure") {
		t.Fatalf("failed retry replayed the old failure: %+v", second)
	}
	if len(fake.created) != 1 {
		t.Fatalf("failed retry did not create exactly one tab: %d", len(fake.created))
	}
	if connector.callCount() != 2 {
		t.Fatalf("failed retry did not reconnect exactly once: connector=%d", connector.callCount())
	}
	if second.Terminal == nil || second.Terminal.Handle != "t/tab-new/s10" {
		t.Fatalf("failed retry lost the fresh session binding: %+v", second)
	}
	rows := r20DurableRows(t, database, family)
	if len(rows) != 2 || rows[0].State != store.AgentOutcomeStateSucceeded || rows[1].State != store.AgentOutcomeStateFailed {
		t.Fatalf("durable history after fresh success = %v", r19DurableStates(t, database, family))
	}
	if rows[0].ResidualSessionID != "s10" {
		t.Fatalf("fresh attempt residual = %q, want the newly connected s10", rows[0].ResidualSessionID)
	}
	if rows[1].ResidualSessionID != "s9" {
		t.Fatalf("failed attempt residual = %q, want the original s9 preserved", rows[1].ResidualSessionID)
	}
	// A later retry replays the fresh success without reconnecting again.
	third := registry.Execute(context.Background(), "job", scope, call, nil)
	if !third.OK || !strings.Contains(third.Text, "调用已执行过") {
		t.Fatalf("post-success retry did not replay: %+v", third)
	}
	if len(fake.created) != 1 || connector.callCount() != 2 {
		t.Fatalf("post-success retry re-executed: created=%d connector=%d", len(fake.created), connector.callCount())
	}
}

// R20-F2: the same lost-session failed retry through the legacy plain
// resolver branch: the pre-connect adjudication must equally honor the
// durable fresh-after-failure verdict instead of replaying the old failure.
func TestTerminalCreateDurableFailedRetryReconnectsAfterSessionLossLegacy(t *testing.T) {
	_, idempotency, _ := outcomeFixture(t)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	durable := newR16DurableOutcomes(t, database)
	liveSession := ""
	vacated := false
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	fake.createErr = errors.New("injected create failure")
	registry := p1Registry(fake)
	registry.deps.Outcome = idempotency
	registry.deps.DurableOutcomes = durable
	registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) {
		if liveSession == "" || vacated {
			return "", ErrNoLiveSession
		}
		return liveSession, nil
	}
	connector := &r13Connector{sessionIDs: []string{"s9", "s10"}, onConnect: func(sessionID string) { liveSession = sessionID }}
	registry.deps.AssetConnector = connector
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"
	family := operationID("job", "c", "terminal_create")

	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if first.OK || !strings.Contains(first.Text, "injected create failure") {
		t.Fatalf("first create = %+v", first)
	}
	if states := r19DurableStates(t, database, family); len(states) != 1 || states[0] != store.AgentOutcomeStateFailed {
		t.Fatalf("durable history after failure = %v, want one failed", states)
	}
	// The create fault clears, but the originally connected session is gone:
	// the retry must connect a fresh session and execute the create once.
	fake.createErr = nil
	vacated = true
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if !second.OK {
		t.Fatalf("failed retry with a lost session did not execute fresh: %+v", second)
	}
	if strings.Contains(second.Text, "调用已执行过") || strings.Contains(second.Text, "injected create failure") {
		t.Fatalf("failed retry replayed the old failure: %+v", second)
	}
	if len(fake.created) != 1 {
		t.Fatalf("failed retry did not create exactly one tab: %d", len(fake.created))
	}
	if connector.callCount() != 2 {
		t.Fatalf("failed retry did not reconnect exactly once: connector=%d", connector.callCount())
	}
	rows := r20DurableRows(t, database, family)
	if len(rows) != 2 || rows[0].State != store.AgentOutcomeStateSucceeded || rows[1].State != store.AgentOutcomeStateFailed {
		t.Fatalf("durable history after fresh success = %v", r19DurableStates(t, database, family))
	}
	if rows[0].ResidualSessionID != "s10" {
		t.Fatalf("fresh attempt residual = %q, want the newly connected s10", rows[0].ResidualSessionID)
	}
	if rows[1].ResidualSessionID != "s9" {
		t.Fatalf("failed attempt residual = %q, want the original s9 preserved", rows[1].ResidualSessionID)
	}
	// A later retry replays the fresh success without reconnecting again.
	third := registry.Execute(context.Background(), "job", scope, call, nil)
	if !third.OK || !strings.Contains(third.Text, "调用已执行过") {
		t.Fatalf("post-success retry did not replay: %+v", third)
	}
	if len(fake.created) != 1 || connector.callCount() != 2 {
		t.Fatalf("post-success retry re-executed: created=%d connector=%d", len(fake.created), connector.callCount())
	}
}
