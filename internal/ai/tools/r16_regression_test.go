package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/outcome"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// R16: the durable, owner-scoped outcome ledger (M74's
// AgentOutcomeListByPrefix+Adjudicate) adjudicates a recorded terminal_create
// before any ConnectAsset: conflict and pending attempts never reconnect,
// a finished attempt replays only when the canonical binding and the original
// session identity still hold, and a reconfigured asset executes fresh. The
// operation_result tool queries the recorded outcome by its operation ID.

// r16DurableOutcomes adapts the real store's agent-outcome methods to the
// tools DurableOutcomeLedger contract, replicating M74's Adjudicate
// classification (agent.OutcomeLedger itself cannot be imported here without
// a test import cycle).
type r16DurableOutcomes struct {
	store *store.Store
	now   func() int64
	// completeErr, when set, fails the completion write to simulate a crash
	// between the connect and the final record.
	completeErr error
	// claimStarted is closed on the first ClaimOutcome call; claimGate, when
	// set, blocks every ClaimOutcome until it closes — together they park a
	// caller between its first adjudication and its claim.
	claimStarted chan struct{}
	claimGate    chan struct{}
	claimOnce    sync.Once
}

func newR16DurableOutcomes(t *testing.T, database *store.Store) *r16DurableOutcomes {
	t.Helper()
	return &r16DurableOutcomes{store: database, now: func() int64 { return time.Now().UnixMilli() }}
}

func (f *r16DurableOutcomes) ClaimOutcome(ctx context.Context, family, kind string) (string, bool, error) {
	if f.claimStarted != nil {
		f.claimOnce.Do(func() { close(f.claimStarted) })
	}
	if f.claimGate != nil {
		<-f.claimGate
	}
	claimed, err := f.store.AgentOutcomeClaimAttempt(ctx, family, kind)
	return store.AgentOutcomeClaimRowID(family), claimed, err
}

func (f *r16DurableOutcomes) ProgressOutcome(ctx context.Context, family, residualSessionID string) error {
	return f.store.AgentOutcomeProgress(ctx, family, residualSessionID)
}

func (f *r16DurableOutcomes) ReleaseOutcome(ctx context.Context, family string) error {
	return f.store.AgentOutcomeReleaseClaim(ctx, family)
}

func (f *r16DurableOutcomes) CompleteOutcome(ctx context.Context, family string, attempt OutcomeAttempt) error {
	if f.completeErr != nil {
		return f.completeErr
	}
	return f.store.AgentOutcomeCompleteAttempt(ctx, family, r16RowFromAttempt(attempt, f.now()))
}

func r16RowFromAttempt(attempt OutcomeAttempt, now int64) store.AgentOutcomeRow {
	return store.AgentOutcomeRow{
		OperationID: attempt.OperationID, Kind: attempt.Kind, State: attempt.State,
		FailureStage:      attempt.FailureStage,
		ResidualSessionID: attempt.ResidualSessionID, ResidualResource: attempt.ResidualResource,
		ResultJSON: string(attempt.Result), Error: attempt.Error, FinishedAt: &now,
	}
}

func (f *r16DurableOutcomes) AdjudicateOutcome(ctx context.Context, prefix string) (OutcomeAdjudication, error) {
	rows, err := f.store.AgentOutcomeListByPrefix(ctx, prefix, 65)
	if err != nil {
		return OutcomeAdjudication{}, err
	}
	var adjudication OutcomeAdjudication
	if len(rows) > 64 {
		adjudication.Conflict = true
		rows = rows[:64]
	}
	pending := 0
	for _, row := range rows {
		attempt := r16AttemptFromRow(row)
		switch {
		case row.State == store.AgentOutcomeStateInitiated:
			pending++
			if adjudication.Pending == nil {
				adjudication.Pending = &attempt
			}
		case adjudication.Terminal == nil:
			adjudication.Terminal = &attempt
		}
	}
	if pending > 1 {
		adjudication.Conflict = true
	}
	return adjudication, nil
}

func (f *r16DurableOutcomes) OutcomeResult(ctx context.Context, operationID string) (OutcomeAttempt, error) {
	rows, err := f.store.AgentOutcomeListByPrefix(ctx, operationID, 1)
	if err != nil {
		return OutcomeAttempt{}, err
	}
	if len(rows) == 0 {
		return OutcomeAttempt{}, errors.New("outcome 记录不存在")
	}
	return r16AttemptFromRow(rows[0]), nil
}

func r16AttemptFromRow(row store.AgentOutcomeRow) OutcomeAttempt {
	return OutcomeAttempt{
		OperationID: row.OperationID, Kind: row.Kind, State: row.State,
		FailureStage:      row.FailureStage,
		ResidualSessionID: row.ResidualSessionID, ResidualResource: row.ResidualResource,
		Result: json.RawMessage(row.ResultJSON), Error: row.Error,
	}
}

// r16Registry builds a recorded terminal_create registry with both ledgers:
// the idempotency ledger (outcomeFixture) and the durable owner-scoped one.
func r16Registry(t *testing.T, terminal Terminal, connector AssetConnector, resolver func(context.Context, string) (string, AssetIdentity, error), idempotency *outcome.Ledger, durable DurableOutcomeLedger) *Registry {
	t.Helper()
	registry := p1Registry(terminal)
	registry.deps.Outcome = idempotency
	registry.deps.DurableOutcomes = durable
	registry.deps.ResolveAssetSessionIdentity = resolver
	registry.deps.AssetConnector = connector
	return registry
}

func r16Nonce() string {
	var bytes [8]byte
	_, _ = rand.Read(bytes[:])
	return hex.EncodeToString(bytes[:])
}

func TestTerminalCreateDurableAdjudicationCrossRegistryReplay(t *testing.T) {
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
	resolver := func(context.Context, string) (string, AssetIdentity, error) {
		if !connected || vacated {
			return "", AssetIdentity{}, ErrNoLiveSession
		}
		return "s9", identityA, nil
	}
	connector := &r13Connector{sessionIDs: []string{"s9", "s10"}, onConnect: func(string) { connected = true }}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	registryA := r16Registry(t, fake, connector, resolver, idempotency, durable)
	first := registryA.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK {
		t.Fatalf("first connected create = %+v", first)
	}
	if connector.callCount() != 1 || len(fake.created) != 1 {
		t.Fatalf("first create: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
	// The session vanishes; a fresh registry over the same durable store must
	// adjudicate the finished record and replay instead of reconnecting.
	vacated = true
	registryB := r16Registry(t, fake, connector, resolver, idempotency, durable)
	second := registryB.Execute(context.Background(), "job", scope, call, nil)
	if !second.OK || !strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("cross-registry retry did not replay: %+v", second)
	}
	if !strings.Contains(second.Text, "session_id=s9") {
		t.Fatalf("replay lost the original session note: %+v", second)
	}
	if second.Terminal == nil || second.Terminal.Handle != "t/tab-new/s9" {
		t.Fatalf("replay lost the recorded result payload: %+v", second)
	}
	if connector.callCount() != 1 {
		t.Fatalf("cross-registry retry reconnected: connector=%d", connector.callCount())
	}
	if len(fake.created) != 1 {
		t.Fatalf("cross-registry retry re-created: %d tabs", len(fake.created))
	}
}

func TestTerminalCreateDurableAdjudicationSurvivesRestart(t *testing.T) {
	_, idempotency, _ := outcomeFixture(t)
	path := filepath.Join(t.TempDir(), "outcome.db")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	durable := newR16DurableOutcomes(t, database)
	connected := false
	vacated := false
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	resolver := func(context.Context, string) (string, AssetIdentity, error) {
		if !connected || vacated {
			return "", AssetIdentity{}, ErrNoLiveSession
		}
		return "s9", identityA, nil
	}
	connector := &r13Connector{sessionIDs: []string{"s9", "s10"}, onConnect: func(string) { connected = true }}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	registryA := r16Registry(t, fake, connector, resolver, idempotency, durable)
	first := registryA.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK {
		t.Fatalf("first connected create = %+v", first)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	vacated = true
	registryB := r16Registry(t, fake, connector, resolver, idempotency, newR16DurableOutcomes(t, reopened))
	second := registryB.Execute(context.Background(), "job", scope, call, nil)
	if !second.OK || !strings.Contains(second.Text, "调用已执行过") || !strings.Contains(second.Text, "session_id=s9") {
		t.Fatalf("post-restart retry did not replay: %+v", second)
	}
	if connector.callCount() != 1 || len(fake.created) != 1 {
		t.Fatalf("post-restart retry re-executed: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
	// The result query resolves the recorded outcome on the reopened store.
	query := registryB.Execute(context.Background(), "job", scope, Call{ID: "q", Name: "operation_result", Args: json.RawMessage(`{"operation_id":"` + first.OperationID + `"}`)}, nil)
	if !query.OK || !strings.Contains(query.Text, "succeeded") || !strings.Contains(query.Text, "session_id=s9") {
		t.Fatalf("operation_result after restart = %+v", query)
	}
}

func TestTerminalCreateDurableAdjudicationPendingBlocksReconnect(t *testing.T) {
	_, idempotency, _ := outcomeFixture(t)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	durable := newR16DurableOutcomes(t, database)
	operation := operationID("job", "c", "terminal_create")
	// A crashed attempt: claimed but never completed, with the connected
	// session persisted as its progress residual — produced by the real
	// store primitives, not hand-seeded.
	if claimed, err := database.AgentOutcomeClaimAttempt(context.Background(), operation, "terminal_create"); err != nil || !claimed {
		t.Fatalf("seed claim = %t err=%v", claimed, err)
	}
	if err := database.AgentOutcomeProgress(context.Background(), operation, "s9"); err != nil {
		t.Fatal(err)
	}

	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	connector := &r13Connector{sessionIDs: []string{"s10"}}
	registry := r16Registry(t, fake, connector, func(context.Context, string) (string, AssetIdentity, error) {
		return "", AssetIdentity{}, ErrNoLiveSession
	}, idempotency, durable)
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"}, call, nil)
	if result.OK {
		t.Fatalf("pending-record retry reported success: %+v", result)
	}
	if !strings.Contains(result.Text, "未完成") || !strings.Contains(result.Text, "session_id=s9") {
		t.Fatalf("pending record not surfaced with its residual: %+v", result)
	}
	if connector.callCount() != 0 || len(fake.created) != 0 {
		t.Fatalf("pending retry connected or created: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
}

func TestTerminalCreateDurableAdjudicationConflictBlocksReconnect(t *testing.T) {
	_, idempotency, _ := outcomeFixture(t)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	durable := newR16DurableOutcomes(t, database)
	operation := operationID("job", "c", "terminal_create")
	// Two unfinished attempts under one operation family: conflicted.
	for _, suffix := range []string{":a", ":b"} {
		if _, _, err := database.AgentOutcomeBegin(context.Background(), store.AgentOutcomeRow{
			OperationID: operation + suffix, Kind: "terminal_create", State: store.AgentOutcomeStateInitiated,
		}); err != nil {
			t.Fatal(err)
		}
	}

	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	connector := &r13Connector{sessionIDs: []string{"s10"}}
	registry := r16Registry(t, fake, connector, func(context.Context, string) (string, AssetIdentity, error) {
		return "", AssetIdentity{}, ErrNoLiveSession
	}, idempotency, durable)
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"}, call, nil)
	if result.OK || !strings.Contains(result.Text, "冲突") {
		t.Fatalf("conflicted record retry = %+v", result)
	}
	if connector.callCount() != 0 || len(fake.created) != 0 {
		t.Fatalf("conflicted retry connected or created: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
}

func TestTerminalCreateDurableAdjudicationRejectsChangedArguments(t *testing.T) {
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
	resolver := func(context.Context, string) (string, AssetIdentity, error) {
		if !connected || vacated {
			return "", AssetIdentity{}, ErrNoLiveSession
		}
		return "s9", identityA, nil
	}
	connector := &r13Connector{sessionIDs: []string{"s9", "s10"}, onConnect: func(string) { connected = true }}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	first := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true,"title":"first"}`)
	first.AuthorizationID = "guard:test"

	registry := r16Registry(t, fake, connector, resolver, idempotency, durable)
	if result := registry.Execute(context.Background(), "job", scope, first, nil); !result.OK {
		t.Fatalf("first create = %+v", result)
	}
	// The same authorization retries the same call ID with a changed title:
	// the durable binding contract makes this a conflict, not a replay.
	vacated = true
	changed := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true,"title":"changed"}`)
	changed.AuthorizationID = "guard:test"
	second := registry.Execute(context.Background(), "job", scope, changed, nil)
	if second.OK || !strings.Contains(second.Text, "冲突") {
		t.Fatalf("changed-argument retry = %+v", second)
	}
	if connector.callCount() != 1 || len(fake.created) != 1 {
		t.Fatalf("changed-argument retry re-executed: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
	// The unchanged retry replays under the same contract.
	third := registry.Execute(context.Background(), "job", scope, first, nil)
	if !third.OK || !strings.Contains(third.Text, "调用已执行过") {
		t.Fatalf("unchanged-argument retry did not replay: %+v", third)
	}
	if connector.callCount() != 1 || len(fake.created) != 1 {
		t.Fatalf("unchanged-argument retry re-executed: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
}

func TestTerminalCreateDurableAdjudicationReconfiguredExecutesFresh(t *testing.T) {
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
	resolver := func(context.Context, string) (string, AssetIdentity, error) {
		if !connected || vacated {
			return "", AssetIdentity{}, ErrNoLiveSession
		}
		return "s9", identityA, nil
	}
	connector := &r13Connector{sessionIDs: []string{"s9", "s10"}, onConnect: func(string) { connected = true }}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	registry := r16Registry(t, fake, connector, resolver, idempotency, durable)
	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK {
		t.Fatalf("first create = %+v", first)
	}
	// The asset is reconfigured and the session vanishes: the recorded
	// success is stale, so the retry executes fresh on the new identity.
	vacated = true
	fake.setIdentity(identityB)
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if !second.OK {
		t.Fatalf("reconfigured retry = %+v", second)
	}
	if strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("reconfigured retry replayed the stale record: %+v", second)
	}
	if connector.callCount() != 2 || len(fake.created) != 2 {
		t.Fatalf("reconfigured retry did not execute fresh: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
	// A later retry replays the fresh attempt's recorded result.
	third := registry.Execute(context.Background(), "job", scope, call, nil)
	if !third.OK || !strings.Contains(third.Text, "调用已执行过") {
		t.Fatalf("post-reconfiguration retry did not replay: %+v", third)
	}
	if connector.callCount() != 2 || len(fake.created) != 2 {
		t.Fatalf("post-reconfiguration retry re-executed: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
}

func TestOperationResultQuery(t *testing.T) {
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
	resolver := func(context.Context, string) (string, AssetIdentity, error) {
		if !connected {
			return "", AssetIdentity{}, ErrNoLiveSession
		}
		return "s9", identityA, nil
	}
	connector := &r13Connector{sessionIDs: []string{"s9"}, onConnect: func(string) { connected = true }}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	registry := r16Registry(t, fake, connector, resolver, idempotency, durable)
	created := registry.Execute(context.Background(), "job", scope, call, nil)
	if !created.OK || created.OperationID == "" {
		t.Fatalf("create = %+v", created)
	}
	query := registry.Execute(context.Background(), "job", scope, Call{ID: "q", Name: "operation_result", Args: json.RawMessage(`{"operation_id":"` + created.OperationID + `"}`)}, nil)
	if !query.OK {
		t.Fatalf("operation_result = %+v", query)
	}
	for _, want := range []string{"succeeded", "terminal_create", "session_id=s9", "tab-new"} {
		if !strings.Contains(query.Text, want) {
			t.Fatalf("operation_result missing %q: %+v", want, query)
		}
	}
	missing := registry.Execute(context.Background(), "job", scope, Call{ID: "q2", Name: "operation_result", Args: json.RawMessage(`{"operation_id":"op-unknown"}`)}, nil)
	if missing.OK || !strings.Contains(missing.Text, "不存在") {
		t.Fatalf("operation_result unknown = %+v", missing)
	}
}
