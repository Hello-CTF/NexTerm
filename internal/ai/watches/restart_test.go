package watches

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

// The restart contracts below run against a real file-backed SQLite database:
// every engine is recreated from a fresh store handle on the same path, which
// is the closest deterministic stand-in for a process restart.

func openRestartStore(t *testing.T, path string) *store.Store {
	t.Helper()
	backend, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open store %s: %v", path, err)
	}
	return backend
}

func newRestartEngine(t *testing.T, backend *store.Store, clock *testClock) *Engine {
	t.Helper()
	engine, err := New(backend, Options{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestRestartResumesAnchorWithoutSkippingOrRefiring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.db")
	clock := newTestClock()
	ctx := ownerCtx("owner-a")

	backend := openRestartStore(t, path)
	engine := newRestartEngine(t, backend, clock)
	watch := mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ab")); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	backend = openRestartStore(t, path)
	defer backend.Close()
	engine = newRestartEngine(t, backend, clock)
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := engine.Get(ctx, watch.ID)
	if err != nil || reloaded.Status != StatusArmed || reloaded.AnchorSeq != 2 || len(reloaded.Carry) != 0 {
		t.Fatalf("reloaded watch = %+v, %v", reloaded, err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 2, []byte("ready"))
	if err != nil || len(events) != 1 {
		t.Fatalf("observe after restart = %+v, %v", events, err)
	}
	if events[0].MatchedFrom != 2 {
		t.Fatalf("match resumed at %d; want anchor 2", events[0].MatchedFrom)
	}
}

func TestRestartKeepsPendingEventWithFixedIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.db")
	clock := newTestClock()
	ctx := ownerCtx("owner-a")

	backend := openRestartStore(t, path)
	engine := newRestartEngine(t, backend, clock)
	event := fireOne(t, engine, backend, "ready")
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	backend = openRestartStore(t, path)
	defer backend.Close()
	sink := &recordingSink{}
	dispatcher, err := NewDispatcher(backend, sink, Options{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := dispatcher.DispatchOnce(ctx, 10)
	if err != nil || delivered != 1 {
		t.Fatalf("dispatch after restart = %d, %v", delivered, err)
	}
	got := sink.delivered()
	if len(got) != 1 || got[0].DeliveryID != event.DeliveryID || got[0].RunID != event.RunID {
		t.Fatalf("delivered after restart = %+v; want fixed IDs %s/%s", got, event.DeliveryID, event.RunID)
	}
}

func TestRestartAfterLostAckDeliversDuplicateWithSameIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.db")
	clock := newTestClock()
	ctx := ownerCtx("owner-a")

	backend := openRestartStore(t, path)
	engine := newRestartEngine(t, backend, clock)
	event := fireOne(t, engine, backend, "ready")
	wrapped := &ackFailStore{Store: backend, failures: 1}
	sink := &recordingSink{}
	dispatcher, err := NewDispatcher(wrapped, sink, Options{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if delivered, err := dispatcher.DispatchOnce(ctx, 10); err == nil || delivered != 0 {
		t.Fatalf("dispatch with injected ack failure = %d, %v", delivered, err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	backend = openRestartStore(t, path)
	defer backend.Close()
	dispatcher, err = NewDispatcher(backend, sink, Options{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	if delivered, err := dispatcher.DispatchOnce(ctx, 10); err != nil || delivered != 1 {
		t.Fatalf("dispatch after restart = %d, %v", delivered, err)
	}
	got := sink.delivered()
	if len(got) != 2 || got[0].DeliveryID != event.DeliveryID || got[1].DeliveryID != event.DeliveryID ||
		got[0].RunID != event.RunID || got[1].RunID != event.RunID {
		t.Fatalf("duplicate across restart drifted: %+v", got)
	}
}

func TestRestartDoesNotDoubleFire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.db")
	clock := newTestClock()
	ctx := ownerCtx("owner-a")

	backend := openRestartStore(t, path)
	engine := newRestartEngine(t, backend, clock)
	mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready")); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	backend = openRestartStore(t, path)
	defer backend.Close()
	engine = newRestartEngine(t, backend, clock)
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	// The same chunk redelivered after a restart must not fire again.
	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(events) != 0 {
		t.Fatalf("redelivered chunk after restart = %+v, %v", events, err)
	}
	pending, err := backend.AgentWatchEventPendingList(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending after restart = %+v, %v; want exactly 1", pending, err)
	}
}

func TestRestartSplitMatchSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.db")
	clock := newTestClock()
	ctx := ownerCtx("owner-a")

	backend := openRestartStore(t, path)
	engine := newRestartEngine(t, backend, clock)
	mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	// A partial pattern is in flight when the process "restarts".
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("re")); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	backend = openRestartStore(t, path)
	defer backend.Close()
	engine = newRestartEngine(t, backend, clock)
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 2, []byte("ady"))
	if err != nil || len(events) != 1 {
		t.Fatalf("split match after restart = %+v, %v", events, err)
	}
	if events[0].MatchedFrom != 0 || events[0].MatchedTo != 5 {
		t.Fatalf("split match after restart = %d..%d; want 0..5", events[0].MatchedFrom, events[0].MatchedTo)
	}
}

func TestRestartPermanentDetachAfterRestartSettles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.db")
	clock := newTestClock()
	ctx := ownerCtx("owner-a")

	backend := openRestartStore(t, path)
	engine := newRestartEngine(t, backend, clock)
	watch := mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	// The fresh engine has no local bindings; without an explicit Attach it
	// holds no credential, so the permanent detach is rejected until it
	// re-attaches and the durable target row settles the incarnation.
	backend = openRestartStore(t, path)
	defer backend.Close()
	engine = newRestartEngine(t, backend, clock)
	if err := engine.Detach(ctx, "target-1", "identity-1", true); !errors.Is(err, ErrDetached) {
		t.Fatalf("permanent detach without credential = %v; want ErrDetached", err)
	}
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	if err := engine.Detach(ctx, "target-1", "identity-1", true); err != nil {
		t.Fatalf("permanent detach after restart: %v", err)
	}
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusUnverifiable {
		t.Fatalf("watch after restarted permanent detach = %+v, %v", after, err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready")); !errors.Is(err, ErrDetached) {
		t.Fatalf("observe after restarted detach = %v; want ErrDetached", err)
	}
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready")}); !errors.Is(err, ErrDetached) {
		t.Fatalf("create after restarted detach = %v; want ErrDetached", err)
	}
}

func TestRestartStaleDetachAfterRestartIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.db")
	clock := newTestClock()
	ctx := ownerCtx("owner-a")

	backend := openRestartStore(t, path)
	engine := newRestartEngine(t, backend, clock)
	mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	backend = openRestartStore(t, path)
	defer backend.Close()
	engine = newRestartEngine(t, backend, clock)
	// The new incarnation replaces the old one; a detach carrying the old
	// identity afterwards must be rejected as stale.
	if err := engine.Attach(ctx, "target-1", "identity-2"); err != nil {
		t.Fatal(err)
	}
	if err := engine.Detach(ctx, "target-1", "identity-1", true); err != nil {
		t.Fatalf("stale detach after restart: %v", err)
	}
	watch, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil || watch.Identity != "identity-2" {
		t.Fatalf("create after stale detach = %+v, %v", watch, err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-2", 0, []byte("ready"))
	if err != nil || len(events) != 1 {
		t.Fatalf("observe after stale detach = %+v, %v", events, err)
	}
}

func TestRestartIdentityReplacementSettlesImmediately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.db")
	clock := newTestClock()
	ctx := ownerCtx("owner-a")

	backend := openRestartStore(t, path)
	engine := newRestartEngine(t, backend, clock)
	old := mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	backend = openRestartStore(t, path)
	defer backend.Close()
	engine = newRestartEngine(t, backend, clock)
	// Replacement after restart settles the old incarnation without waiting
	// for a later Observe.
	if err := engine.Attach(ctx, "target-1", "identity-2"); err != nil {
		t.Fatal(err)
	}
	after, err := engine.Get(ctx, old.ID)
	if err != nil || after.Status != StatusUnverifiable {
		t.Fatalf("old watch after restarted replacement = %+v, %v", after, err)
	}
}

func TestRestartSplitRuneSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.db")
	clock := newTestClock()
	ctx := ownerCtx("owner-a")

	backend := openRestartStore(t, path)
	engine := newRestartEngine(t, backend, clock)
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `[界x]`}}); err != nil {
		t.Fatal(err)
	}
	// The incomplete rune is durable carry across the restart.
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte{0xE7}); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	backend = openRestartStore(t, path)
	defer backend.Close()
	engine = newRestartEngine(t, backend, clock)
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 1, []byte{0x95, 0x8C})
	if err != nil || len(events) != 1 {
		t.Fatalf("split rune after restart = %+v, %v; want one fire", events, err)
	}
	if events[0].MatchedFrom != 0 || events[0].MatchedTo != 3 {
		t.Fatalf("match = %d..%d; want 0..3", events[0].MatchedFrom, events[0].MatchedTo)
	}
}

func TestRestartKeepsUnverifiableTerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.db")
	clock := newTestClock()
	ctx := ownerCtx("owner-a")

	backend := openRestartStore(t, path)
	engine := newRestartEngine(t, backend, clock)
	watch := mustCreate(t, engine, ctx, "target-1", containsCondition("ready"))
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("12345678")); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 100, []byte("ready")); err != nil {
		t.Fatal(err)
	}
	before, err := engine.Get(ctx, watch.ID)
	if err != nil || before.Status != StatusUnverifiable {
		t.Fatalf("before restart = %+v, %v", before, err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	backend = openRestartStore(t, path)
	defer backend.Close()
	engine = newRestartEngine(t, backend, clock)
	if err := engine.Attach(ctx, "target-1", "identity-1"); err != nil {
		t.Fatal(err)
	}
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusUnverifiable {
		t.Fatalf("after restart = %+v, %v", after, err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 101, []byte("ready"))
	if err != nil || len(events) != 0 {
		t.Fatalf("unverifiable watch fired after restart: %+v, %v", events, err)
	}
}
