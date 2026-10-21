package tools

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/outcome"
)

// R14-F1: a recorded terminal_create retry must never establish a new
// connection before adjudicating the operation's existing outcome records
// through the stable operation→identity mapping: a finished record replays
// directly (naming the originally connected session), a pending or running
// record fails visibly, and only the certain fresh-execution case connects.
//
// R14-F2: the session a pre-key connect established and verified is reused
// directly at execution — the resolver-driven connect never runs a second
// time within the same call, and every success/failure result keeps naming
// that same session.

// r14SequenceIdentityTerminal reports a fixed identity for the first
// failAfter reads and errors afterwards, modeling a session that vanishes
// between the pre-key verification and the execution re-verification.
type r14SequenceIdentityTerminal struct {
	*tabFakeTerminal
	mu        sync.Mutex
	reads     int
	failAfter int
	identity  AssetIdentity
	err       error
}

func (f *r14SequenceIdentityTerminal) SessionAssetIdentity(context.Context, string) (AssetIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.reads > f.failAfter {
		return AssetIdentity{}, f.err
	}
	return f.identity, nil
}

func TestTerminalCreateRecordedRetryReplayDoesNotReconnect(t *testing.T) {
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
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK {
		t.Fatalf("first connected create = %+v", first)
	}
	if connector.callCount() != 1 || len(fake.created) != 1 {
		t.Fatalf("first create: connector=%d created=%d", connector.callCount(), len(fake.created))
	}
	// The connected session vanishes; the same-identity retry must replay
	// the recorded result without establishing s10.
	vacated = true
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if !second.OK || !strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("vanished-session retry did not replay: %+v", second)
	}
	if !strings.Contains(second.Text, "session_id=s9") {
		t.Fatalf("replay lost the original session note: %+v", second)
	}
	if connector.callCount() != 1 {
		t.Fatalf("retry reconnected: connector=%d", connector.callCount())
	}
	if len(fake.created) != 1 {
		t.Fatalf("retry re-created: %d tabs", len(fake.created))
	}
}

func TestTerminalCreateRecordedRetryNonTerminalDoesNotReconnect(t *testing.T) {
	_, ledger, sqliteStore := outcomeFixture(t)
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
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	first := registry.Execute(context.Background(), "job", scope, call, nil)
	if !first.OK {
		t.Fatalf("first connected create = %+v", first)
	}
	// Rewrite the finished record into a running one, simulating an
	// in-flight execution of the same operation elsewhere.
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
	second := registry.Execute(context.Background(), "job", scope, call, nil)
	if second.OK {
		t.Fatalf("running-record retry reported success: %+v", second)
	}
	if strings.Contains(second.Text, "调用已执行过") {
		t.Fatalf("running record replayed: %+v", second)
	}
	if !strings.Contains(second.Text, "正在执行中") {
		t.Fatalf("running record not surfaced: %+v", second)
	}
	if connector.callCount() != 1 {
		t.Fatalf("running-record retry reconnected: connector=%d", connector.callCount())
	}
	if len(fake.created) != 1 {
		t.Fatalf("running-record retry re-created: %d tabs", len(fake.created))
	}
}

func TestTerminalCreateExecutionReusesPreKeyConnectedSession(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	// The resolver never sees the connected session (delayed visibility), so
	// a resolver-driven execution connect would fire a second time.
	fake := &r14SequenceIdentityTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0)), failAfter: 2, identity: identityA}
	registry := p1Registry(fake)
	registry.deps.Outcome = ledger
	registry.deps.ResolveAssetSessionIdentity = func(context.Context, string) (string, AssetIdentity, error) {
		return "", AssetIdentity{}, ErrNoLiveSession
	}
	connector := &r13Connector{sessionIDs: []string{"s9", "s10"}}
	registry.deps.AssetConnector = connector
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	result := registry.Execute(context.Background(), "job", scope, call, nil)
	if !result.OK {
		t.Fatalf("create on the pre-key connected session = %+v", result)
	}
	if connector.callCount() != 1 {
		t.Fatalf("resolver-driven second connect: connector=%d", connector.callCount())
	}
	if len(fake.created) != 1 {
		t.Fatalf("created = %d tabs, want 1", len(fake.created))
	}
	if !strings.Contains(result.Text, "session_id=s9") || !strings.Contains(result.Text, "handle=t/tab-new/s9") {
		t.Fatalf("result does not name the connected session: %+v", result)
	}
}

func TestTerminalCreateExecutionReuseFailClosedWhenSessionGone(t *testing.T) {
	_, ledger, _ := outcomeFixture(t)
	// The session passes the pre-key verification and is gone by execution:
	// the call fails visibly, names the residual session, and never connects
	// a second time.
	fake := &r14SequenceIdentityTerminal{tabFakeTerminal: newTabFake(fakeTab("t1", "s1", "$ ", 0)), failAfter: 1, identity: identityA, err: errors.New("session gone")}
	registry := p1Registry(fake)
	registry.deps.Outcome = ledger
	registry.deps.ResolveAssetSessionIdentity = func(context.Context, string) (string, AssetIdentity, error) {
		return "", AssetIdentity{}, ErrNoLiveSession
	}
	connector := &r13Connector{sessionIDs: []string{"s9", "s10"}}
	registry.deps.AssetConnector = connector
	scope := Scope{SessionID: "s1", TabID: "t1"}
	call := p1Call("c", "terminal_create", `{"asset_id":"a1","connect":true}`)
	call.AuthorizationID = "guard:test"

	result := registry.Execute(context.Background(), "job", scope, call, nil)
	if result.OK {
		t.Fatalf("create on a vanished session reported success: %+v", result)
	}
	if !strings.Contains(result.Text, "session gone") {
		t.Fatalf("session loss not surfaced: %+v", result)
	}
	if !strings.Contains(result.Text, "残留") || !strings.Contains(result.Text, "session_id=s9") {
		t.Fatalf("residual session not visible: %+v", result)
	}
	if result.OperationID == "" {
		t.Fatalf("failure lost the operation ID: %+v", result)
	}
	if connector.callCount() != 1 {
		t.Fatalf("second connect after session loss: connector=%d", connector.callCount())
	}
	if len(fake.created) != 0 {
		t.Fatalf("create executed on a vanished session: %+v", fake.created)
	}
}
