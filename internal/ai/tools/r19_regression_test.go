package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

// R19: a durable fresh attempt after a failed record must actually execute —
// the inner idempotency ledger's key carries the new attempt identity, so the
// old finished failure cannot short-circuit the effect. Success replay,
// conflict, pending rejection, reconfigured fresh, residual/operation ID
// contracts are unchanged.

// r19DurableStates returns the attempt states under the operation prefix,
// newest first.
func r19DurableStates(t *testing.T, database *store.Store, family string) []string {
	t.Helper()
	rows, err := database.AgentOutcomeListByPrefix(context.Background(), family, 0)
	if err != nil {
		t.Fatal(err)
	}
	states := make([]string, 0, len(rows))
	for _, row := range rows {
		states = append(states, row.State)
	}
	return states
}

// R19-F1a: sequential failed-retry. The first attempt fails at the create
// and the durable ledger records exactly one failed attempt; once the fault
// clears, the same-identity retry must execute fresh and succeed — never
// replay the old failure.
func TestTerminalCreateDurableFailedRetryExecutesFresh(t *testing.T) {
	_, idempotency, _ := outcomeFixture(t)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	durable := newR16DurableOutcomes(t, database)
	connected := false
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	fake.createErr = errors.New("injected create failure")
	resolver := func(context.Context, string) (string, AssetIdentity, error) {
		if !connected {
			return "", AssetIdentity{}, ErrNoLiveSession
		}
		return "s9", identityA, nil
	}
	connector := &r13Connector{sessionIDs: []string{"s9"}, onConnect: func(string) { connected = true }}
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
	// The fault clears: the same-identity retry must execute fresh.
	fake.createErr = nil
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if !second.OK {
		t.Fatalf("failed retry did not execute fresh: %+v", second)
	}
	if strings.Contains(second.Text, "调用已执行过") || strings.Contains(second.Text, "injected create failure") {
		t.Fatalf("failed retry replayed the old failure: %+v", second)
	}
	if len(fake.created) != 1 {
		t.Fatalf("failed retry did not create exactly one tab: %d", len(fake.created))
	}
	if connector.callCount() != 1 {
		t.Fatalf("failed retry reconnected: connector=%d", connector.callCount())
	}
	if states := r19DurableStates(t, database, family); len(states) != 2 || states[0] != store.AgentOutcomeStateSucceeded || states[1] != store.AgentOutcomeStateFailed {
		t.Fatalf("durable history after fresh success = %v", states)
	}
	// A later retry replays the fresh success.
	third := registry.Execute(context.Background(), "job", scope, call, nil)
	if !third.OK || !strings.Contains(third.Text, "调用已执行过") {
		t.Fatalf("post-success retry did not replay: %+v", third)
	}
	if len(fake.created) != 1 || connector.callCount() != 1 {
		t.Fatalf("post-success retry re-executed: created=%d connector=%d", len(fake.created), connector.callCount())
	}
}

// R19-F1b: claim race with a failed completion. B's first adjudication is
// empty; A claims, connects, fails the create, completes the durable failed
// record, and releases; B then claims and — after clearing the fault — must
// execute exactly once, not replay the old failure.
func TestTerminalCreateDurableClaimRacingFailedAttemptExecutesFresh(t *testing.T) {
	_, idempotency, _ := outcomeFixture(t)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	durableA := newR16DurableOutcomes(t, database)
	durableB := newR16DurableOutcomes(t, database)
	gate := make(chan struct{})
	durableB.claimStarted = make(chan struct{})
	durableB.claimGate = gate
	connected := false
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	fake.createErr = errors.New("injected create failure")
	resolver := func(context.Context, string) (string, AssetIdentity, error) {
		if !connected {
			return "", AssetIdentity{}, ErrNoLiveSession
		}
		return "s9", identityA, nil
	}
	connector := &r13Connector{sessionIDs: []string{"s9", "s10"}, onConnect: func(string) { connected = true }}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"
	family := operationID("job", "c", "terminal_create")

	registryA := r16Registry(t, fake, connector, resolver, idempotency, durableA)
	registryB := r16Registry(t, fake, connector, resolver, idempotency, durableB)
	resultB := make(chan Output, 1)
	go func() { resultB <- registryB.Execute(context.Background(), "job", scope, call, nil) }()
	// B adjudicated empty and is parked at its claim.
	<-durableB.claimStarted
	// A claims, connects, fails the create, and completes the failed record.
	first := registryA.Execute(context.Background(), "job", scope, call, nil)
	if first.OK || !strings.Contains(first.Text, "injected create failure") {
		t.Fatalf("first create = %+v", first)
	}
	if states := r19DurableStates(t, database, family); len(states) != 1 || states[0] != store.AgentOutcomeStateFailed {
		t.Fatalf("durable history after A's failure = %v, want one failed", states)
	}
	// The fault clears; B's claim now succeeds and its second adjudication
	// sees the failed record: B must execute exactly once, not replay it.
	fake.createErr = nil
	close(gate)
	second := <-resultB
	if !second.OK {
		t.Fatalf("racing claim did not execute fresh: %+v", second)
	}
	if strings.Contains(second.Text, "调用已执行过") || strings.Contains(second.Text, "injected create failure") {
		t.Fatalf("racing claim replayed the old failure: %+v", second)
	}
	if len(fake.created) != 1 {
		t.Fatalf("racing claim did not create exactly one tab: %d", len(fake.created))
	}
	if connector.callCount() != 1 {
		t.Fatalf("racing claim reconnected: connector=%d", connector.callCount())
	}
	if states := r19DurableStates(t, database, family); len(states) != 2 || states[0] != store.AgentOutcomeStateSucceeded || states[1] != store.AgentOutcomeStateFailed {
		t.Fatalf("durable history after B's fresh success = %v", states)
	}
}
