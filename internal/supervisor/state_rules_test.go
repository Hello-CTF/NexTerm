package supervisor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/terminal/staterule"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func testRuleSet(t *testing.T, rules ...staterule.Rule) staterule.Set {
	t.Helper()
	set, err := staterule.NewSet(rules)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func bottomRule(pattern, state string) staterule.Rule {
	return staterule.Rule{ID: ids.New(), Region: staterule.RegionBottom, Pattern: pattern, State: state}
}

// withRules gives the harness tracker a rule set it can replace.
func (h *trackerHarness) withRules(set staterule.Set) *staterule.Compiled {
	h.t.Helper()
	compiled, err := staterule.Compile(set)
	if err != nil {
		h.t.Fatal(err)
	}
	h.tracker.opts.rules = func() *staterule.Compiled { return compiled }
	return compiled
}

func TestStateRuleDecidesAtTheIdleInspection(t *testing.T) {
	h := newHarness(t)
	waiting := bottomRule(`Waiting for your review`, base.SessionNeedsMe)
	waiting.Keys = "\r"
	h.withRules(testRuleSet(t, waiting))
	h.out(time.Second, "Agent: Waiting for your review")
	h.expect(base.SessionRunning, base.ConfidenceSignal)
	h.idle()
	got := h.expect(base.SessionNeedsMe, base.ConfidenceRule)
	if got.Reason != "rule" || got.RuleID != waiting.ID || got.RuleKeys != "\r" {
		t.Fatalf("ruled state = %+v", got)
	}
	last := got.Events[len(got.Events)-1]
	if last.Kind != base.SessionEventState || last.RuleID != waiting.ID {
		t.Fatalf("state event = %+v", last)
	}
	// A redraw holds the rule's needs_me like a detected prompt.
	h.out(time.Second, "\r\x1b[KAgent: Waiting for your review")
	h.expect(base.SessionNeedsMe, base.ConfidenceRule)
	// Input clears it and drops the rule.
	h.in(time.Second)
	cleared := h.expect(base.SessionIdle, base.ConfidenceSignal)
	if cleared.RuleID != "" || cleared.RuleKeys != "" {
		t.Fatalf("rule kept after input: %+v", cleared)
	}
}

func TestStateRuleOverridesAPromptShape(t *testing.T) {
	h := newHarness(t)
	h.withRules(testRuleSet(t, bottomRule(`Overwrite config\.yaml\?`, base.SessionIdle)))
	h.out(time.Second, "Overwrite config.yaml? [y/N] ")
	h.idle()
	got := h.expect(base.SessionIdle, base.ConfidenceRule)
	if got.Prompt == nil || got.Prompt.Kind != "yesno" {
		t.Fatalf("the detected prompt should stay published for taps: %+v", got.Prompt)
	}
	// Without the rule the same screen needs the user.
	other := newHarness(t)
	other.out(time.Second, "Overwrite config.yaml? [y/N] ")
	other.idle()
	other.expect(base.SessionNeedsMe, base.ConfidenceHeuristic)
}

func TestStateRuleKeepsAQuietSessionRunning(t *testing.T) {
	h := newHarness(t)
	h.withRules(testRuleSet(t, bottomRule(`esc to interrupt`, base.SessionRunning)))
	h.out(time.Second, "✻ Thinking… (esc to interrupt)")
	if wait := h.tracker.tick(h.clock.advance(defaultStateIdle)); wait != 0 {
		t.Fatalf("a ruled state should not settle, wait %v", wait)
	}
	h.expect(base.SessionRunning, base.ConfidenceRule)
	h.clock.advance(time.Minute)
	h.expect(base.SessionRunning, base.ConfidenceRule)
}

func TestStateSignalWinsOverARule(t *testing.T) {
	h := newHarness(t)
	h.withRules(testRuleSet(t, bottomRule(`ready`, base.SessionIdle)))
	h.out(5*time.Second, "ready\a")
	h.idle()
	h.expect(base.SessionNeedsMe, base.ConfidenceSignal)
}

func TestStateRulesChangedReevaluatesAQuietSession(t *testing.T) {
	h := newHarness(t)
	h.out(time.Second, "Agent: Waiting for your review")
	h.idle()
	h.expect(base.SessionIdle, base.ConfidenceSignal)
	h.withRules(testRuleSet(t, bottomRule(`Waiting for your review`, base.SessionNeedsMe)))
	changes := h.changes
	h.tracker.rulesChanged(h.clock.advance(time.Second))
	h.expect(base.SessionNeedsMe, base.ConfidenceRule)
	if h.changes == changes {
		t.Fatal("rule change did not notify")
	}
	// Removing the rule lets the session settle back to idle.
	h.tracker.opts.rules = func() *staterule.Compiled { return nil }
	h.tracker.rulesChanged(h.clock.advance(time.Second))
	h.idle()
	h.expect(base.SessionIdle, base.ConfidenceSignal)
}

func TestStateRuleThatStopsMatchingLeavesTheRuleState(t *testing.T) {
	h := newHarness(t)
	h.withRules(testRuleSet(t, bottomRule(`esc to interrupt`, base.SessionRunning)))
	h.out(time.Second, "✻ Thinking… (esc to interrupt)")
	h.tracker.tick(h.clock.advance(defaultStateIdle))
	h.expect(base.SessionRunning, base.ConfidenceRule)
	// The spinner line is replaced by plain output: the session is running
	// on the output signal until it settles, no longer on the rule.
	h.out(time.Second, "\r\x1b[Kdone")
	if wait := h.tracker.tick(h.clock.advance(defaultStateIdle)); wait == 0 {
		t.Fatal("expected the session to settle")
	}
	got := h.expect(base.SessionRunning, base.ConfidenceSignal)
	if got.RuleID != "" {
		t.Fatalf("rule kept: %+v", got)
	}
}

func testOwnerKey(n int) string {
	return fmt.Sprintf("%032x", n+1)
}

func TestSupervisorKeepsStateRulesPerOwner(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	first, err := New(Config{StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	ownerA, ownerB := testOwnerKey(0), testOwnerKey(1)
	setA := testRuleSet(t, bottomRule(`Waiting`, base.SessionNeedsMe))
	setB := testRuleSet(t, bottomRule(`Thinking`, base.SessionRunning))
	if err := first.SetStateRules(ownerA, setA); err != nil {
		t.Fatal(err)
	}
	if err := first.SetStateRules(ownerB, setB); err != nil {
		t.Fatal(err)
	}
	if first.StateRulesRevision(ownerA) != setA.Revision || first.StateRulesRevision(ownerB) != setB.Revision {
		t.Fatal("one owner's set replaced another's")
	}
	if err := first.SetStateRules("not-a-key", setA); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("malformed owner key = %v", err)
	}
	invalid := setA
	invalid.Rules = append([]staterule.Rule(nil), setA.Rules...)
	invalid.Rules[0].Pattern = "(unclosed"
	invalid.Revision = staterule.Revision(invalid.Rules)
	if err := first.SetStateRules(ownerA, invalid); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid rules = %v", err)
	}
	if got := first.StateRulesRevision(ownerA); got != setA.Revision {
		t.Fatalf("revision after a rejected push = %q, want %q", got, setA.Revision)
	}
	_ = first.Close()

	// A restarted helper applies every owner's rules without any client.
	second, err := New(Config{StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	if got := second.StateRules(ownerA); got.Revision != setA.Revision || got.Rules[0].ID != setA.Rules[0].ID {
		t.Fatalf("reloaded rules of A = %+v", got)
	}
	if got := second.StateRulesRevision(ownerB); got != setB.Revision {
		t.Fatalf("reloaded revision of B = %q", got)
	}
	// Clearing A's rules leaves B's.
	if err := second.SetStateRules(ownerA, staterule.Set{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stateRulesDir(second.StateDir()), ownerA+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clearing the rules left the file: %v", err)
	}
	if second.StateRulesRevision(ownerB) != setB.Revision {
		t.Fatal("clearing A's rules changed B's")
	}
	_ = second.Close()

	// A damaged file leaves its owner without rules and an empty revision,
	// so the owner's next client pushes again.
	if err := os.WriteFile(filepath.Join(stateRulesDir(stateDir), ownerB+".json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := New(Config{StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = third.Close() }()
	if got := third.StateRulesRevision(ownerB); got != "" {
		t.Fatalf("revision from a damaged file = %q", got)
	}
}

func TestSupervisorEvictsTheLeastRecentlyUsedOwner(t *testing.T) {
	supervisor, err := New(Config{StateDir: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = supervisor.Close() }()
	clock := stateEpoch0
	supervisor.now = func() time.Time { return clock }
	set := testRuleSet(t, bottomRule(`Waiting`, base.SessionNeedsMe))
	for i := 0; i < maxStateRuleOwners; i++ {
		clock = clock.Add(time.Second)
		if err := supervisor.SetStateRules(testOwnerKey(i), set); err != nil {
			t.Fatal(err)
		}
	}
	// A hello from the oldest owner counts as a use; the second oldest is
	// then the least recently used.
	clock = clock.Add(time.Second)
	if got := supervisor.stateRulesSeen(testOwnerKey(0)); got != set.Revision {
		t.Fatalf("seen revision = %q", got)
	}
	clock = clock.Add(time.Second)
	if err := supervisor.SetStateRules(testOwnerKey(maxStateRuleOwners), set); err != nil {
		t.Fatal(err)
	}
	if supervisor.StateRulesRevision(testOwnerKey(1)) != "" {
		t.Fatal("the least recently used owner was kept")
	}
	if _, err := os.Stat(filepath.Join(stateRulesDir(supervisor.StateDir()), testOwnerKey(1)+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("evicted owner's file remains: %v", err)
	}
	for _, i := range []int{0, 2, maxStateRuleOwners} {
		if supervisor.StateRulesRevision(testOwnerKey(i)) != set.Revision {
			t.Fatalf("owner %d lost its rules", i)
		}
	}
}

func testOwners(t *testing.T, limit int, bound map[string]bool) *stateRuleOwners {
	t.Helper()
	owners := loadStateRuleOwners(filepath.Join(t.TempDir(), "state"))
	owners.limit = limit
	owners.bound = func() map[string]bool { return bound }
	return owners
}

func TestStateRuleOwnersNeverEvictAnOwnerInUse(t *testing.T) {
	inUse := map[string]bool{testOwnerKey(0): true}
	owners := testOwners(t, 3, inUse)
	set := testRuleSet(t, bottomRule(`Waiting`, base.SessionNeedsMe))
	now := stateEpoch0
	for i := 0; i < 3; i++ {
		now = now.Add(time.Second)
		if _, _, err := owners.set(testOwnerKey(i), set, now); err != nil {
			t.Fatal(err)
		}
	}
	// Owner 0 is the least recently used but live sessions use its rules:
	// owner 1 goes instead.
	_, evicted, err := owners.set(testOwnerKey(3), set, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(evicted) != 1 || evicted[0] != testOwnerKey(1) {
		t.Fatalf("evicted %v, want owner 1", evicted)
	}
	if owners.compiled(testOwnerKey(0)) == nil || owners.compiled(testOwnerKey(1)) != nil {
		t.Fatal("the owner in use lost its rules or the idle one kept them")
	}
}

func TestStateRuleOwnersRefuseAPushWhenEverySetIsInUse(t *testing.T) {
	inUse := map[string]bool{testOwnerKey(0): true, testOwnerKey(1): true}
	owners := testOwners(t, 2, inUse)
	set := testRuleSet(t, bottomRule(`Waiting`, base.SessionNeedsMe))
	for i := 0; i < 2; i++ {
		if _, _, err := owners.set(testOwnerKey(i), set, stateEpoch0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := owners.set(testOwnerKey(2), set, stateEpoch0.Add(time.Minute)); !errors.Is(err, ErrLimit) {
		t.Fatalf("push with every set in use = %v, want ErrLimit", err)
	}
	if owners.compiled(testOwnerKey(2)) != nil {
		t.Fatal("the refused owner's rules were installed")
	}
	if _, err := os.Stat(filepath.Join(owners.dir, testOwnerKey(2)+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refused owner's rules were written: %v", err)
	}
	for i := 0; i < 2; i++ {
		if owners.compiled(testOwnerKey(i)) == nil {
			t.Fatalf("owner %d in use lost its rules", i)
		}
	}
	// An owner already holding a set may still replace it.
	other := testRuleSet(t, bottomRule(`Thinking`, base.SessionRunning))
	if _, _, err := owners.set(testOwnerKey(1), other, stateEpoch0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
}

// A rule change that lands between the inspection prepared outside the
// lock and its use must not leave the old rules' result in force.
func TestStateRulesChangedBetweenPrepareAndInspect(t *testing.T) {
	h := newHarness(t)
	owners := testOwners(t, maxStateRuleOwners, nil)
	owner := testOwnerKey(0)
	h.tracker.opts.rules = func() *staterule.Compiled { return owners.compiled(owner) }
	h.out(time.Second, "Agent: Waiting for your review")
	now := h.clock.advance(defaultStateIdle)
	pre := h.tracker.prepare(now)
	if pre == nil || pre.ruled {
		t.Fatalf("prepared inspection = %+v", pre)
	}
	rule := bottomRule(`Waiting for your review`, base.SessionNeedsMe)
	if _, _, err := owners.set(owner, testRuleSet(t, rule), now); err != nil {
		t.Fatal(err)
	}
	h.tracker.mu.Lock()
	h.tracker.inspectLocked(pre)
	ruled, matched := h.tracker.ruled, h.tracker.rule
	h.tracker.mu.Unlock()
	if !ruled || matched.ID != rule.ID {
		t.Fatalf("inspection used the rules from before the change: ruled %v, rule %+v", ruled, matched)
	}
}
