package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

// R18: a claim that lands after a completed-and-released attempt must
// re-adjudicate before any side effect — the terminal record that raced in
// replays (or conflicts) instead of a duplicate execution — and the progress
// residual write survives a canceled caller context with a bounded timeout,
// so a crash before the final completion still leaves the residual on the
// initiated claim.

// R18-F1: B's first adjudication is empty, A completes and releases, then B's
// claim succeeds. B must re-adjudicate after the claim, see A's terminal
// record, and replay it — never execute a duplicate side effect.
func TestTerminalCreateDurableClaimRacingCompletedAttemptReplays(t *testing.T) {
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

	registryA := r16Registry(t, fake, connector, resolver, idempotency, durableA)
	registryB := r16Registry(t, fake, connector, resolver, idempotency, durableB)
	resultB := make(chan Output, 1)
	go func() { resultB <- registryB.Execute(context.Background(), "job", scope, call, nil) }()
	// B adjudicated empty and is parked at its claim.
	<-durableB.claimStarted
	// A runs the whole flow: claim, connect, create, complete, release.
	first := registryA.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK {
		t.Fatalf("first create = %+v", first)
	}
	if connector.callCount() != 1 || len(fake.created) != 1 {
		t.Fatalf("first create: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
	// B's claim now succeeds: it must re-adjudicate, see A's terminal record,
	// and replay it instead of executing a duplicate side effect.
	close(gate)
	second := <-resultB
	if !second.OK || !strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("racing claim did not replay the completed attempt: %+v", second)
	}
	if !strings.Contains(second.Text, "session_id=s9") {
		t.Fatalf("replay lost the original session note: %+v", second)
	}
	if second.Terminal == nil || second.Terminal.Handle != "t/tab-new/s9" {
		t.Fatalf("replay lost the recorded result payload: %+v", second)
	}
	if connector.callCount() != 1 {
		t.Fatalf("racing claim reconnected: connector=%d", connector.callCount())
	}
	if len(fake.created) != 1 {
		t.Fatalf("racing claim re-created: %d tabs", len(fake.created))
	}
	// The abort released B's claim: a later legitimate retry still replays.
	third := registryA.Execute(context.Background(), "job", scope, call, nil)
	if !third.OK || !strings.Contains(third.Text, "调用已执行过") {
		t.Fatalf("post-abort retry did not replay: %+v", third)
	}
	if connector.callCount() != 1 || len(fake.created) != 1 {
		t.Fatalf("post-abort retry re-executed: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
}

// R18-F2: the connect returns a non-empty session while the caller's context
// is canceled; the progress residual still persists with a bounded timeout,
// and the crash-boundary claim row keeps the residual for the result query.
func TestTerminalCreateDurableProgressResidualSurvivesCanceledContext(t *testing.T) {
	_, idempotency, _ := outcomeFixture(t)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	durable := newR16DurableOutcomes(t, database)
	durable.completeErr = errInjectedCompleteFailure
	ctx, cancel := context.WithCancel(context.Background())
	connector := &r18CancelConnector{sessionID: "s9", cancel: cancel}
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	registry := r16Registry(t, fake, connector, func(context.Context, string) (string, AssetIdentity, error) {
		return "", AssetIdentity{}, ErrNoLiveSession
	}, idempotency, durable)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	result := registry.Execute(ctx, "job", scope, call, nil)
	if result.OK {
		t.Fatalf("canceled-context create reported success: %+v", result)
	}
	if !strings.Contains(result.Text, "session_id=s9") {
		t.Fatalf("canceled-context result lost the residual session: %+v", result)
	}
	// The progress residual persisted despite the canceled context, so the
	// crash-boundary claim row carries it and the result query shows it.
	query := registry.Execute(context.Background(), "job", scope, Call{ID: "q", Name: "operation_result", Args: json.RawMessage(`{"operation_id":"` + result.OperationID + `"}`)}, nil)
	if !query.OK || !strings.Contains(query.Text, "initiated") || !strings.Contains(query.Text, "session_id=s9") {
		t.Fatalf("crash-boundary record lost the residual: %+v", query)
	}
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if second.OK || !strings.Contains(second.Text, "未完成") || !strings.Contains(second.Text, "session_id=s9") {
		t.Fatalf("crash-boundary retry = %+v", second)
	}
	if connector.calls != 1 {
		t.Fatalf("crash-boundary retry reconnected: %d", connector.calls)
	}
}

var errInjectedCompleteFailure = &r18InjectedError{"injected completion failure"}

type r18InjectedError struct{ message string }

func (e *r18InjectedError) Error() string { return e.message }

// r18CancelConnector cancels the caller's context mid-connect and returns the
// session ID alongside the context error, per the connector contract.
type r18CancelConnector struct {
	sessionID string
	cancel    context.CancelFunc
	calls     int
}

func (c *r18CancelConnector) ConnectAsset(ctx context.Context, _ string) (string, error) {
	c.calls++
	c.cancel()
	return c.sessionID, ctx.Err()
}
