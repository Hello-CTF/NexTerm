package tools

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// Identity-only registries (the identity-aware resolver configured without
// the plain one, a shape both tool guards explicitly allow) must degrade the
// unverifiable verdict honestly: the identity resolver's own resolved
// session/targets carry the operation with the unverifiable mark, and a
// resolver that cannot resolve produces a visible error — never a nil-plain
// panic, never a wrong execution.

func TestIdentityOnlyUnverifiableTerminalCreateUsesResolvedSession(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	deps := Dependencies{Terminal: fake, PollInterval: time.Millisecond}
	deps.ResolveAssetSessionIdentity = func(context.Context, string) (string, AssetIdentity, error) {
		// The identity cannot be evidenced, but the session itself resolved.
		return "s-device", AssetIdentity{}, ErrAssetIdentityUnverifiable
	}
	registry := NewRegistry(deps)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("c", "terminal_create", `{"asset_id":"device/d1"}`), nil)
	if result.Panic {
		t.Fatalf("identity-only unverifiable create panicked: %+v", result)
	}
	if !result.OK {
		t.Fatalf("identity-only unverifiable create failed: %+v", result)
	}
	if !strings.Contains(result.Text, "资产身份不可验证") || !strings.Contains(result.Notes, "资产身份不可验证") {
		t.Fatalf("unverifiable identity not marked: %+v", result)
	}
	if len(fake.created) != 1 {
		t.Fatalf("created = %+v", fake.created)
	}
}

func TestIdentityOnlyUnverifiableTerminalCreateUnresolvedVisibleError(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	deps := Dependencies{Terminal: fake, PollInterval: time.Millisecond}
	deps.ResolveAssetSessionIdentity = func(context.Context, string) (string, AssetIdentity, error) {
		// Unverifiable and no session resolved either.
		return "", AssetIdentity{}, ErrAssetIdentityUnverifiable
	}
	registry := NewRegistry(deps)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("c", "terminal_create", `{"asset_id":"device/d1"}`), nil)
	if result.Panic {
		t.Fatalf("identity-only unresolved create panicked: %+v", result)
	}
	if result.OK {
		t.Fatalf("unresolvable create succeeded: %+v", result)
	}
	if len(fake.created) != 0 {
		t.Fatalf("unresolvable create produced a tab: %+v", fake.created)
	}
}

func TestIdentityOnlyUnverifiableExecBatchUsesResolvedTargets(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	var batches atomic.Int64
	deps := Dependencies{Terminal: fake, PollInterval: time.Millisecond}
	deps.ResolveExecTargetIdentities = func(context.Context, []string, []string) (ExecTargetSet, []AssetIdentity, error) {
		return ExecTargetSet{SessionIDs: []string{"s-device"}}, nil, ErrAssetIdentityUnverifiable
	}
	deps.ExecBatch = func(_ context.Context, _ execbatch.Request) (execbatch.Batch, error) {
		batches.Add(1)
		return execbatch.Batch{Results: []execbatch.Result{{SessionID: "s-device", AssetID: "device/d1", Status: execbatch.StatusSuccess, Stdout: "ok"}}}, nil
	}
	registry := NewRegistry(deps)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("c", "exec_commands", `{"commands":["ls"],"asset_ids":["device/d1"]}`), nil)
	if result.Panic {
		t.Fatalf("identity-only unverifiable exec panicked: %+v", result)
	}
	if !result.OK {
		t.Fatalf("identity-only unverifiable exec failed: %+v", result)
	}
	if !strings.Contains(result.Text, "资产身份不可验证") || !strings.Contains(result.Notes, "资产身份不可验证") {
		t.Fatalf("unverifiable batch not marked: %+v", result)
	}
	if batches.Load() != 1 {
		t.Fatalf("batches = %d", batches.Load())
	}
}

func TestIdentityOnlyUnverifiableExecBatchUnresolvedVisibleError(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	var batches atomic.Int64
	deps := Dependencies{Terminal: fake, PollInterval: time.Millisecond}
	deps.ResolveExecTargetIdentities = func(context.Context, []string, []string) (ExecTargetSet, []AssetIdentity, error) {
		return ExecTargetSet{}, nil, ErrAssetIdentityUnverifiable
	}
	deps.ExecBatch = func(_ context.Context, _ execbatch.Request) (execbatch.Batch, error) {
		batches.Add(1)
		return execbatch.Batch{}, nil
	}
	registry := NewRegistry(deps)
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("c", "exec_commands", `{"commands":["ls"],"asset_ids":["device/d1"]}`), nil)
	if result.Panic {
		t.Fatalf("identity-only unresolved exec panicked: %+v", result)
	}
	if result.OK {
		t.Fatalf("unresolvable exec succeeded: %+v", result)
	}
	if batches.Load() != 0 {
		t.Fatalf("unresolvable exec ran %d batches", batches.Load())
	}
}

// A call that already binds asset identities must never degrade on the
// unverifiable verdict: the bound identity requires an exact match, so the
// retry fails closed instead of executing on or recording against the
// unverifiable target, and the old recorded outcome is never replayed as
// this call's fresh result.
func TestBoundExecUnverifiableFailsClosedWithoutReplay(t *testing.T) {
	_, idempotency, _ := outcomeFixture(t)
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	fake.bindSessionAsset("s9", identityA)
	var batches atomic.Int64
	unverifiable := false
	deps := Dependencies{Terminal: fake, PollInterval: time.Millisecond, Outcome: idempotency}
	deps.ResolveExecTargetIdentities = func(context.Context, []string, []string) (ExecTargetSet, []AssetIdentity, error) {
		if unverifiable {
			return ExecTargetSet{SessionIDs: []string{"s9"}}, nil, ErrAssetIdentityUnverifiable
		}
		return ExecTargetSet{SessionIDs: []string{"s9"}}, []AssetIdentity{identityA}, nil
	}
	deps.ResolveExecTargets = func(_ context.Context, _, _ []string) (ExecTargetSet, error) {
		return ExecTargetSet{SessionIDs: []string{"s9"}}, nil
	}
	deps.ExecBatch = func(_ context.Context, _ execbatch.Request) (execbatch.Batch, error) {
		batches.Add(1)
		return execbatch.Batch{Results: []execbatch.Result{{SessionID: "s9", AssetID: "a1", Status: execbatch.StatusSuccess, Stdout: "ok"}}}, nil
	}
	registry := NewRegistry(deps)
	call := p1Call("c", "exec_commands", `{"commands":["ls"],"asset_ids":["a1"]}`)
	call.AuthorizationID = "guard:test"
	first := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"}, call, nil)
	if !first.OK || batches.Load() != 1 {
		t.Fatalf("first bound exec = %+v batches=%d", first, batches.Load())
	}

	// The session's identity can no longer be evidenced; the retry carries
	// the previously bound identity and must fail closed.
	unverifiable = true
	call.AssetIdentities = []AssetIdentity{identityA}
	second := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"}, call, nil)
	if second.OK {
		t.Fatalf("bound+unverifiable retry executed: %+v", second)
	}
	if !strings.Contains(second.Text, "已变化") && !strings.Contains(second.Text, "冲突") {
		t.Fatalf("bound+unverifiable retry not a fail-closed verdict: %+v", second)
	}
	if strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("bound+unverifiable retry replayed the old record: %+v", second)
	}
	if batches.Load() != 1 {
		t.Fatalf("bound+unverifiable retry ran %d batches", batches.Load())
	}
}

func TestBoundTerminalCreateUnverifiableFailsClosedWithoutReplay(t *testing.T) {
	_, idempotency, _ := outcomeFixture(t)
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	fake.bindSessionAsset("s9", identityA)
	unverifiable := false
	deps := Dependencies{Terminal: fake, PollInterval: time.Millisecond, Outcome: idempotency}
	deps.ResolveAssetSessionIdentity = func(context.Context, string) (string, AssetIdentity, error) {
		if unverifiable {
			return "s9", AssetIdentity{}, ErrAssetIdentityUnverifiable
		}
		return "s9", identityA, nil
	}
	deps.ResolveAssetSession = func(context.Context, string) (string, error) { return "s9", nil }
	registry := NewRegistry(deps)
	call := p1Call("c", "terminal_create", `{"asset_id":"a1"}`)
	call.AuthorizationID = "guard:test"
	first := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"}, call, nil)
	if !first.OK || len(fake.created) != 1 {
		t.Fatalf("first bound create = %+v", first)
	}

	unverifiable = true
	call.AssetIdentities = []AssetIdentity{identityA}
	second := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"}, call, nil)
	if second.OK {
		t.Fatalf("bound+unverifiable create executed: %+v", second)
	}
	if strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("bound+unverifiable create replayed the old record: %+v", second)
	}
	if len(fake.created) != 1 {
		t.Fatalf("bound+unverifiable create produced another tab: %+v", fake.created)
	}
}

// The durable replay path short-circuits before the bind-time check: a
// finished durable record bound to a specific identity must never replay
// when the current target can no longer evidence an identity — the replay
// cannot certify the binding still holds, so it fails closed.
func TestDurableReplayBoundUnverifiableFailsClosed(t *testing.T) {
	_, idempotency, _ := outcomeFixture(t)
	database, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	durable := newR16DurableOutcomes(t, database)
	unverifiable := false
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	resolver := func(context.Context, string) (string, AssetIdentity, error) {
		if unverifiable {
			return "s9", AssetIdentity{}, ErrAssetIdentityUnverifiable
		}
		return "s9", identityA, nil
	}
	connector := &r13Connector{sessionIDs: []string{"s9"}}
	registry := r16Registry(t, fake, connector, resolver, idempotency, durable)
	call := p1Call("c", "terminal_create", `{"asset_id":"a1"}`)
	call.AuthorizationID = "guard:test"

	first := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"}, call, nil)
	if !first.OK || len(fake.created) != 1 {
		t.Fatalf("first bound create = %+v", first)
	}
	// The recorded attempt is bound to identityA; the target can no longer
	// evidence an identity, so the retry must not replay the old result.
	unverifiable = true
	second := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"}, call, nil)
	if second.OK {
		t.Fatalf("bound+unverifiable durable retry succeeded: %+v", second)
	}
	if strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("bound+unverifiable durable retry replayed the old record: %+v", second)
	}
	if len(fake.created) != 1 || connector.callCount() != 0 {
		t.Fatalf("bound+unverifiable durable retry re-executed: created=%d connector=%d", len(fake.created), connector.callCount())
	}
}
