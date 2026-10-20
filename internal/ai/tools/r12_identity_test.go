package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
)

// R12-F2: every batch target must carry exactly one non-empty identity, and
// the identity set must match the key-bound set as a multiset — a missing,
// extra, duplicate, or empty identity fails closed with zero execution.

// identitySetTerminal reports per-session identities from a fixed map.
type identitySetTerminal struct {
	*tabFakeTerminal
	identities map[string]AssetIdentity
}

func (f *identitySetTerminal) SessionAssetIdentity(_ context.Context, sessionID string) (AssetIdentity, error) {
	if identity, ok := f.identities[sessionID]; ok {
		return identity, nil
	}
	return AssetIdentity{}, nil
}

func batchIdentityRegistry(t *testing.T, sessionIDs []string, identities []AssetIdentity, bound []AssetIdentity) (*Registry, *int) {
	t.Helper()
	fake := &tabFakeTerminal{tabs: make(map[string]*tabFakeTab)}
	for _, sessionID := range sessionIDs {
		fake.tabs[sessionID] = fakeTab(sessionID, sessionID, "$ ", 0)
	}
	registry := p1Registry(fake)
	registry.deps.ResolveExecTargetIdentities = func(_ context.Context, _, _ []string) (ExecTargetSet, []AssetIdentity, error) {
		return ExecTargetSet{SessionIDs: sessionIDs}, identities, nil
	}
	calls := 0
	registry.deps.ExecBatch = func(_ context.Context, _ execbatch.Request) (execbatch.Batch, error) {
		calls++
		return execbatch.Batch{Results: []execbatch.Result{{SessionID: "s1", AssetID: "a1", Status: execbatch.StatusSuccess, Stdout: "ok"}}}, nil
	}
	bySession := make(map[string]AssetIdentity, len(sessionIDs))
	for index, sessionID := range sessionIDs {
		if index < len(identities) {
			bySession[sessionID] = identities[index]
		}
	}
	registry.deps.Terminal = &identitySetTerminal{tabFakeTerminal: fake, identities: bySession}
	return registry, &calls
}

func TestExecCommandsRequiresIdentityForEveryTarget(t *testing.T) {
	for name, test := range map[string]struct {
		sessionIDs []string
		identities []AssetIdentity
		bound      []AssetIdentity
		want       string
	}{
		"missing":    {sessionIDs: []string{"s1", "s2"}, identities: []AssetIdentity{identityA}, bound: []AssetIdentity{identityA, identityB}, want: "数量不一致"},
		"extra":      {sessionIDs: []string{"s1"}, identities: []AssetIdentity{identityA, identityB}, bound: []AssetIdentity{identityA}, want: "数量不一致"},
		"duplicate":  {sessionIDs: []string{"s1", "s2"}, identities: []AssetIdentity{identityA, identityA}, bound: []AssetIdentity{identityA, identityB}, want: "被替换"},
		"empty":      {sessionIDs: []string{"s1"}, identities: []AssetIdentity{{}}, bound: []AssetIdentity{{}}, want: "身份为空"},
		"order":      {sessionIDs: []string{"s1", "s2"}, identities: []AssetIdentity{identityB, identityA}, bound: []AssetIdentity{identityA, identityB}, want: "被替换"},
		"multisetOK": {sessionIDs: []string{"s1", "s2"}, identities: []AssetIdentity{identityA, identityB}, bound: []AssetIdentity{identityB, identityA}, want: "被替换"},
		"match":      {sessionIDs: []string{"s1", "s2"}, identities: []AssetIdentity{identityA, identityB}, bound: []AssetIdentity{identityA, identityB}, want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			registry, calls := batchIdentityRegistry(t, test.sessionIDs, test.identities, test.bound)
			scope := Scope{SessionID: "s1", TabID: "t1"}
			call := p1Call("c", "exec_commands", `{"commands":["ls"],"asset_ids":["a1","a2"]}`)
			call.AssetIdentities = test.bound
			result := registry.Execute(context.Background(), "job", scope, call, nil)
			if test.want == "" {
				if !result.OK {
					t.Fatalf("matching identities rejected: %+v", result)
				}
				if *calls != 1 {
					t.Fatalf("matching identities did not execute: %d", *calls)
				}
				return
			}
			if result.OK || !strings.Contains(result.Text, test.want) {
				t.Fatalf("%s: got %+v, want %q", name, result, test.want)
			}
			if *calls != 0 {
				t.Fatalf("%s executed the batch: %d calls", name, *calls)
			}
		})
	}
}

// R12-F4: a connected session must name the requested asset before the
// create proceeds — a connector that attached a different asset fails closed
// with the residual session and operation ID visible.
func TestTerminalCreateRejectsConnectedSessionForDifferentAsset(t *testing.T) {
	registry := p1Registry(newTabFake(fakeTab("t1", "s1", "$ ", 0)))
	registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) {
		return "", ErrNoLiveSession
	}
	registry.deps.AssetConnector = &residualConnector{sessionID: "s9"}
	registry.deps.Terminal = &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	registry.deps.Terminal.(*identityFakeTerminal).setIdentity(AssetIdentity{AssetID: "a2", ConfigFingerprint: "fp-a2"})
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)

	result := registry.Execute(context.Background(), "job", scope, call, nil)
	if result.OK || !strings.Contains(result.Text, "a2") || !strings.Contains(result.Text, "a1") {
		t.Fatalf("connected session for a different asset = %+v", result)
	}
	if !strings.Contains(result.Text, "残留资源") || !strings.Contains(result.Text, "session_id=s9") {
		t.Fatalf("residual session not visible: %+v", result)
	}
	if result.OperationID == "" {
		t.Fatalf("operation ID lost: %+v", result)
	}
	created := registry.deps.Terminal.(*identityFakeTerminal)
	if len(created.created) != 0 {
		t.Fatalf("create executed on the wrong asset: %+v", created.created)
	}
}

// The connected identity is bound into the operation: a same-identity retry
// replays, and the connected session's own identity is what gets validated.
func TestTerminalCreateConnectedIdentityBoundIntoOperation(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	sequence := &identitySequenceResolver{values: []AssetIdentity{identityB, identityB, identityB, identityB}}
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityB)
	registry := p1Registry(fake)
	registry.deps.Outcome = ledger
	registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) {
		return "", ErrNoLiveSession
	}
	registry.deps.ResolveAssetSessionIdentity = func(_ context.Context, _ string) (string, AssetIdentity, error) {
		return "s9", sequence.next(), nil
	}
	registry.deps.AssetConnector = &residualConnector{sessionID: "s9"}
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK {
		t.Fatalf("connected create = %+v", first)
	}
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if !strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("connected create retry did not replay: %+v", second)
	}
	if len(fake.created) != 1 {
		t.Fatalf("connected create retry re-created: %d tabs", len(fake.created))
	}
}
