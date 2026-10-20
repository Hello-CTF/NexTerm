package watches

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

// gateStore blocks the first Consume call until released, deterministically
// parking an Observe between reading a matched repeat watch and consuming
// its chunk so an Ack can commit in between.
type gateStore struct {
	Store
	consumeEntered chan struct{}
	consumeRelease chan struct{}
	once           sync.Once
}

func newGateStore(backend Store) *gateStore {
	return &gateStore{Store: backend, consumeEntered: make(chan struct{}), consumeRelease: make(chan struct{})}
}

func (s *gateStore) AgentWatchConsume(ctx context.Context, watchID string, expectedRevision uint64, identity string, anchorSeq, now int64) (model.AgentWatchRow, error) {
	s.once.Do(func() { close(s.consumeEntered) })
	<-s.consumeRelease
	return s.Store.AgentWatchConsume(ctx, watchID, expectedRevision, identity, anchorSeq, now)
}

func TestObserveSplitRuneCharClassAcrossChunks(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `[界x]`}}); err != nil {
		t.Fatal(err)
	}
	// 界 is [0xE7 0x95 0x8C]; the split lands inside the rune.
	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte{0xE7})
	if err != nil || len(events) != 0 {
		t.Fatalf("partial rune chunk = %+v, %v", events, err)
	}
	events, err = engine.Observe(ctx, "target-1", "identity-1", 1, []byte{0x95, 0x8C})
	if err != nil || len(events) != 1 {
		t.Fatalf("completed rune chunk = %+v, %v; want one fire", events, err)
	}
	if events[0].MatchedFrom != 0 || events[0].MatchedTo != 3 {
		t.Fatalf("match = %d..%d; want 0..3", events[0].MatchedFrom, events[0].MatchedTo)
	}
}

func TestObserveDotDoesNotFireOnPartialRune(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `.`}}); err != nil {
		t.Fatal(err)
	}
	// A lone lead byte is not RuneError data yet: "." must not fire on it.
	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte{0xE7})
	if err != nil || len(events) != 0 {
		t.Fatalf("dot fired on a partial rune: %+v, %v", events, err)
	}
	events, err = engine.Observe(ctx, "target-1", "identity-1", 1, []byte{0x95, 0x8C})
	if err != nil || len(events) != 1 {
		t.Fatalf("dot on completed rune = %+v, %v; want one fire", events, err)
	}
	if events[0].MatchedFrom != 0 || events[0].MatchedTo != 3 {
		t.Fatalf("match = %d..%d; want 0..3", events[0].MatchedFrom, events[0].MatchedTo)
	}
}

func TestObserveSplitCaseFoldedLiteralAcrossChunks(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `(?i)ready`}}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("RE")); err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 2, []byte("ADY"))
	if err != nil || len(events) != 1 {
		t.Fatalf("case-folded split = %+v, %v; want one fire", events, err)
	}
	if events[0].MatchedFrom != 0 || events[0].MatchedTo != 5 {
		t.Fatalf("match = %d..%d; want 0..5", events[0].MatchedFrom, events[0].MatchedTo)
	}
}

func TestRuneTailPreservedEvenBelowCarryCap(t *testing.T) {
	clock := newTestClock()
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	// A cap smaller than one rune: the incomplete tail must still be
	// preserved because it is below any pattern-bound tradeoff.
	engine, err := New(backend, Options{Now: clock.Now, MaxCarryBytes: 2})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ownerCtx("owner-a")
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `[界x]`}}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte{0xE7}); err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 1, []byte{0x95, 0x8C})
	if err != nil || len(events) != 1 {
		t.Fatalf("split rune with tiny cap = %+v, %v; want one fire", events, err)
	}
}

func TestTwoOwnersSameTargetSameIdentityIndependentGenerations(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctxA := ownerCtx("owner-a")
	ctxB := ownerCtx("owner-b")
	// Owner B attaches the shared target first and arms a watch at
	// generation 1.
	if err := engine.Attach(ctxB, "target-1", "same"); err != nil {
		t.Fatal(err)
	}
	watchB, err := engine.Create(ctxB, CreateParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil || watchB.Generation != 1 {
		t.Fatalf("watch B = %+v, %v", watchB, err)
	}
	// Owner A then churns its own incarnation of the same target to
	// generation 2 under the same identity string.
	if err := engine.Attach(ctxA, "target-1", "a"); err != nil {
		t.Fatal(err)
	}
	if err := engine.Attach(ctxA, "target-1", "same"); err != nil {
		t.Fatal(err)
	}
	// B's observe must be evaluated against B's own mirror: A's newer
	// generation must never settle B's watch.
	events, err := engine.Observe(ctxB, "target-1", "same", 0, []byte("ready"))
	if err != nil || len(events) != 1 {
		t.Fatalf("owner-b observe = %+v, %v", events, err)
	}
	if events[0].WatchID != watchB.ID {
		t.Fatalf("fired watch = %s; want %s", events[0].WatchID, watchB.ID)
	}
	// A's own lifecycle continues independently.
	watchA, err := engine.Create(ctxA, CreateParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil || watchA.Generation != 2 {
		t.Fatalf("watch A = %+v, %v", watchA, err)
	}
	events, err = engine.Observe(ctxA, "target-1", "same", 0, []byte("ready"))
	if err != nil || len(events) != 1 || events[0].WatchID != watchA.ID {
		t.Fatalf("owner-a observe = %+v, %v", events, err)
	}
}

func TestSameIdentityPresenterCannotAdoptReincarnation(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	stale, err := New(backend, Options{Now: newTestClock().Now})
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	// The current incarnation permanently detaches and then re-attaches,
	// producing a new generation under the same identity string.
	if err := engine.Detach(ctx, "target-1", "identity-1", true); err != nil {
		t.Fatal(err)
	}
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	fresh, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}
	// The revoked presenter must not adopt the reincarnation by observing:
	// only an explicit successful Attach may advance its mirror.
	if _, err := stale.Observe(ctx, "target-1", "identity-1", 0, []byte("ready")); !errors.Is(err, ErrDetached) {
		t.Fatalf("revoked presenter observe = %v; want ErrDetached", err)
	}
	if after, err := engine.Get(ctx, fresh.ID); err != nil || after.Status != StatusArmed {
		t.Fatalf("fresh watch after revoked observe = %+v, %v", after, err)
	}
	if err := stale.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	events, err := stale.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(events) != 1 {
		t.Fatalf("explicitly re-attached observe = %+v, %v", events, err)
	}
}

func TestAckDuringConsumeReloadsAndReEvaluates(t *testing.T) {
	clock := newTestClock()
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	gated := newGateStore(backend)
	engine, err := New(gated, Options{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ownerCtx("owner-a")
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	watch, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready"), Repeat: true})
	if err != nil {
		t.Fatal(err)
	}
	first, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(first) != 1 {
		t.Fatalf("first fire = %+v, %v", first, err)
	}

	// Park the next Observe inside Consume, commit the Ack (re-arming the
	// watch), then release: the engine must reload and re-evaluate the
	// chunk instead of swallowing the conflict.
	type observeResult struct {
		events []Event
		err    error
	}
	result := make(chan observeResult, 1)
	go func() {
		events, err := engine.Observe(ctx, "target-1", "identity-1", 5, []byte("xxready"))
		result <- observeResult{events: events, err: err}
	}()
	<-gated.consumeEntered
	if _, err := backend.AgentWatchEventAck(ctx, first[0].DeliveryID, 1, 5000); err != nil {
		t.Fatal(err)
	}
	close(gated.consumeRelease)
	out := <-result
	if out.err != nil {
		t.Fatalf("observe with ack-during-consume: %v", out.err)
	}
	if len(out.events) != 1 {
		t.Fatalf("chunk was dropped after ack-during-consume: %+v", out.events)
	}
	if out.events[0].MatchedFrom != 7 || out.events[0].MatchedTo != 12 {
		t.Fatalf("re-evaluated match = %d..%d; want 7..12", out.events[0].MatchedFrom, out.events[0].MatchedTo)
	}
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusMatched || after.AnchorSeq != 12 {
		t.Fatalf("watch after ack-during-consume = %+v, %v", after, err)
	}
	// The next contiguous chunk is not a false gap.
	events, err := engine.Observe(ctx, "target-1", "identity-1", 12, []byte("tail"))
	if err != nil || len(events) != 0 {
		t.Fatalf("chunk after recovery = %+v, %v", events, err)
	}
}
