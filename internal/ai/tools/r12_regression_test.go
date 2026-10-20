package tools

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

// R12-F3: terminal_create goes through the outcome ledger like every other
// mutation — a same-identity retry replays, a reconfigured asset executes
// fresh, and a failed create keeps its residual contract.

func TestTerminalCreateSameIdentityRetryReplays(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	sequence := &identitySequenceResolver{values: []AssetIdentity{identityA, identityA, identityA, identityA}}
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	registry := p1Registry(fake)
	registry.deps.Outcome = ledger
	registry.deps.ResolveAssetSessionIdentity = func(_ context.Context, _ string) (string, AssetIdentity, error) {
		return "s1", sequence.next(), nil
	}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1"}`)
	call.AuthorizationID = "guard:test"

	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK || first.Terminal == nil {
		t.Fatalf("first create = %+v", first)
	}
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if !strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("same-identity create retry did not replay: %+v", second)
	}
	if len(fake.created) != 1 {
		t.Fatalf("same-identity create retry re-created: %d tabs", len(fake.created))
	}
}

func TestTerminalCreateReconfiguredAssetExecutesFresh(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	sequence := &identitySequenceResolver{values: []AssetIdentity{identityA, identityA, identityB, identityB}}
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	registry := p1Registry(fake)
	registry.deps.Outcome = ledger
	registry.deps.ResolveAssetSessionIdentity = func(_ context.Context, _ string) (string, AssetIdentity, error) {
		return "s1", sequence.next(), nil
	}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1"}`)
	call.AuthorizationID = "guard:test"

	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK {
		t.Fatalf("first create = %+v", first)
	}
	fake.setIdentity(identityB)
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if !second.OK || second.Terminal == nil {
		t.Fatalf("reconfigured asset create = %+v", second)
	}
	if strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("reconfigured asset create replayed the old result: %+v", second)
	}
	if len(fake.created) != 2 {
		t.Fatalf("reconfigured asset create did not execute fresh: %d tabs", len(fake.created))
	}
}

func TestTerminalCreateFailureKeepsResidualAndOperationID(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	fake.createErr = errors.New("shell 启动失败")
	registry := p1Registry(fake)
	registry.deps.Outcome = ledger
	registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) {
		return "", ErrNoLiveSession
	}
	registry.deps.AssetConnector = &residualConnector{sessionID: "s9"}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	result := registry.Execute(context.Background(), "job", scope, call, nil)
	if result.OK || !strings.Contains(result.Text, "残留资源") || !strings.Contains(result.Text, "session_id=s9") {
		t.Fatalf("failed connected create = %+v", result)
	}
	if result.OperationID == "" {
		t.Fatalf("failed connected create lost the operation ID: %+v", result)
	}
}

// R12-F1: a configured resolver that fails pre-resolution must fail visibly —
// never write or replay an identity-less record.

func TestExecCommandsPreResolutionFailureDoesNotReplayAcrossIdentities(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	sequence := &identitySequenceResolver{values: []AssetIdentity{identityB, identityB}}
	failing := &failingIdentityResolver{sequence: sequence, failFirst: 2}
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	registry := p1Registry(fake)
	registry.deps.Outcome = ledger
	registry.deps.ResolveExecTargetIdentities = failing.resolve
	batchCalls := 0
	registry.deps.ExecBatch = func(_ context.Context, _ execbatch.Request) (execbatch.Batch, error) {
		batchCalls++
		return execbatch.Batch{Results: []execbatch.Result{{SessionID: "s1", AssetID: "a1", Status: execbatch.StatusSuccess, Stdout: "ok"}}}, nil
	}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "exec_commands", `{"commands":["ls"],"asset_ids":["a1"]}`)
	call.AuthorizationID = "guard:test"

	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if first.OK || !strings.Contains(first.Text, "身份解析失败") {
		t.Fatalf("pre-resolution failure = %+v", first)
	}
	if batchCalls != 0 {
		t.Fatalf("pre-resolution failure executed the batch: %d calls", batchCalls)
	}
	fake.setIdentity(identityB)
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if second.OK || !strings.Contains(second.Text, "身份解析失败") {
		t.Fatalf("pre-resolution failure on retry = %+v", second)
	}
	if strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("identity-less replay across reconfiguration: %+v", second)
	}
	if batchCalls != 0 {
		t.Fatalf("identity-less replay executed the batch: %d calls", batchCalls)
	}

	// Once the resolver recovers, the retry executes fresh on the current
	// asset instead of replaying anything recorded under an identity-less key.
	third := registry.Execute(context.Background(), "job", scope, call, nil)
	if !third.OK {
		t.Fatalf("recovered pre-resolution = %+v", third)
	}
	if strings.Contains(third.Text, "调用已执行过") {
		t.Fatalf("recovered retry replayed an identity-less record: %+v", third)
	}
	if batchCalls != 1 {
		t.Fatalf("recovered retry did not execute fresh: %d calls", batchCalls)
	}
}

// failingIdentityResolver fails the first N calls, then delegates.
type failingIdentityResolver struct {
	mu        sync.Mutex
	sequence  *identitySequenceResolver
	failFirst int
	calls     int
}

func (f *failingIdentityResolver) resolve(_ context.Context, _, _ []string) (ExecTargetSet, []AssetIdentity, error) {
	f.mu.Lock()
	f.calls++
	fail := f.calls <= f.failFirst
	f.mu.Unlock()
	if fail {
		return ExecTargetSet{}, nil, errors.New("identity backend unavailable")
	}
	return ExecTargetSet{SessionIDs: []string{"s1"}}, []AssetIdentity{f.sequence.next()}, nil
}
