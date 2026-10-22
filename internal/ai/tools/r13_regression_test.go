package tools

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/outcome"
)

// R13-F1: a configured resolver that returns an empty or invalid identity
// set must fail the call before any ledger reserve/replay — an identity-less
// record is never written, so a later retry can neither execute under it nor
// replay it across a reconfiguration. Pre-resolution and execution must
// share the same normalized (trimmed, deduplicated) target filters.
//
// R13-F2: the pre-resolved and execution-resolved identity sequences are
// validated independently — exact cardinality, complete identities, no
// duplicates, per-index agreement, and an independent multiset comparison —
// before any checker or ExecBatch runs.
//
// R13-F3/F4: terminal_create with connect=true establishes and verifies the
// session BEFORE the outcome key is computed; the connected identity must be
// complete (asset ID plus config fingerprint) and match any pre-bound
// identity, and the legacy no-resolver connect path binds the connected
// identity too.

// r13Connector connects per-call session IDs and runs a hook after each
// successful connect, modeling the production post-connect resolution.
type r13Connector struct {
	mu         sync.Mutex
	calls      int
	sessionIDs []string
	err        error
	onConnect  func(sessionID string)
}

func (c *r13Connector) ConnectAsset(context.Context, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	index := c.calls
	c.calls++
	if c.err != nil {
		return "", c.err
	}
	if index >= len(c.sessionIDs) {
		index = len(c.sessionIDs) - 1
	}
	sessionID := c.sessionIDs[index]
	if c.onConnect != nil {
		c.onConnect(sessionID)
	}
	return sessionID, nil
}

func (c *r13Connector) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// r13ExecResult is one programmed target-set resolution.
type r13ExecResult struct {
	sessions   []string
	identities []AssetIdentity
	err        error
}

// r13ExecResolver returns programmed target sets per call and records the
// filters each call received, so tests can pin the pre-resolution and
// execution call shapes.
type r13ExecResolver struct {
	mu      sync.Mutex
	calls   int
	inputs  [][2][]string
	results []r13ExecResult
}

func (r *r13ExecResolver) resolve(_ context.Context, assetIDs, tags []string) (ExecTargetSet, []AssetIdentity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputs = append(r.inputs, [2][]string{assetIDs, tags})
	index := r.calls
	r.calls++
	if index >= len(r.results) {
		index = len(r.results) - 1
	}
	result := r.results[index]
	return ExecTargetSet{SessionIDs: result.sessions}, result.identities, result.err
}

// identityErrorTerminal fails every session identity read.
type identityErrorTerminal struct {
	*tabFakeTerminal
	err error
}

func (f *identityErrorTerminal) SessionAssetIdentity(context.Context, string) (AssetIdentity, error) {
	return AssetIdentity{}, f.err
}

// r13BatchRegistry builds a recorded exec_commands registry with a counting
// ExecBatch and a per-session identity checker.
func r13BatchRegistry(t *testing.T, ledger *outcome.Ledger, resolver *r13ExecResolver, bySession map[string]AssetIdentity, batchCalls *int) *Registry {
	t.Helper()
	fake := newTabFake(fakeTab("t1", "s1", "$ ", 0))
	registry := p1Registry(&identitySetTerminal{tabFakeTerminal: fake, identities: bySession})
	registry.deps.Outcome = ledger
	registry.deps.ResolveExecTargetIdentities = resolver.resolve
	registry.deps.ExecBatch = func(_ context.Context, _ execbatch.Request) (execbatch.Batch, error) {
		*batchCalls++
		batch := execbatch.Batch{}
		for sessionID, identity := range bySession {
			batch.Results = append(batch.Results, execbatch.Result{SessionID: sessionID, AssetID: identity.AssetID, Status: execbatch.StatusSuccess, Stdout: "ok"})
		}
		return batch, nil
	}
	return registry
}

func TestExecCommandsEmptyPreResolutionFailsBeforeLedger(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	resolver := &r13ExecResolver{results: []r13ExecResult{
		{}, // first pre-resolution: empty everything
		{}, // retry pre-resolution: still empty after reconfiguration
		{sessions: []string{"s1"}, identities: []AssetIdentity{identityB}}, // recovered on the new configuration
	}}
	batchCalls := 0
	registry := r13BatchRegistry(t, ledger, resolver, map[string]AssetIdentity{"s1": identityB}, &batchCalls)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "exec_commands", `{"commands":["ls"],"asset_ids":["a1"]}`)
	call.AuthorizationID = "guard:test"

	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if first.OK || !strings.Contains(first.Text, "身份解析失败") {
		t.Fatalf("empty pre-resolution = %+v", first)
	}
	if batchCalls != 0 {
		t.Fatalf("empty pre-resolution executed the batch: %d", batchCalls)
	}
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if second.OK || !strings.Contains(second.Text, "身份解析失败") {
		t.Fatalf("empty pre-resolution retry = %+v", second)
	}
	if strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("identity-less record replayed across reconfiguration: %+v", second)
	}
	if batchCalls != 0 {
		t.Fatalf("identity-less replay executed the batch: %d", batchCalls)
	}
	third := registry.Execute(context.Background(), "job", scope, call, nil)
	if !third.OK {
		t.Fatalf("recovered pre-resolution = %+v", third)
	}
	if strings.Contains(third.Text, "调用已执行过") {
		t.Fatalf("recovered retry replayed an identity-less record: %+v", third)
	}
	if batchCalls != 1 {
		t.Fatalf("recovered retry did not execute fresh: %d", batchCalls)
	}
}

func TestExecCommandsPreResolutionSharesNormalizedFilters(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	resolver := &r13ExecResolver{results: []r13ExecResult{
		{sessions: []string{"s1", "s2"}, identities: []AssetIdentity{identityA, identityB}},
	}}
	batchCalls := 0
	registry := r13BatchRegistry(t, ledger, resolver, map[string]AssetIdentity{"s1": identityA, "s2": identityB}, &batchCalls)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "exec_commands", `{"commands":["ls"],"asset_ids":[" a1 ","a1","a2 "]}`)
	call.AuthorizationID = "guard:test"

	result := registry.Execute(context.Background(), "job", scope, call, nil)
	if !result.OK {
		t.Fatalf("normalized filters rejected: %+v", result)
	}
	if batchCalls != 1 {
		t.Fatalf("normalized filters did not execute: %d", batchCalls)
	}
	if len(resolver.inputs) != 2 {
		t.Fatalf("resolver calls = %d, want pre-resolution and execution", len(resolver.inputs))
	}
	for index, input := range resolver.inputs {
		if !reflect.DeepEqual(input[0], []string{"a1", "a2"}) {
			t.Fatalf("resolver call %d asset filters = %v, want [a1 a2]", index, input[0])
		}
	}
}

func TestExecCommandsDuplicateTargetIdentitiesFailClosed(t *testing.T) {
	t.Run("matching duplicate across both resolutions", func(t *testing.T) {
		_, ledger, _ := outcomeFixture(t)
		resolver := &r13ExecResolver{results: []r13ExecResult{
			{sessions: []string{"s1", "s2"}, identities: []AssetIdentity{identityA, identityA}},
		}}
		batchCalls := 0
		registry := r13BatchRegistry(t, ledger, resolver, map[string]AssetIdentity{"s1": identityA, "s2": identityA}, &batchCalls)
		scope := Scope{SessionID: "s1", TabID: "t1"}
		call := p1Call("c", "exec_commands", `{"commands":["ls"],"asset_ids":["a1","a2"]}`)
		call.AuthorizationID = "guard:test"

		first := registry.Execute(context.Background(), "job", scope, call, nil)
		if first.OK || !strings.Contains(first.Text, "重复") {
			t.Fatalf("matching duplicate identities = %+v", first)
		}
		if batchCalls != 0 {
			t.Fatalf("matching duplicate identities executed the batch: %d", batchCalls)
		}
		second := registry.Execute(context.Background(), "job", scope, call, nil)
		if second.OK || !strings.Contains(second.Text, "重复") {
			t.Fatalf("matching duplicate identities retry = %+v", second)
		}
		if strings.Contains(second.Text, "调用已执行过") {
			t.Fatalf("duplicate-bound record replayed: %+v", second)
		}
		if batchCalls != 0 {
			t.Fatalf("duplicate-bound replay executed the batch: %d", batchCalls)
		}
	})

	t.Run("execution duplicate after valid pre-resolution", func(t *testing.T) {
		_, ledger, _ := outcomeFixture(t)
		resolver := &r13ExecResolver{results: []r13ExecResult{
			{sessions: []string{"s1", "s2"}, identities: []AssetIdentity{identityA, identityB}},
			{sessions: []string{"s1", "s2"}, identities: []AssetIdentity{identityA, identityA}},
		}}
		batchCalls := 0
		registry := r13BatchRegistry(t, ledger, resolver, map[string]AssetIdentity{"s1": identityA, "s2": identityA}, &batchCalls)
		scope := Scope{SessionID: "s1", TabID: "t1"}
		call := p1Call("c", "exec_commands", `{"commands":["ls"],"asset_ids":["a1","a2"]}`)
		call.AuthorizationID = "guard:test"

		result := registry.Execute(context.Background(), "job", scope, call, nil)
		if result.OK || !strings.Contains(result.Text, "重复") {
			t.Fatalf("execution duplicate identities = %+v", result)
		}
		if batchCalls != 0 {
			t.Fatalf("execution duplicate identities executed the batch: %d", batchCalls)
		}
	})
}

func TestExecCommandsBoundIdentityMismatchFailsClosed(t *testing.T) {
	for name, test := range map[string]struct {
		pre       r13ExecResult
		exec      r13ExecResult
		bySession map[string]AssetIdentity
		want      string
	}{
		"missing": {
			pre:       r13ExecResult{sessions: []string{"s1", "s2"}, identities: []AssetIdentity{identityA, identityB}},
			exec:      r13ExecResult{sessions: []string{"s1", "s2"}, identities: []AssetIdentity{identityA}},
			bySession: map[string]AssetIdentity{"s1": identityA, "s2": identityA},
			want:      "数量不一致",
		},
		"extra": {
			pre:       r13ExecResult{sessions: []string{"s1"}, identities: []AssetIdentity{identityA}},
			exec:      r13ExecResult{sessions: []string{"s1"}, identities: []AssetIdentity{identityA, identityB}},
			bySession: map[string]AssetIdentity{"s1": identityA},
			want:      "数量不一致",
		},
		"order": {
			pre:       r13ExecResult{sessions: []string{"s1", "s2"}, identities: []AssetIdentity{identityA, identityB}},
			exec:      r13ExecResult{sessions: []string{"s1", "s2"}, identities: []AssetIdentity{identityB, identityA}},
			bySession: map[string]AssetIdentity{"s1": identityB, "s2": identityA},
			want:      "被替换",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, ledger, _ := outcomeFixture(t)
			resolver := &r13ExecResolver{results: []r13ExecResult{test.pre, test.exec}}
			batchCalls := 0
			registry := r13BatchRegistry(t, ledger, resolver, test.bySession, &batchCalls)
			scope := Scope{SessionID: "s1", TabID: "t1"}
			call := p1Call("c", "exec_commands", `{"commands":["ls"],"asset_ids":["a1","a2"]}`)
			call.AuthorizationID = "guard:test"

			result := registry.Execute(context.Background(), "job", scope, call, nil)
			if result.OK || !strings.Contains(result.Text, test.want) {
				t.Fatalf("%s = %+v, want %q", name, result, test.want)
			}
			if batchCalls != 0 {
				t.Fatalf("%s executed the batch: %d", name, batchCalls)
			}
		})
	}
}

func TestTerminalCreateConnectedSessionReconfiguredFailsClosed(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	resolveCalls := 0
	registry := p1Registry(&identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))})
	fake := registry.deps.Terminal.(*identityFakeTerminal)
	fake.setIdentity(identityB)
	registry.deps.Outcome = ledger
	registry.deps.ResolveAssetSessionIdentity = func(context.Context, string) (string, AssetIdentity, error) {
		resolveCalls++
		switch {
		case resolveCalls == 1:
			return "s1", identityA, nil // pre-resolution: the old configuration is live
		case resolveCalls == 2:
			return "", AssetIdentity{}, ErrNoLiveSession // execution: the old session died
		default:
			return "s9", identityB, nil // follow-up: the new configuration is live
		}
	}
	connector := &r13Connector{sessionIDs: []string{"s9"}}
	registry.deps.AssetConnector = connector
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if first.OK {
		t.Fatalf("reconfigured connected session accepted under the old identity: %+v", first)
	}
	if !strings.Contains(first.Text, "session_id=s9") || !strings.Contains(first.Text, "残留") {
		t.Fatalf("residual session not visible: %+v", first)
	}
	if !strings.Contains(first.Text, "不一致") {
		t.Fatalf("identity mismatch not named: %+v", first)
	}
	if first.OperationID == "" {
		t.Fatalf("failure lost the operation ID: %+v", first)
	}
	if len(fake.created) != 0 {
		t.Fatalf("create executed on the reconfigured asset: %+v", fake.created)
	}
	if connector.callCount() != 1 {
		t.Fatalf("connector calls = %d, want 1", connector.callCount())
	}
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if !second.OK {
		t.Fatalf("follow-up create on the new configuration = %+v", second)
	}
	if strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("follow-up create replayed the old identity's record: %+v", second)
	}
	if len(fake.created) != 1 {
		t.Fatalf("follow-up create did not execute fresh: %+v", fake.created)
	}
}

func TestTerminalCreateConnectedSessionIdentityMustBeComplete(t *testing.T) {
	for name, test := range map[string]struct {
		identity AssetIdentity
		readErr  error
	}{
		"empty fingerprint": {identity: AssetIdentity{AssetID: "a1"}},
		"empty identity":    {},
		"read failure":      {readErr: errors.New("session vanished")},
	} {
		t.Run(name, func(t *testing.T) {
			_, ledger, _ := outcomeFixture(t)
			var terminal Terminal
			base := newTabFake(fakeTab("t1", "s1", "$ ", 0))
			if test.readErr != nil {
				terminal = &identityErrorTerminal{tabFakeTerminal: base, err: test.readErr}
			} else {
				terminal = &identityFakeTerminal{tabFakeTerminal: base}
				terminal.(*identityFakeTerminal).setIdentity(test.identity)
			}
			registry := p1Registry(terminal)
			registry.deps.Outcome = ledger
			registry.deps.ResolveAssetSessionIdentity = func(context.Context, string) (string, AssetIdentity, error) {
				return "", AssetIdentity{}, ErrNoLiveSession
			}
			connector := &r13Connector{sessionIDs: []string{"s9"}}
			registry.deps.AssetConnector = connector
			scope := Scope{SessionID: "s1", TabID: "t1"}
			call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
			call.AuthorizationID = "guard:test"

			result := registry.Execute(context.Background(), "job", scope, call, nil)
			if result.OK {
				t.Fatalf("%s: incomplete connected identity accepted: %+v", name, result)
			}
			if !strings.Contains(result.Text, "残留") || !strings.Contains(result.Text, "session_id=s9") {
				t.Fatalf("%s: residual session not visible: %+v", name, result)
			}
			if result.OperationID == "" {
				t.Fatalf("%s: failure lost the operation ID: %+v", name, result)
			}
			if connector.callCount() != 1 {
				t.Fatalf("%s: connector calls = %d, want 1", name, connector.callCount())
			}
		})
	}
}

func TestTerminalCreateLegacyConnectBindsIdentity(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	liveSession := ""
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	registry := p1Registry(fake)
	registry.deps.Outcome = ledger
	registry.deps.ResolveAssetSession = func(context.Context, string) (string, error) {
		if liveSession == "" {
			return "", ErrNoLiveSession
		}
		return liveSession, nil
	}
	connector := &r13Connector{sessionIDs: []string{"s9", "s10"}, onConnect: func(sessionID string) { liveSession = sessionID }}
	registry.deps.AssetConnector = connector
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK {
		t.Fatalf("legacy connected create = %+v", first)
	}
	if connector.callCount() != 1 || len(fake.created) != 1 {
		t.Fatalf("legacy connected create: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if !strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("legacy same-identity retry did not replay: %+v", second)
	}
	if connector.callCount() != 1 || len(fake.created) != 1 {
		t.Fatalf("legacy retry re-executed: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
	// Reconfigure: the session drops and the asset comes back with a new
	// configuration fingerprint; the retry must execute fresh, not replay.
	liveSession = ""
	fake.setIdentity(identityB)
	third := registry.Execute(context.Background(), "job", scope, call, nil)
	if !third.OK {
		t.Fatalf("legacy reconfigured create = %+v", third)
	}
	if strings.Contains(third.Text, "调用已执行过") {
		t.Fatalf("legacy reconfigured create replayed the old identity: %+v", third)
	}
	if connector.callCount() != 2 || len(fake.created) != 2 {
		t.Fatalf("legacy reconfigured create did not execute fresh: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
}
