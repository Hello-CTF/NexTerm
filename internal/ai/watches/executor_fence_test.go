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
