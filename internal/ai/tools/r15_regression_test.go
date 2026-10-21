package tools

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/outcome"
)

// R15-F1: the manual adjudication must apply the same binding contract as
// the outcome ledger before any replay/running/pending handling — canonical
// arguments, Kind, and AuthorizationID all bind the operation. A
// same-authorization retry with changed arguments surfaces the conflict and
// never replays the old success, never reconnects.
//
// R15-F2: adjudication→pre-key connect→ledger reserve/claim is arbitrated
// in-process per operation: a concurrent duplicate of the same call fails
// fast with the operation ID before connecting, and every post-connect
// replay/conflict/record-failure path keeps naming the connected session
// and the operation ID.

// r15BarrierConnector blocks each ConnectAsset until release closes and
// counts calls, so a concurrent duplicate can be observed mid-flight.
type r15BarrierConnector struct {
	mu         sync.Mutex
	calls      int
	sessionIDs []string
	entered    chan string
	release    chan struct{}
}

func (c *r15BarrierConnector) ConnectAsset(context.Context, string) (string, error) {
	c.mu.Lock()
	index := c.calls
	c.calls++
	c.mu.Unlock()
	if index >= len(c.sessionIDs) {
		index = len(c.sessionIDs) - 1
	}
	sessionID := c.sessionIDs[index]
	c.entered <- sessionID
	<-c.release
	return sessionID, nil
}

func (c *r15BarrierConnector) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestTerminalCreateAdjudicationRejectsChangedArguments(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	connected := false
	vacated := false
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	registry := p1Registry(fake)
	registry.deps.Outcome = ledger
	registry.deps.ResolveAssetSessionIdentity = func(context.Context, string) (string, AssetIdentity, error) {
		if !connected || vacated {
			return "", AssetIdentity{}, ErrNoLiveSession
		}
		return "s9", identityA, nil
	}
	connector := &r13Connector{sessionIDs: []string{"s9", "s10"}, onConnect: func(string) { connected = true }}
	registry.deps.AssetConnector = connector
	scope := Scope{SessionID: "s1", TabID: "t1"}
	first := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true,"title":"first"}`)
	first.AuthorizationID = "guard:test"

	result := registry.Execute(context.Background(), "job", scope, first, nil)
	if !result.OK {
		t.Fatalf("first create = %+v", result)
	}
	// The same authorization retries the same call ID with a changed title:
	// the ledger's binding contract makes this a conflict, not a replay.
	vacated = true
	retry := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true,"title":"changed"}`)
	retry.AuthorizationID = "guard:test"
	second := registry.Execute(context.Background(), "job", scope, retry, nil)
	if second.OK {
		t.Fatalf("changed-argument retry replayed the old success: %+v", second)
	}
	if !strings.Contains(second.Text, "冲突") {
		t.Fatalf("changed-argument retry not surfaced as a conflict: %+v", second)
	}
	if strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("changed-argument retry replayed: %+v", second)
	}
	if second.OperationID == "" {
		t.Fatalf("conflict lost the operation ID: %+v", second)
	}
	if connector.callCount() != 1 || len(fake.created) != 1 {
		t.Fatalf("conflict re-executed: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
	// The unchanged-argument retry still replays under the same contract.
	third := registry.Execute(context.Background(), "job", scope, first, nil)
	if !third.OK || !strings.Contains(third.Text, "调用已执行过") {
		t.Fatalf("unchanged-argument retry did not replay: %+v", third)
	}
	if connector.callCount() != 1 || len(fake.created) != 1 {
		t.Fatalf("replay re-executed: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
}

func TestTerminalCreateConcurrentSameOperationConnectsOnce(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	registry := p1Registry(fake)
	registry.deps.Outcome = ledger
	registry.deps.ResolveAssetSessionIdentity = func(context.Context, string) (string, AssetIdentity, error) {
		return "", AssetIdentity{}, ErrNoLiveSession
	}
	connector := &r15BarrierConnector{sessionIDs: []string{"s9", "s10"}, entered: make(chan string, 2), release: make(chan struct{})}
	registry.deps.AssetConnector = connector
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	firstResult := make(chan Output, 1)
	go func() { firstResult <- registry.Execute(context.Background(), "job", scope, call, nil) }()
	// The first call is inside ConnectAsset, holding the operation gate.
	if sessionID := <-connector.entered; sessionID != "s9" {
		t.Fatalf("first connect = %s, want s9", sessionID)
	}
	duplicate := registry.Execute(context.Background(), "job", scope, call, nil)
	if duplicate.OK {
		t.Fatalf("concurrent duplicate reported success: %+v", duplicate)
	}
	if !strings.Contains(duplicate.Text, "正在执行中") {
		t.Fatalf("concurrent duplicate not surfaced as in-progress: %+v", duplicate)
	}
	if duplicate.OperationID == "" {
		t.Fatalf("in-progress duplicate lost the operation ID: %+v", duplicate)
	}
	if strings.Contains(duplicate.Text, "残留") {
		t.Fatalf("duplicate that never connected names a residual: %+v", duplicate)
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
	retry := registry.Execute(context.Background(), "job", scope, call, nil)
	if !retry.OK || !strings.Contains(retry.Text, "调用已执行过") {
		t.Fatalf("post-flight retry did not replay: %+v", retry)
	}
	if connector.callCount() != 1 || len(fake.created) != 1 {
		t.Fatalf("post-flight retry re-executed: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
}

func TestTerminalCreateRecordInProgressAfterConnectNamesResidual(t *testing.T) {
	_, ledger, sqliteStore := outcomeFixture(t)
	connected := false
	vacated := false
	fake := &identityFakeTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0))}
	fake.setIdentity(identityA)
	newRegistry := func(connector AssetConnector) *Registry {
		registry := p1Registry(fake)
		registry.deps.Outcome = ledger
		registry.deps.ResolveAssetSessionIdentity = func(context.Context, string) (string, AssetIdentity, error) {
			if !connected || vacated {
				return "", AssetIdentity{}, ErrNoLiveSession
			}
			return "s9", identityA, nil
		}
		registry.deps.AssetConnector = connector
		return registry
	}
	firstConnector := &r13Connector{sessionIDs: []string{"s9"}, onConnect: func(string) { connected = true }}
	registry := newRegistry(firstConnector)
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK {
		t.Fatalf("first create = %+v", first)
	}
	// Rewrite the finished record into a running one, simulating an in-flight
	// execution of the same operation; a fresh registry (no in-memory
	// mapping) then reconnects and must surface the ledger's in-progress
	// verdict with its own residual and the operation ID.
	key := OutcomeKeyWithIdentity("job", "c", []AssetIdentity{identityA})
	record, err := ledger.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get recorded outcome: %v", err)
	}
	started := time.Now()
	record.State = outcome.ExecutionRunning
	record.StartedAt = &started
	record.FinishedAt = nil
	revision := record.Revision
	record.Revision = revision + 1
	if err := sqliteStore.Update(context.Background(), record, revision); err != nil {
		t.Fatalf("mark record running: %v", err)
	}

	vacated = true
	secondConnector := &r13Connector{sessionIDs: []string{"s10"}}
	other := newRegistry(secondConnector)
	second := other.Execute(context.Background(), "job", scope, call, nil)
	if second.OK {
		t.Fatalf("in-progress retry reported success: %+v", second)
	}
	if !strings.Contains(second.Text, "正在执行中") {
		t.Fatalf("in-progress verdict not surfaced: %+v", second)
	}
	if second.OperationID == "" {
		t.Fatalf("in-progress failure lost the operation ID: %+v", second)
	}
	if !strings.Contains(second.Text, "残留") || !strings.Contains(second.Text, "session_id=s10") {
		t.Fatalf("post-connect in-progress failure hides its residual: %+v", second)
	}
	if secondConnector.callCount() != 1 {
		t.Fatalf("second registry connector calls = %d, want 1", secondConnector.callCount())
	}
	if len(fake.created) != 1 {
		t.Fatalf("in-progress retry re-created: %d tabs", len(fake.created))
	}
}
