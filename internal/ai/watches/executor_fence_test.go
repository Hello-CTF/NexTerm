package watches

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

// A stale attach that prepared before a newer incarnation landed must fail
// with zero side effects: it may not cancel the newer runner, roll the
// incarnation back, settle the newer generation's watches, or close the
// source it never bound.
func TestExecutorStaleDelayedAttachHasZeroSideEffects(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	executor := newTestExecutor(t, backend, newChanSink())

	attachTarget(t, executor, ctx, "target-1", "identity-1", &scriptSource{})
	_, fenceG1, attached := executor.AttachedCredential(ctx, "target-1")
	if !attached || fenceG1 == 0 {
		t.Fatalf("credential after first attach = gen %d, %v", fenceG1, attached)
	}

	// A newer incarnation lands (the recovery reconcile) and a watch is
	// created on it.
	attachTarget(t, executor, ctx, "target-1", "identity-2", &scriptSource{gate: make(chan struct{})})
	watchB, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}

	// The delayed stale attach resumes: it must conflict without touching
	// anything.
	staleSource := &scriptSource{}
	err = executor.AttachTarget(ctx, "target-1", "identity-1", staleSource, fenceG1)
	if err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("stale delayed attach = %v; want ErrConflict", err)
	}
	staleSource.mu.Lock()
	staleClosed := staleSource.closed
	staleSource.mu.Unlock()
	if staleClosed {
		t.Fatal("the executor closed a source it never bound")
	}
	if identity, attached := executor.AttachedIdentity(ctx, "target-1"); !attached || identity != "identity-2" {
		t.Fatalf("mirror after stale attach = %q, %v; want identity-2, true", identity, attached)
	}
	if !executor.RunnerAlive("target-1") {
		t.Fatal("the stale attach cancelled the newer runner")
	}
	row, err := backend.AgentWatchGet(ctx, watchB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.AgentWatchStatusArmed || row.Generation != 2 {
		t.Fatalf("newer watch after stale attach = %+v; want armed at generation 2", row)
	}
	targetRow, err := backend.AgentWatchTargetGet(ctx, "target-1")
	if err != nil {
		t.Fatal(err)
	}
	if targetRow.Identity != "identity-2" || targetRow.Generation != 2 {
		t.Fatalf("durable target after stale attach = %+v; want identity-2 generation 2", targetRow)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// gatedTailSource blocks the Tail call until released, so a test can hold a
// Watch create between runner acquisition and creation.
type gatedTailSource struct {
	*scriptSource
	entered chan struct{}
	release chan struct{}
	once    func()
}

func (s *gatedTailSource) Tail(ctx context.Context, beforeSeq int64, maxBytes int) ([]byte, error) {
	if s.once != nil {
		s.once()
		s.once = nil
	}
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.scriptSource.Tail(ctx, beforeSeq, maxBytes)
}

// Watch creation and the replacement attach form one serialized lifecycle:
// the create that fetched the old runner finishes with the old
// incarnation's coordinates and generation (correctly settled by the
// subsequent replacement), never signing old coordinates into the new
// generation; the replacement cannot interpose.
func TestExecutorWatchCreateSerializedWithReplacement(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	executor := newTestExecutor(t, backend, newChanSink())

	source := &gatedTailSource{
		scriptSource: &scriptSource{endSeq: 8, tail: []byte("history-")},
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	attachTarget(t, executor, ctx, "target-1", "identity-1", source)
	_, fenceG1, _ := executor.AttachedCredential(ctx, "target-1")

	createDone := make(chan struct {
		watch Watch
		err   error
	}, 1)
	go func() {
		watch, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready")})
		createDone <- struct {
			watch Watch
			err   error
		}{watch, err}
	}()
	<-source.entered

	// The replacement attach must wait for the create's lifecycle section: it
	// cannot interpose between the runner fetch and the create.
	attachDone := make(chan error, 1)
	go func() {
		attachDone <- executor.AttachTarget(ctx, "target-1", "identity-2", &scriptSource{gate: make(chan struct{})}, fenceG1)
	}()
	select {
	case err := <-attachDone:
		t.Fatalf("the replacement interposed during the create: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if identity, _ := executor.AttachedIdentity(ctx, "target-1"); identity != "identity-1" {
		t.Fatalf("mirror moved to %q while the create held the lifecycle section", identity)
	}

	close(source.release)
	created := <-createDone
	if created.err != nil {
		t.Fatal(created.err)
	}
	if created.watch.Generation != fenceG1 || created.watch.Identity != "identity-1" {
		t.Fatalf("created watch = identity %q generation %d; want identity-1 generation %d", created.watch.Identity, created.watch.Generation, fenceG1)
	}
	if err := <-attachDone; err != nil {
		t.Fatal(err)
	}
	// The replacement settles the old create honestly; nothing carries its
	// coordinates at the new generation.
	row, err := backend.AgentWatchGet(ctx, created.watch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Generation != fenceG1 || row.Status != store.AgentWatchStatusUnverifiable {
		t.Fatalf("old create after replacement = %+v; want generation %d unverifiable", row, fenceG1)
	}

	// A create after the replacement binds the new stream's coordinates.
	after, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}
	if after.Generation != fenceG1+1 || after.Identity != "identity-2" {
		t.Fatalf("post-replacement create = identity %q generation %d; want identity-2 generation %d", after.Identity, after.Generation, fenceG1+1)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// The clipped-tail boundary: with the anchor's full left context evicted,
// the source honestly returns only the retained prefix, and the create fails
// instead of persisting shifted context that would let a boundary assertion
// misfire. Counterexample, retained [96,104) = "abcdXYZ " with anchor 100:
// the byte at 99 is 'd', so \bXYZ must not match at 100; a shifted
// eight-byte context ending in the space at 103 would invent that boundary.
func TestExecutorCreateFailsOnUnverifiableLeftContext(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	sink := newChanSink()
	executor := newTestExecutor(t, backend, sink)
	// The source's stream already rolled everything before 96 away and its
	// tail honestly reports only the four retained bytes before the anchor.
	source := &scriptSource{
		steps:  []scriptStep{{data: []byte("XYZ ")}},
		endSeq: 100,
		tail:   []byte("abcd"),
		gate:   make(chan struct{}),
	}
	attachTarget(t, executor, ctx, "target-1", "identity-1", source)
	if _, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: Condition{Type: ConditionRegex, Pattern: `\bXYZ`}}); err == nil {
		t.Fatal("create succeeded with an unverifiable left context")
	}
	rows, err := backend.AgentWatchListByTarget(ctx, "target-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("a watch with shifted left context was persisted: %+v", rows)
	}
	if err := executor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if delivered, err := executor.DispatchOnce(ctx); err != nil || delivered != 0 {
		t.Fatalf("dispatch = %d, %v; the shifted-context watch must never exist to fire", delivered, err)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// Two executors over one store: a stale attach delayed past another
// executor's incarnation advance must conflict with zero side effects — the
// durable precheck and the transaction CAS, never the local mirror.
func TestExecutorStaleAttachAcrossExecutorsConflictsAtTheDurableFence(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	executorA := newTestExecutor(t, backend, newChanSink())
	executorB := newTestExecutor(t, backend, newChanSink())

	attachTarget(t, executorA, ctx, "target-1", "identity-1", &scriptSource{gate: make(chan struct{})})
	rowG1, err := backend.AgentWatchTargetGet(ctx, "target-1")
	if err != nil {
		t.Fatal(err)
	}
	// Executor B advances the incarnation and arms a watch on the new stream.
	attachTarget(t, executorB, ctx, "target-1", "identity-2", &scriptSource{gate: make(chan struct{})})
	watchB, err := executorB.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}

	// Executor A's delayed attach prepared at generation 1 resumes: its local
	// mirror still says g1, but the durable row is at g2, so the durable fence
	// must reject it before it can cancel B's runner or settle B's watch.
	staleSource := &scriptSource{}
	err = executorA.AttachTarget(ctx, "target-1", "identity-1", staleSource, rowG1.Generation)
	if err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-executor stale attach = %v; want ErrConflict", err)
	}
	staleSource.mu.Lock()
	staleClosed := staleSource.closed
	staleSource.mu.Unlock()
	if staleClosed {
		t.Fatal("the executor closed a source it never bound")
	}
	if !executorB.RunnerAlive("target-1") {
		t.Fatal("the stale attach cancelled the other executor's live runner")
	}
	row, err := backend.AgentWatchGet(ctx, watchB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.AgentWatchStatusArmed || row.Generation != 2 {
		t.Fatalf("watch B after the stale attach = %+v; want armed at generation 2", row)
	}
	targetRow, err := backend.AgentWatchTargetGet(ctx, "target-1")
	if err != nil {
		t.Fatal(err)
	}
	if targetRow.Identity != "identity-2" || targetRow.Generation != 2 {
		t.Fatalf("durable target after the stale attach = %+v; want identity-2 generation 2", targetRow)
	}
	if err := executorA.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := executorB.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// The ^ seam shares the clipped-context honesty: a left context shorter than
// the anchor window must fail the create, never let a begin-assertion
// evaluate against bytes taken from after the anchor.
func TestExecutorCreateFailsOnUnverifiableBeginAssertionContext(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	executor := newTestExecutor(t, backend, newChanSink())
	// The honest clipped tail has only five bytes before the anchor; the full
	// eight-byte window is unverifiable, so ^-anchored creation must fail.
	source := &scriptSource{endSeq: 100, tail: []byte("abXYZ")}
	attachTarget(t, executor, ctx, "target-1", "identity-1", source)
	if _, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: Condition{Type: ConditionRegex, Pattern: `^XYZ`}}); err == nil {
		t.Fatal("^ create succeeded with an unverifiable left context")
	}
	rows, err := backend.AgentWatchListByTarget(ctx, "target-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("a ^ watch with shifted context was persisted: %+v", rows)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// The UTF-8 seam: a clipped tail ending mid-rune anchors at the rune start
// with the incomplete sequence carried, so the completed rune is evaluated
// at its true position instead of the anchor swallowing post-anchor bytes.
func TestExecutorCreateRetreatsAnchorAcrossAMidRuneSeam(t *testing.T) {
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	ctx := ownerCtx("owner-a")
	sink := newChanSink()
	executor := newTestExecutor(t, backend, sink)
	// The clipped tail is the full window ending with the first two bytes
	// of 力; the anchor must retreat to the rune start at 98 with the
	// incomplete sequence carried, and the chunk supplying the rune's final
	// byte fires at the true rune start.
	gate := make(chan struct{})
	source := &scriptSource{
		steps:  []scriptStep{{data: append([]byte{0x9B}, []byte(" ready")...)}},
		endSeq: 100,
		tail:   []byte{'a', 'a', 'a', 'a', 'a', 'a', 0xE5, 0x8A},
		gate:   gate,
	}
	attachTarget(t, executor, ctx, "target-1", "identity-1", source)
	watch, err := executor.Watch(ctx, WatchParams{TargetID: "target-1", Condition: containsCondition("力 ready")})
	if err != nil {
		t.Fatal(err)
	}
	if watch.AnchorSeq != 98 {
		t.Fatalf("anchor = %d; want the rune start 98", watch.AnchorSeq)
	}
	close(gate)
	if err := executor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	data := waitDelivery(t, sink)
	if data.Event.WatchID != watch.ID || data.Event.MatchedFrom != 98 || data.Event.MatchedTo != 107 {
		t.Fatalf("delivered event = %+v; want the match at the true rune start 98..107", data.Event)
	}
	if err := executor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
