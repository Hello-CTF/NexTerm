//go:build unix

package supervisor

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/terminal/staterule"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type ruleSourceResult struct {
	revision string
	err      error
}

// testRuleSource serves a replaceable rule set and records push outcomes.
type testRuleSource struct {
	mu      sync.Mutex
	set     staterule.Set
	ok      bool
	results []ruleSourceResult
}

func (s *testRuleSource) StateRules() (StateRuleSet, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set, s.ok
}

func (s *testRuleSource) StateRulesPushed(revision string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = append(s.results, ruleSourceResult{revision, err})
}

func (s *testRuleSource) replace(set staterule.Set) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.set, s.ok = set, true
}

func (s *testRuleSource) last() ruleSourceResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.results) == 0 {
		return ruleSourceResult{revision: "<none>"}
	}
	return s.results[len(s.results)-1]
}

func TestClientPushesStateRulesAfterHello(t *testing.T) {
	supervisor := testSupervisor(t)
	server := testServer(t, supervisor)
	source := &testRuleSource{}
	set := testRuleSet(t, bottomRule(`Waiting`, base.SessionNeedsMe))
	source.replace(set)
	client := testClient(t, server).WithStateRules(source)
	ctx := context.Background()

	// Any operation pushes rules the helper does not have.
	if _, err := client.List(ctx); err != nil {
		t.Fatal(err)
	}
	if got := supervisor.StateRulesRevision(); got != set.Revision {
		t.Fatalf("helper revision = %q, want %q", got, set.Revision)
	}
	if got := source.last(); got.revision != set.Revision || got.err != nil {
		t.Fatalf("push outcome = %+v", got)
	}

	// An explicit sync pushes a changed set at once.
	cleared := testRuleSet(t)
	source.replace(cleared)
	if err := client.SyncStateRules(ctx); err != nil {
		t.Fatal(err)
	}
	if got := supervisor.StateRulesRevision(); got != "" {
		t.Fatalf("helper revision after clearing = %q", got)
	}

	// A set the helper rejects is reported without failing the operation,
	// and the explicit sync returns the rejection.
	bad := set
	bad.Revision = "forged"
	source.replace(bad)
	if _, err := client.List(ctx); err != nil {
		t.Fatalf("list with rejected rules = %v", err)
	}
	if got := source.last(); !errors.Is(got.err, ErrInvalidInput) {
		t.Fatalf("rejected push outcome = %+v", got)
	}
	if err := client.SyncStateRules(ctx); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("sync of rejected rules = %v", err)
	}
	if got := supervisor.StateRulesRevision(); got != "" {
		t.Fatalf("a rejected push changed the helper revision to %q", got)
	}

	// A client without a source neither pushes nor fails.
	if err := testClient(t, server).SyncStateRules(ctx); err != nil {
		t.Fatal(err)
	}
}
