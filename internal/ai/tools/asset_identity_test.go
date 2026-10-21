package tools

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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

// The registry composes the identity-aware resolvers production implies: a
// terminal with the identity seam plus the plain session resolvers binds the
// live session's bound asset identity instead of marking the operation
// unverifiable, and the binding comes from the same source the
// execution-time re-validation reads.
func TestComposeAssetIdentityResolverBindsLiveSessionIdentity(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	fake.bindSessionAsset("s9", identityA)
	deps := Dependencies{Terminal: fake, PollInterval: time.Millisecond}
	deps.ResolveAssetSession = func(context.Context, string) (string, error) { return "s9", nil }
	registry := NewRegistry(deps)

	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("c", "terminal_create", `{"asset_id":"a1"}`), nil)
	if !result.OK {
		t.Fatalf("create = %+v", result)
	}
	if strings.Contains(result.Text, "资产身份不可验证") || strings.Contains(result.Notes, "资产身份不可验证") {
		t.Fatalf("composed resolver still marked unverifiable: %+v", result)
	}
	if len(fake.created) != 1 {
		t.Fatalf("created = %+v", fake.created)
	}
	if len(result.OperationID) == 0 {
		t.Fatalf("recorded create lost its operation ID: %+v", result)
	}
}

// The composed resolver re-validates at execution time from the live
// session: a session rebound to another asset between the binding and the
// execution fails closed instead of receiving the mutation.
func TestComposeAssetIdentityResolverExecutionRevalidation(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	fake.bindSessionAsset("s9", identityA)
	deps := Dependencies{Terminal: fake, PollInterval: time.Millisecond}
	resolved := "s9"
	deps.ResolveAssetSession = func(context.Context, string) (string, error) { return resolved, nil }
	registry := NewRegistry(deps)
	if registry.deps.ResolveAssetSessionIdentity == nil {
		t.Fatal("identity-aware resolver not composed")
	}
	sessionID, identity, err := registry.deps.ResolveAssetSessionIdentity(context.Background(), "a1")
	if err != nil || sessionID != "s9" || identity != identityA {
		t.Fatalf("composed resolution = %s %+v err=%v", sessionID, identity, err)
	}
	// A session without an identity source resolves to the honest
	// unverifiable verdict instead of an invented identity.
	resolved = "s-gone"
	if _, _, err := registry.deps.ResolveAssetSessionIdentity(context.Background(), "a1"); !errors.Is(err, ErrAssetIdentityUnverifiable) {
		t.Fatalf("unevidenced session resolution = %v, want ErrAssetIdentityUnverifiable", err)
	}

	resolverErr := errors.New("resolver down")
	deps.ResolveAssetSession = func(context.Context, string) (string, error) { return "", resolverErr }
	registry = NewRegistry(deps)
	if _, _, err := registry.deps.ResolveAssetSessionIdentity(context.Background(), "a1"); !errors.Is(err, resolverErr) {
		t.Fatalf("resolver error not propagated: %v", err)
	}
}

// Without the identity seam on the terminal the honest unverifiable path is
// preserved, and an explicitly configured resolver always wins over the
// composition.
func TestComposeAssetIdentityResolverOptOutAndExplicitWin(t *testing.T) {
	plain := &fakeTerminal{}
	deps := Dependencies{Terminal: plain, PollInterval: time.Millisecond}
	deps.ResolveAssetSession = func(context.Context, string) (string, error) { return "s9", nil }
	registry := NewRegistry(deps)
	if registry.deps.ResolveAssetSessionIdentity != nil {
		t.Fatal("terminal without the identity seam still composed a resolver")
	}

	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	explicit := func(context.Context, string) (string, AssetIdentity, error) { return "s-explicit", identityB, nil }
	deps = Dependencies{Terminal: fake, PollInterval: time.Millisecond}
	deps.ResolveAssetSession = func(context.Context, string) (string, error) { return "s9", nil }
	deps.ResolveAssetSessionIdentity = explicit
	registry = NewRegistry(deps)
	sessionID, identity, err := registry.deps.ResolveAssetSessionIdentity(context.Background(), "a1")
	if err != nil || sessionID != "s-explicit" || identity != identityB {
		t.Fatalf("explicit resolver lost to the composition: %s %+v err=%v", sessionID, identity, err)
	}
}

// The batch variant composes the same way: exec_commands against resolved
// targets binds each session's live identity instead of marking the batch
// unverifiable.
func TestComposeExecTargetIdentitiesBindsLiveSessionIdentity(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	fake.bindSessionAsset("s9", identityA)
	deps := Dependencies{Terminal: fake, PollInterval: time.Millisecond}
	deps.ResolveExecTargets = func(_ context.Context, _, _ []string) (ExecTargetSet, error) {
		return ExecTargetSet{SessionIDs: []string{"s9"}}, nil
	}
	deps.ExecBatch = func(_ context.Context, _ execbatch.Request) (execbatch.Batch, error) {
		return execbatch.Batch{Results: []execbatch.Result{{SessionID: "s9", AssetID: "a1", Status: execbatch.StatusSuccess, Stdout: "ok"}}}, nil
	}
	registry := NewRegistry(deps)
	if registry.deps.ResolveExecTargetIdentities == nil {
		t.Fatal("batch identity resolver not composed")
	}
	targets, identities, err := registry.deps.ResolveExecTargetIdentities(context.Background(), []string{"a1"}, nil)
	if err != nil || len(targets.SessionIDs) != 1 || len(identities) != 1 || identities[0] != identityA {
		t.Fatalf("composed batch resolution = %+v %+v err=%v", targets, identities, err)
	}
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("c", "exec_commands", `{"commands":["ls"],"asset_ids":["a1"]}`), nil)
	if !result.OK {
		t.Fatalf("exec = %+v", result)
	}
	if strings.Contains(result.Text, "资产身份不可验证") || strings.Contains(result.Notes, "资产身份不可验证") {
		t.Fatalf("composed batch still marked unverifiable: %+v", result)
	}
}

// A target whose identity genuinely cannot be evidenced (a session without
// an identity source, mirroring device or inline sessions) keeps the visibly
// unverifiable path: the operation succeeds with the mark instead of
// failing, the same contract as a registry without the identity resolver.
func TestComposeAssetIdentityUnverifiableDegradesToMarkedOperation(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	// The device shape: the session names its asset but has no config
	// fingerprint, so the identity is unevidenced — never a replacement.
	fake.bindSessionAsset("s-device", AssetIdentity{AssetID: "device/d1"})
	deps := Dependencies{Terminal: fake, PollInterval: time.Millisecond}
	deps.ResolveAssetSession = func(context.Context, string) (string, error) { return "s-device", nil }
	registry := NewRegistry(deps)
	if _, _, err := registry.deps.ResolveAssetSessionIdentity(context.Background(), "device/d1"); !errors.Is(err, ErrAssetIdentityUnverifiable) {
		t.Fatalf("device-like resolution = %v", err)
	}
	result := registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("c", "terminal_create", `{"asset_id":"device/d1"}`), nil)
	if !result.OK {
		t.Fatalf("device-like create failed: %+v", result)
	}
	if !strings.Contains(result.Text, "资产身份不可验证") || !strings.Contains(result.Notes, "资产身份不可验证") {
		t.Fatalf("unverifiable identity not marked: %+v", result)
	}
	if len(fake.created) != 1 {
		t.Fatalf("created = %+v", fake.created)
	}

	batch := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	batch.bindSessionAsset("s-device", AssetIdentity{AssetID: "device/d1"})
	deps = Dependencies{Terminal: batch, PollInterval: time.Millisecond}
	deps.ResolveExecTargets = func(_ context.Context, _, _ []string) (ExecTargetSet, error) {
		return ExecTargetSet{SessionIDs: []string{"s-device"}}, nil
	}
	deps.ExecBatch = func(_ context.Context, _ execbatch.Request) (execbatch.Batch, error) {
		return execbatch.Batch{Results: []execbatch.Result{{SessionID: "s-device", AssetID: "device/d1", Status: execbatch.StatusSuccess, Stdout: "ok"}}}, nil
	}
	registry = NewRegistry(deps)
	result = registry.Execute(context.Background(), "job", Scope{SessionID: "s1", TabID: "t1"},
		p1Call("c", "exec_commands", `{"commands":["ls"],"asset_ids":["device/d1"]}`), nil)
	if !result.OK {
		t.Fatalf("device-like exec failed: %+v", result)
	}
	if !strings.Contains(result.Text, "资产身份不可验证") || !strings.Contains(result.Notes, "资产身份不可验证") {
		t.Fatalf("unverifiable batch not marked: %+v", result)
	}
}

// An operation recorded against a specific identity never lands on an
// unverifiable session: the bound identity still requires an exact match
// even when the current resolution cannot evidence one.
func TestValidateExecutionAssetIdentityBoundRequiresExactMatch(t *testing.T) {
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	fake.bindSessionAsset("s-device", AssetIdentity{AssetID: "device/d1"})
	registry := p1Registry(fake)
	failure, err := registry.validateExecutionAssetIdentity(context.Background(), "s-device", AssetIdentity{}, []AssetIdentity{identityA})
	if err == nil {
		t.Fatalf("bound operation executed on an unverifiable session: %+v", failure)
	}
	// Both sides unverifiable proceed; a complete resolved identity still
	// re-validates against the session's bound identity.
	if failure, err := registry.validateExecutionAssetIdentity(context.Background(), "s-device", AssetIdentity{}, nil); err != nil {
		t.Fatalf("unverifiable pair failed: %+v err=%v", failure, err)
	}
	fake.bindSessionAsset("s9", identityA)
	if failure, err := registry.validateExecutionAssetIdentity(context.Background(), "s9", identityA, []AssetIdentity{identityA}); err != nil {
		t.Fatalf("matching identity failed: %+v err=%v", failure, err)
	}
	if failure, err := registry.validateExecutionAssetIdentity(context.Background(), "s9", identityB, nil); !errors.Is(err, ErrAssetIdentityChanged) {
		t.Fatalf("replaced asset not rejected: %+v err=%v", failure, err)
	}
}
