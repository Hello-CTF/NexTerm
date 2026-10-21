package watches

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

// A source-reported gap skips the runner to the honest resume position: the
// engine's own gap semantics settle the lagging watch as unverifiable with
// the true positions in the cause, the stream keeps being consumed (no
// mis-positioned match ever fires), and the executor stops cleanly.
func TestExecutorSourceGapSettlesLaggingWatches(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	sink := newChanSink()
	executor := newTestExecutor(t, backend, sink)
	gate := make(chan struct{})
	source := &scriptSource{
		steps: []scriptStep{
			{data: []byte("aa")},
			{err: &GapError{FromSeq: 100}},
			{data: []byte("bbzz")},
		},
		gate: gate,
	}
	if err := executor.AttachTarget(ctx, "target-1", "identity-1", source); err != nil {
		t.Fatal(err)
	}
	// The runner is parked on the very first chunk, so the watch anchors at 0
	// with an empty left context deterministically.
	watch, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("zz")})
	if err != nil {
		t.Fatal(err)
	}
	close(gate)
	// "aa" advances the watch to 2, the gap skips the runner to 100, and the
	// next chunk observes at the true position: the watch evaluated 2 and the
	// chunk starts at 100, so the lost window settles it as unverifiable.
	deadline := time.Now().Add(10 * time.Second)
	for {
		row, err := backend.AgentWatchGet(ctx, watch.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status == store.AgentWatchStatusUnverifiable {
			if !strings.Contains(row.LastError, "output gap: evaluated 2, chunk starts 100") {
				t.Fatalf("unverifiable cause = %q; want the true gap positions", row.LastError)
			}
			break
		}
		if row.Status != store.AgentWatchStatusArmed {
			t.Fatalf("watch settled as %s; want unverifiable", row.Status)
		}
		if time.Now().After(deadline) {
			t.Fatalf("lagging watch was not settled: %+v", row)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The "zz" bytes at 102..104 are behind the hole for this watch: no event
	// may fire from mis-positioned data, so the outbox stays empty and the
	// runner keeps consuming until EOF.
	if err := executor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if delivered, err := executor.DispatchOnce(ctx); err != nil || delivered != 0 {
		t.Fatalf("dispatch after gap = %d, %v; want 0, nil", delivered, err)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	source.mu.Lock()
	closed := source.closed
	source.mu.Unlock()
	if !closed {
		t.Fatal("executor stop did not close the gapped source")
	}
}

// The executor's dispatch tick sweeps due watches: an expired watch settles
// without any output or explicit sweep call.
func TestExecutorDispatchTickSweepsExpired(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	clock := newTestClock()
	executor, err := NewExecutor(ctx, backend, newChanSink(), ExecutorOptions{
		Now: clock.Now, DispatchInterval: 5 * time.Millisecond, RetryBackoff: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	source := &scriptSource{}
	if err := executor.AttachTarget(ctx, "target-1", "identity-1", source); err != nil {
		t.Fatal(err)
	}
	watch, err := executor.Watch(ctx, WatchParams{
		TargetID: "target-1", Condition: containsCondition("ready"), ExpiresAt: clock.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Hour)
	deadline := time.Now().Add(10 * time.Second)
	for {
		row, err := backend.AgentWatchGet(ctx, watch.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status == store.AgentWatchStatusExpired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watch was not swept: %+v", row)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// A terminal source failure stops the runner without retrying; the target
// can then be re-attached, which replaces the dead runner, closes the stale
// source, and settles the previous incarnation's watches.
func TestExecutorTerminalErrorStopsRunnerAndAllowsReattach(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	sink := newChanSink()
	executor := newTestExecutor(t, backend, sink)
	gate := make(chan struct{})
	stale := &scriptSource{
		steps: []scriptStep{
			{data: []byte("aa")},
			{err: &TerminalError{Err: errors.New("runtime replaced")}},
		},
		gate:      gate,
		gateAfter: 1,
	}
	if err := executor.AttachTarget(ctx, "target-1", "identity-1", stale); err != nil {
		t.Fatal(err)
	}
	watch, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}
	if identity, attached := executor.AttachedIdentity(ctx, "target-1"); !attached || identity != "identity-1" {
		t.Fatalf("AttachedIdentity = %q, %v; want identity-1, true", identity, attached)
	}
	close(gate)
	// The runner consumes "aa", then the terminal error stops it; re-attach
	// succeeds whether the stale runner is still draining or already done.
	deadline := time.Now().Add(10 * time.Second)
	var fresh *scriptSource
	for {
		fresh = &scriptSource{endSeq: 50, gate: make(chan struct{})}
		if err := executor.AttachTarget(ctx, "target-1", "identity-2", fresh); err == nil {
			break
		} else if !strings.Contains(err.Error(), "already attached") || time.Now().After(deadline) {
			t.Fatalf("re-attach = %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	stale.mu.Lock()
	staleClosed := stale.closed
	stale.mu.Unlock()
	if !staleClosed {
		t.Fatal("the stale source was not closed on replacement")
	}
	// The replacement identity settled the old incarnation's watch.
	row, err := backend.AgentWatchGet(ctx, watch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.AgentWatchStatusUnverifiable {
		t.Fatalf("watch after identity replacement = %s; want unverifiable", row.Status)
	}
	if identity, attached := executor.AttachedIdentity(ctx, "target-1"); !attached || identity != "identity-2" {
		t.Fatalf("AttachedIdentity after re-attach = %q, %v; want identity-2, true", identity, attached)
	}
	// The fresh runner is alive (parked on its gate): a duplicate attach for
	// the same identity is rejected without touching durable state, while a
	// different identity is a live replacement — the running stale runner is
	// cancelled, awaited and closed before the newer identity binds.
	if err := executor.AttachTarget(ctx, "target-1", "identity-2", &scriptSource{}); err == nil || !strings.Contains(err.Error(), "already attached") {
		t.Fatalf("duplicate same-identity attach = %v; want already attached", err)
	}
	live := &scriptSource{gate: make(chan struct{})}
	if err := executor.AttachTarget(ctx, "target-1", "identity-3", live); err != nil {
		t.Fatalf("live replacement attach = %v", err)
	}
	fresh.mu.Lock()
	freshClosed := fresh.closed
	fresh.mu.Unlock()
	if !freshClosed {
		t.Fatal("the live stale source was not closed on replacement")
	}
	if identity, attached := executor.AttachedIdentity(ctx, "target-1"); !attached || identity != "identity-3" {
		t.Fatalf("AttachedIdentity after live replacement = %q, %v; want identity-3, true", identity, attached)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
