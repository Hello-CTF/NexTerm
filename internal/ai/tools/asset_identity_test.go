package tools

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

// R11: mutation tools bind the operation to the resolved asset identity and
// re-validate it at execution time — a replaced asset never receives the
// mutation, the old result is never attributed to it, and only a same-
// identity retry replays.

// identitySequenceResolver returns per-call identities.
type identitySequenceResolver struct {
	mu     sync.Mutex
	calls  int
	values []AssetIdentity
}

func (s *identitySequenceResolver) next() AssetIdentity {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.calls
	s.calls++
	if index >= len(s.values) {
		return s.values[len(s.values)-1]
	}
	return s.values[index]
}

// identityFakeTerminal adds a controllable bound identity.
type identityFakeTerminal struct {
	*tabFakeTerminal
	mu       sync.Mutex
	identity AssetIdentity
}

func (f *identityFakeTerminal) SessionAssetIdentity(_ context.Context, _ string) (AssetIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.identity, nil
}

func (f *identityFakeTerminal) setIdentity(identity AssetIdentity) {
	f.mu.Lock()
	f.identity = identity
	f.mu.Unlock()
}

var (
	identityA = AssetIdentity{AssetID: "a1", ConfigFingerprint: "fp-v1"}
	identityB = AssetIdentity{AssetID: "a1", ConfigFingerprint: "fp-v2"}
)

func TestTerminalCreateAssetReplacedBetweenResolutionAndExecution(t *testing.T) {
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityB)
	registry := p1Registry(fake)
	registry.deps.ResolveAssetSessionIdentity = func(context.Context, string) (string, AssetIdentity, error) {
		return "s1", identityA, nil
	}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1"}`)
	call.AssetIdentities = []AssetIdentity{identityA}

	result := registry.Execute(context.Background(), "job", scope, call, nil)
	if result.OK || !strings.Contains(result.Text, "被替换") {
		t.Fatalf("replaced asset create = %+v", result)
	}
	if len(fake.created) != 0 {
		t.Fatalf("create executed on the replaced asset: %+v", fake.created)
	}
}

func TestExecCommandsAssetReplacedBetweenResolutionAndExecution(t *testing.T) {
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityB)
	registry := p1Registry(fake)
	registry.deps.ResolveExecTargetIdentities = func(_ context.Context, _, _ []string) (ExecTargetSet, []AssetIdentity, error) {
		return ExecTargetSet{SessionIDs: []string{"s1"}}, []AssetIdentity{identityA}, nil
	}
	registry.deps.ExecBatch = func(_ context.Context, request execbatch.Request) (execbatch.Batch, error) {
		t.Fatalf("batch executed on the replaced asset: %+v", request)
		return execbatch.Batch{}, nil
	}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "exec_commands", `{"commands":["ls"],"asset_ids":["a1"]}`)
	call.AssetIdentities = []AssetIdentity{identityA}

	result := registry.Execute(context.Background(), "job", scope, call, nil)
	if result.OK || !strings.Contains(result.Text, "被替换") {
		t.Fatalf("replaced asset exec = %+v", result)
	}
}

func TestExecCommandsReplacedAssetRetryExecutesFresh(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	sequence := &identitySequenceResolver{values: []AssetIdentity{identityA, identityA, identityB, identityB}}
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	registry := p1Registry(fake)
	registry.deps.Outcome = ledger
	registry.deps.ResolveExecTargetIdentities = func(_ context.Context, _, _ []string) (ExecTargetSet, []AssetIdentity, error) {
		return ExecTargetSet{SessionIDs: []string{"s1"}}, []AssetIdentity{sequence.next()}, nil
	}
	batchCalls := 0
	registry.deps.ExecBatch = func(_ context.Context, _ execbatch.Request) (execbatch.Batch, error) {
		batchCalls++
		return execbatch.Batch{Results: []execbatch.Result{{SessionID: "s1", AssetID: "a1", Status: execbatch.StatusSuccess, Stdout: "ok"}}}, nil
	}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "exec_commands", `{"commands":["ls"],"asset_ids":["a1"]}`)
	call.AuthorizationID = "guard:test"

	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK {
		t.Fatalf("first execution = %+v", first)
	}
	fake.setIdentity(identityB)
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if !second.OK {
		t.Fatalf("retry on replaced asset = %+v", second)
	}
	if strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("replaced asset retry replayed the old result: %+v", second)
	}
	if batchCalls != 2 {
		t.Fatalf("replaced asset retry skipped fresh execution: batch calls = %d", batchCalls)
	}
}

func TestExecCommandsSameIdentityRetryReplays(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	sequence := &identitySequenceResolver{values: []AssetIdentity{identityA, identityA, identityA, identityA}}
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	registry := p1Registry(fake)
	registry.deps.Outcome = ledger
	registry.deps.ResolveExecTargetIdentities = func(_ context.Context, _, _ []string) (ExecTargetSet, []AssetIdentity, error) {
		return ExecTargetSet{SessionIDs: []string{"s1"}}, []AssetIdentity{sequence.next()}, nil
	}
	batchCalls := 0
	registry.deps.ExecBatch = func(_ context.Context, _ execbatch.Request) (execbatch.Batch, error) {
		batchCalls++
		return execbatch.Batch{Results: []execbatch.Result{{SessionID: "s1", AssetID: "a1", Status: execbatch.StatusSuccess, Stdout: "ok"}}}, nil
	}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "exec_commands", `{"commands":["ls"],"asset_ids":["a1"]}`)
	call.AuthorizationID = "guard:test"

	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK {
		t.Fatalf("first execution = %+v", first)
	}
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if !strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("same-identity retry did not replay: %+v", second)
	}
	if batchCalls != 1 {
		t.Fatalf("same-identity retry re-executed: batch calls = %d", batchCalls)
	}
}

func TestTerminalCreateAssetIdentityUnverifiableMarked(t *testing.T) {
	registry := p1Registry(newTabFake(fakeTab("t1", "s1", "$ ", 0)))
	registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) {
		return "s1", nil
	}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	result := registry.Execute(context.Background(), "job", scope,
		p1Call("c", "terminal_create", `{"asset_id":"a1"}`), nil)
	if !result.OK {
		t.Fatalf("create = %+v", result)
	}
	if !strings.Contains(result.Text, "资产身份不可验证") || !strings.Contains(result.Notes, "资产身份不可验证") {
		t.Fatalf("unverifiable identity not marked: %+v", result)
	}
}

func TestExecCommandsAssetIdentityUnverifiableMarked(t *testing.T) {
	registry := p1Registry(newTabFake(fakeTab("t1", "s1", "$ ", 0)))
	registry.deps.ResolveExecTargets = func(_ context.Context, _, _ []string) (ExecTargetSet, error) {
		return ExecTargetSet{SessionIDs: []string{"s1"}}, nil
	}
	registry.deps.ExecBatch = func(_ context.Context, _ execbatch.Request) (execbatch.Batch, error) {
		return execbatch.Batch{Results: []execbatch.Result{{SessionID: "s1", AssetID: "a1", Status: execbatch.StatusSuccess, Stdout: "ok"}}}, nil
	}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	result := registry.Execute(context.Background(), "job", scope,
		p1Call("c", "exec_commands", `{"commands":["ls"],"asset_ids":["a1"]}`), nil)
	if !result.OK {
		t.Fatalf("exec = %+v", result)
	}
	if !strings.Contains(result.Text, "资产身份不可验证") || !strings.Contains(result.Notes, "资产身份不可验证") {
		t.Fatalf("unverifiable identity not marked: %+v", result)
	}
}
