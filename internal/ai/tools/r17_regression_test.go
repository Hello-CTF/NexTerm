package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

// R17: the durable adjudication consults the CURRENT asset resolution before
// any finished replay (a different valid identity means fresh execution), the
// family claim is atomic across registries (a concurrent caller gets
// pending/conflict before connecting), connect residuals reach the durable
// record on every path — including a crash before the final completion — and
// background creates persist their tab identity for replay and result query.

// R17-F1: the old session still lives with the old fingerprint while the
// current resolver already points at a new session on a new fingerprint: the
// recorded success is stale and the retry must execute fresh on the new
// identity instead of replaying.
func TestTerminalCreateDurableReplayConsultsCurrentResolution(t *testing.T) {
	_, idempotency, _ := outcomeFixture(t)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	durable := newR16DurableOutcomes(t, database)
	connected := false
	phase := 1
	fake := &identitySetTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0)), identities: map[string]AssetIdentity{
		"s9": identityA, "s10": identityB,
	}}
	resolver := func(context.Context, string) (string, AssetIdentity, error) {
		if !connected {
			return "", AssetIdentity{}, ErrNoLiveSession
		}
		if phase == 1 {
			return "s9", identityA, nil
		}
		return "s10", identityB, nil
	}
	connector := &r13Connector{sessionIDs: []string{"s9"}, onConnect: func(string) { connected = true }}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	registry := r16Registry(t, fake, connector, resolver, idempotency, durable)
	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK {
		t.Fatalf("first create = %+v", first)
	}
	// The asset is reconfigured: the current resolver now points at s10 on
	// the new fingerprint while the old session s9 still lives and still
	// reports the old one. The retry must execute fresh on s10.
	phase = 2
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if !second.OK {
		t.Fatalf("reconfigured retry = %+v", second)
	}
	if strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("reconfigured retry replayed the stale record: %+v", second)
	}
	if connector.callCount() != 1 || len(fake.created) != 2 {
		t.Fatalf("reconfigured retry did not execute fresh on the live session: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
	// A later retry replays the fresh attempt's recorded result.
	third := registry.Execute(context.Background(), "job", scope, call, nil)
	if !third.OK || !strings.Contains(third.Text, "调用已执行过") {
		t.Fatalf("post-reconfiguration retry did not replay: %+v", third)
	}
	if connector.callCount() != 1 || len(fake.created) != 2 {
		t.Fatalf("post-reconfiguration retry re-executed: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
}

// R17-F2: two registries sharing the durable store race the same operation:
// the atomic family claim admits exactly one caller; the loser gets a pending
// verdict before any connect.
func TestTerminalCreateDurableClaimConcurrentRegistriesConnectOnce(t *testing.T) {
	_, idempotency, _ := outcomeFixture(t)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	durable := newR16DurableOutcomes(t, database)
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	resolver := func(context.Context, string) (string, AssetIdentity, error) {
		return "", AssetIdentity{}, ErrNoLiveSession
	}
	connector := &r15BarrierConnector{sessionIDs: []string{"s9", "s10"}, entered: make(chan string, 2), release: make(chan struct{})}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	registryA := r16Registry(t, fake, connector, resolver, idempotency, durable)
	registryB := r16Registry(t, fake, connector, resolver, idempotency, durable)
	firstResult := make(chan Output, 1)
	go func() { firstResult <- registryA.Execute(context.Background(), "job", scope, call, nil) }()
	// The first caller claimed the family and is inside ConnectAsset.
	if sessionID := <-connector.entered; sessionID != "s9" {
		t.Fatalf("first connect = %s, want s9", sessionID)
	}
	duplicate := registryB.Execute(context.Background(), "job", scope, call, nil)
	if duplicate.OK {
		t.Fatalf("concurrent duplicate reported success: %+v", duplicate)
	}
	if !strings.Contains(duplicate.Text, "未完成") && !strings.Contains(duplicate.Text, "冲突") {
		t.Fatalf("concurrent duplicate not surfaced as pending/conflict: %+v", duplicate)
	}
	if duplicate.OperationID == "" {
		t.Fatalf("concurrent duplicate lost the operation ID: %+v", duplicate)
	}
	if connector.callCount() != 1 {
		t.Fatalf("concurrent duplicate connected: connector=%d", connector.callCount())
	}
	close(connector.release)
	first := <-firstResult
	if !first.OK {
		t.Fatalf("first create = %+v", first)
	}
	if connector.callCount() != 1 || len(fake.created) != 1 {
		t.Fatalf("first create: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
	// A later legitimate retry replays under the contract.
	retry := registryB.Execute(context.Background(), "job", scope, call, nil)
	if !retry.OK || !strings.Contains(retry.Text, "调用已执行过") {
		t.Fatalf("post-flight retry did not replay: %+v", retry)
	}
	if connector.callCount() != 1 || len(fake.created) != 1 {
		t.Fatalf("post-flight retry re-executed: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
}

// r17PartialErrorConnector returns a session ID alongside its error, modeling
// a connect that established a session but failed afterwards.
type r17PartialErrorConnector struct {
	sessionID string
	err       error
}

func (c r17PartialErrorConnector) ConnectAsset(context.Context, string) (string, error) {
	return c.sessionID, c.err
}

// identityC names a different asset, so a connected session reporting it
// fails the requested-asset verification.
var identityC = AssetIdentity{AssetID: "a2", ConfigFingerprint: "fp-a2"}

// R17-F3a: a connect that returns a session alongside its error persists the
// residual on the durable record — no hand-seeded row.
func TestTerminalCreateDurableConnectErrorRecordsResidual(t *testing.T) {
	_, idempotency, _ := outcomeFixture(t)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	durable := newR16DurableOutcomes(t, database)
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	connector := r17PartialErrorConnector{sessionID: "s9", err: errors.New("dial failed")}
	registry := r16Registry(t, fake, connector, func(context.Context, string) (string, AssetIdentity, error) {
		return "", AssetIdentity{}, ErrNoLiveSession
	}, idempotency, durable)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	result := registry.Execute(context.Background(), "job", scope, call, nil)
	if result.OK || !strings.Contains(result.Text, "dial failed") || !strings.Contains(result.Text, "session_id=s9") {
		t.Fatalf("connect-error result = %+v", result)
	}
	if len(fake.created) != 0 {
		t.Fatalf("connect error created tabs: %+v", fake.created)
	}
	query := registry.Execute(context.Background(), "job", scope, Call{ID: "q", Name: "operation_result", Args: json.RawMessage(`{"operation_id":"` + result.OperationID + `"}`)}, nil)
	if !query.OK || !strings.Contains(query.Text, "failed") || !strings.Contains(query.Text, "session_id=s9") || !strings.Contains(query.Text, "connect") {
		t.Fatalf("connect-error record = %+v", query)
	}
}

// R17-F3b: a post-connect identity verification failure persists the
// residual on the durable record.
func TestTerminalCreateDurableIdentityErrorRecordsResidual(t *testing.T) {
	_, idempotency, _ := outcomeFixture(t)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	durable := newR16DurableOutcomes(t, database)
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityC)
	connector := &r13Connector{sessionIDs: []string{"s9"}}
	registry := r16Registry(t, fake, connector, func(context.Context, string) (string, AssetIdentity, error) {
		return "", AssetIdentity{}, ErrNoLiveSession
	}, idempotency, durable)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	result := registry.Execute(context.Background(), "job", scope, call, nil)
	if result.OK || !strings.Contains(result.Text, "a2") || !strings.Contains(result.Text, "session_id=s9") {
		t.Fatalf("identity-error result = %+v", result)
	}
	if len(fake.created) != 0 {
		t.Fatalf("identity error created tabs: %+v", fake.created)
	}
	query := registry.Execute(context.Background(), "job", scope, Call{ID: "q", Name: "operation_result", Args: json.RawMessage(`{"operation_id":"` + result.OperationID + `"}`)}, nil)
	if !query.OK || !strings.Contains(query.Text, "failed") || !strings.Contains(query.Text, "session_id=s9") {
		t.Fatalf("identity-error record = %+v", query)
	}
}

// R17-F3c: a crash between the connect and the final completion leaves the
// claim row initiated with the progress residual — the retry is blocked with
// the residual named, and the result query shows it.
func TestTerminalCreateDurableCrashBoundaryPendingKeepsResidual(t *testing.T) {
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
	connector := &r13Connector{sessionIDs: []string{"s9"}, onConnect: func(string) { connected = true }}
	resolver := func(context.Context, string) (string, AssetIdentity, error) {
		if !connected {
			return "", AssetIdentity{}, ErrNoLiveSession
		}
		return "s9", identityA, nil
	}
	registry := r16Registry(t, fake, connector, resolver, idempotency, durable)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	// The completion write fails: the process "crashes" between the connect
	// and the final record. The claim row keeps the progress residual.
	durable.completeErr = errors.New("injected completion failure")
	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK {
		t.Fatalf("first create = %+v", first)
	}
	if !strings.Contains(first.Text, "durable 执行记录写入失败") {
		t.Fatalf("completion failure not surfaced: %+v", first)
	}
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if second.OK || !strings.Contains(second.Text, "未完成") || !strings.Contains(second.Text, "session_id=s9") {
		t.Fatalf("crash-boundary retry = %+v", second)
	}
	if connector.callCount() != 1 || len(fake.created) != 1 {
		t.Fatalf("crash-boundary retry re-executed: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
	query := registry.Execute(context.Background(), "job", scope, Call{ID: "q", Name: "operation_result", Args: json.RawMessage(`{"operation_id":"` + first.OperationID + `"}`)}, nil)
	if !query.OK || !strings.Contains(query.Text, "initiated") || !strings.Contains(query.Text, "session_id=s9") {
		t.Fatalf("crash-boundary record = %+v", query)
	}
}

// R17-F4: a background create persists its tab identity for the durable
// record without changing the background display semantics; the replay and
// the result query restore the full identity.
func TestTerminalCreateBackgroundDurableReplayRestoresResult(t *testing.T) {
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
	registry := r16Registry(t, fake, connector, resolver, idempotency, durable)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true,"background":true}`)
	call.AuthorizationID = "guard:test"

	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK {
		t.Fatalf("background create = %+v", first)
	}
	if first.Terminal != nil {
		t.Fatalf("background create exposed the foreground Terminal field: %+v", first.Terminal)
	}
	if !strings.Contains(first.Text, "后台") || !strings.Contains(first.Text, "session_id=s9") {
		t.Fatalf("background create text = %+v", first)
	}
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if !second.OK || !strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("background replay = %+v", second)
	}
	if second.Terminal == nil || second.Terminal.Handle != "t/tab-new/s9" || second.Terminal.TabID != "tab-new" {
		t.Fatalf("background replay lost the created tab identity: %+v", second)
	}
	query := registry.Execute(context.Background(), "job", scope, Call{ID: "q", Name: "operation_result", Args: json.RawMessage(`{"operation_id":"` + first.OperationID + `"}`)}, nil)
	if !query.OK || !strings.Contains(query.Text, "succeeded") || !strings.Contains(query.Text, "tab-new") || !strings.Contains(query.Text, "t/tab-new/s9") {
		t.Fatalf("background result query = %+v", query)
	}
}
