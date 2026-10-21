package watches

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

// TestExecutorResumeFiltersSupersededMatchedRepeat covers identity
// replacement: an old matched repeat from a previous generation can no
// longer re-arm, so the new incarnation's watch anchors at the source end
// instead of firing on pre-attach output.
func TestExecutorResumeFiltersSupersededMatchedRepeat(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")

	// Executor A attaches identity-1; the repeat watch fires and stops at
	// matched/anchor=5 before the acknowledgement.
	first := newTestExecutor(t, backend, newChanSink())
	gate := make(chan struct{})
	source := &scriptSource{gate: gate, steps: []scriptStep{{data: []byte("ready")}}}
	attachTarget(t, first, ctx, "target-1", "identity-1", source)
	old, err := first.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready"), Repeat: true})
	if err != nil {
		t.Fatal(err)
	}
	close(gate)
	deadline := time.Now().Add(10 * time.Second)
	for {
		after, err := backend.AgentWatchGet(ctx, old.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status == store.AgentWatchStatusMatched {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watch did not fire: %+v", after)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := first.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	// Executor B attaches identity-2 (replacement): the old matched repeat
	// can no longer re-arm, so the new watch anchors at the source end.
	second := newTestExecutor(t, backend, newChanSink())
	secondGate := make(chan struct{})
	secondSource := &scriptSource{endSeq: 7, gate: secondGate, tail: []byte("aaaaaXX"), steps: []scriptStep{
		{data: []byte("XX")},
	}}
	attachTarget(t, second, ctx, "target-1", "identity-2", secondSource)
	fresh, err := second.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("XX")})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.AnchorSeq != 7 {
		t.Fatalf("new watch anchor = %d; want the source end 7, not the superseded position 5", fresh.AnchorSeq)
	}
	// New output after the anchor fires normally; the historical 5..7 output
	// predates the incarnation and is never evaluated.
	close(secondGate)
	deadline = time.Now().Add(10 * time.Second)
	for {
		after, err := backend.AgentWatchGet(ctx, fresh.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status == store.AgentWatchStatusMatched {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("new watch did not fire on new output: %+v", after)
		}
		time.Sleep(5 * time.Millisecond)
	}
	after, err := backend.AgentWatchGet(ctx, fresh.ID)
	if err != nil || after.MatchedFrom != 7 || after.MatchedTo != 9 {
		t.Fatalf("new watch fired on %+v; want new output 7..9", after)
	}
	if after, err := backend.AgentWatchGet(ctx, old.ID); err != nil || after.Status != store.AgentWatchStatusMatched {
		t.Fatalf("old watch after replacement = %+v, %v", after, err)
	}
	if err := second.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestExecutorResumeFiltersSupersededMatchedRepeatDetach covers permanent
// detach and reattach: the resurrected incarnation's watch anchors at the
// source end, not at the superseded matched repeat's position.
func TestExecutorResumeFiltersSupersededMatchedRepeatDetach(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")

	first := newTestExecutor(t, backend, newChanSink())
	gate := make(chan struct{})
	source := &scriptSource{gate: gate, steps: []scriptStep{{data: []byte("ready")}}}
	attachTarget(t, first, ctx, "target-1", "identity-1", source)
	old, err := first.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready"), Repeat: true})
	if err != nil {
		t.Fatal(err)
	}
	close(gate)
	deadline := time.Now().Add(10 * time.Second)
	for {
		after, err := backend.AgentWatchGet(ctx, old.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status == store.AgentWatchStatusMatched {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watch did not fire: %+v", after)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := first.DetachTarget(ctx, "target-1", true); err != nil {
		t.Fatal(err)
	}
	if err := first.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	// Reattach after the permanent detach: the resurrected incarnation is a
	// new generation, so its watch anchors at the source end.
	second := newTestExecutor(t, backend, newChanSink())
	secondGate := make(chan struct{})
	secondSource := &scriptSource{endSeq: 7, gate: secondGate, tail: []byte("aaaaaXX"), steps: []scriptStep{
		{data: []byte("XX")},
	}}
	attachTarget(t, second, ctx, "target-1", "identity-1", secondSource)
	fresh, err := second.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("XX")})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.AnchorSeq != 7 {
		t.Fatalf("new watch anchor = %d; want the source end 7, not the superseded position 5", fresh.AnchorSeq)
	}
	close(secondGate)
	deadline = time.Now().Add(10 * time.Second)
	for {
		after, err := backend.AgentWatchGet(ctx, fresh.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status == store.AgentWatchStatusMatched {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("new watch did not fire on new output: %+v", after)
		}
		time.Sleep(5 * time.Millisecond)
	}
	after, err := backend.AgentWatchGet(ctx, fresh.ID)
	if err != nil || after.MatchedFrom != 7 || after.MatchedTo != 9 {
		t.Fatalf("new watch fired on %+v; want new output 7..9", after)
	}
	if err := second.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestExecutorResumeFiltersSupersededMatchedRepeatRestart is the restart
// variant: a new engine on the same store must also filter the superseded
// matched repeat out of the resume candidates.
func TestExecutorResumeFiltersSupersededMatchedRepeatRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.db")
	clock := newTestClock()
	ctx := ownerCtx("owner-a")

	backend := openRestartStore(t, path)
	engine := newRestartEngine(t, backend, clock)
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	_, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready"), Repeat: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready")); err != nil {
		t.Fatal(err)
	}
	attachEngine(t, engine, ctx, "target-1", "identity-2")
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen on the same store and attach as a new executor: the superseded
	// matched repeat must not pull the new watch back to position 5.
	backend = openRestartStore(t, path)
	defer backend.Close()
	executor := newTestExecutor(t, backend, newChanSink())
	gate := make(chan struct{})
	source := &scriptSource{endSeq: 7, gate: gate, tail: []byte("aaaaaXX"), steps: []scriptStep{{data: []byte("XX")}}}
	attachTarget(t, executor, ctx, "target-1", "identity-2", source)
	fresh, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("XX")})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.AnchorSeq != 7 {
		t.Fatalf("new watch anchor = %d; want the source end 7", fresh.AnchorSeq)
	}
	close(gate)
	deadline := time.Now().Add(10 * time.Second)
	for {
		after, err := backend.AgentWatchGet(ctx, fresh.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status == store.AgentWatchStatusMatched {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("new watch did not fire on new output: %+v", after)
		}
		time.Sleep(5 * time.Millisecond)
	}
	after, err := backend.AgentWatchGet(ctx, fresh.ID)
	if err != nil || after.MatchedFrom != 7 || after.MatchedTo != 9 {
		t.Fatalf("new watch fired on %+v; want new output 7..9", after)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
