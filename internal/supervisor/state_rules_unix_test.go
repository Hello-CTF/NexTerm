//go:build unix

package supervisor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/terminal/staterule"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type ruleSourceResult struct {
	revision string
	err      error
}

// testRuleSource serves a replaceable rule set for one owner and records
// push outcomes.
type testRuleSource struct {
	owner string

	mu      sync.Mutex
	set     staterule.Set
	results []ruleSourceResult
}

func (s *testRuleSource) StateRulesOwner() string { return s.owner }

func (s *testRuleSource) StateRules() (StateRuleSet, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set, true
}

func (s *testRuleSource) StateRulesPushed(revision string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = append(s.results, ruleSourceResult{revision, err})
}

func (s *testRuleSource) replace(set staterule.Set) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.set = set
}

func (s *testRuleSource) last() ruleSourceResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.results) == 0 {
		return ruleSourceResult{revision: "<none>"}
	}
	return s.results[len(s.results)-1]
}

func TestClientPushesItsOwnersStateRulesAfterHello(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	ctx := context.Background()
	sourceA := &testRuleSource{owner: testOwnerKey(0)}
	setA := testRuleSet(t, bottomRule(`Waiting`, base.SessionNeedsMe))
	sourceA.replace(setA)
	clientA := testClient(t, server).WithStateRules(sourceA)

	// Any operation pushes rules the helper does not have for the owner.
	if _, err := clientA.List(ctx); err != nil {
		t.Fatal(err)
	}
	if got := supervisor.StateRulesRevision(sourceA.owner); got != setA.Revision {
		t.Fatalf("helper revision = %q, want %q", got, setA.Revision)
	}
	if got := sourceA.last(); got.revision != setA.Revision || got.err != nil {
		t.Fatalf("push outcome = %+v", got)
	}

	// Another owner with its own rules, and an owner without rules, leave
	// the first owner's rules alone.
	sourceB := &testRuleSource{owner: testOwnerKey(1)}
	setB := testRuleSet(t, bottomRule(`Thinking`, base.SessionRunning))
	sourceB.replace(setB)
	if _, err := testClient(t, server).WithStateRules(sourceB).List(ctx); err != nil {
		t.Fatal(err)
	}
	empty := &testRuleSource{owner: testOwnerKey(2)}
	if _, err := testClient(t, server).WithStateRules(empty).List(ctx); err != nil {
		t.Fatal(err)
	}
	if supervisor.StateRulesRevision(sourceA.owner) != setA.Revision || supervisor.StateRulesRevision(sourceB.owner) != setB.Revision {
		t.Fatal("one owner's push changed another owner's rules")
	}

	// An explicit sync pushes a changed set at once.
	sourceA.replace(testRuleSet(t))
	if err := clientA.SyncStateRules(ctx); err != nil {
		t.Fatal(err)
	}
	if got := supervisor.StateRulesRevision(sourceA.owner); got != "" {
		t.Fatalf("helper revision after clearing = %q", got)
	}

	// A set the helper rejects is reported without failing the operation,
	// and the explicit sync returns the rejection.
	bad := setA
	bad.Revision = "forged"
	sourceA.replace(bad)
	if _, err := clientA.List(ctx); err != nil {
		t.Fatalf("list with rejected rules = %v", err)
	}
	if got := sourceA.last(); !errors.Is(got.err, ErrInvalidInput) {
		t.Fatalf("rejected push outcome = %+v", got)
	}
	if err := clientA.SyncStateRules(ctx); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("sync of rejected rules = %v", err)
	}

	// A client without a source neither pushes nor fails.
	if err := testClient(t, server).SyncStateRules(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsUseTheirCreatorsStateRules(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	ctx := context.Background()
	rule := bottomRule(`Waiting for review`, base.SessionNeedsMe)
	sourceA := &testRuleSource{owner: testOwnerKey(0)}
	sourceA.replace(testRuleSet(t, rule))
	sourceB := &testRuleSource{owner: testOwnerKey(1)}
	sourceB.replace(testRuleSet(t, bottomRule(`never shown`, base.SessionIdle)))

	create := func(client *Client) string {
		t.Helper()
		info, err := client.Create(ctx, CreateOptions{
			Command: []string{"/bin/sh", "-c", "printf 'Agent: Waiting for review'; sleep 30"},
			Env:     []string{"TERM=xterm-256color", "LC_ALL=C"},
			Cols:    80, Rows: 24,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Kill(context.Background(), info.ID, nil) })
		return info.ID
	}
	idA := create(testClient(t, server).WithStateRules(sourceA))
	idB := create(testClient(t, server).WithStateRules(sourceB))
	idNone := create(testClient(t, server))

	state := func(id string) *SessionState {
		supervisor.mu.Lock()
		session := supervisor.sessions[id]
		supervisor.mu.Unlock()
		return session.Info().State
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		got := state(idA)
		if got.State == base.SessionNeedsMe && got.Confidence == base.ConfidenceRule && got.RuleID == rule.ID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("creator's rule never applied: %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The other sessions went through the same idle inspection without
	// any of A's rules.
	for _, id := range []string{idB, idNone} {
		for {
			got := state(id)
			if got.State == base.SessionIdle {
				if got.Confidence == base.ConfidenceRule {
					t.Fatalf("session %s used another owner's rules: %+v", id, got)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("session %s never settled: %+v", id, got)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
