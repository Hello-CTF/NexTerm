package watches

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func mustAttach(t *testing.T, engine *Engine, ctx context.Context, target, identity string) {
	t.Helper()
	if err := engine.Attach(ctx, target, identity); err != nil {
		t.Fatal(err)
	}
}

func mustCreateRepeat(t *testing.T, engine *Engine, ctx context.Context, target, pattern string) Watch {
	t.Helper()
	watch, err := engine.Create(ctx, CreateParams{TargetID: target, Condition: containsCondition(pattern), Repeat: true})
	if err != nil {
		t.Fatal(err)
	}
	return watch
}

func TestAckDoesNotReArmAfterPermanentDetach(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	stale, err := New(backend, Options{Now: newTestClock().Now})
	if err != nil {
		t.Fatal(err)
	}
	mustAttach(t, stale, ctx, "target-1", "identity-1")
	watch := mustCreateRepeat(t, stale, ctx, "target-1", "ready")
	first, err := stale.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(first) != 1 {
		t.Fatalf("first fire = %+v, %v", first, err)
	}
	// The current incarnation permanently detaches while the fire awaits
	// acknowledgement; it must hold its own credential to do so.
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	if err := engine.Detach(ctx, "target-1", "identity-1", true); err != nil {
		t.Fatal(err)
	}
	// The produced event still acks...
	if _, err := backend.AgentWatchEventAck(ctx, first[0].DeliveryID, 1, 5000); err != nil {
		t.Fatal(err)
	}
	// ...but the revoked incarnation's watch is terminated, never re-armed.
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusUnverifiable {
		t.Fatalf("watch after detached ack = %+v, %v", after, err)
	}
	events, err := stale.Observe(ctx, "target-1", "identity-1", 5, []byte("ready"))
	if err != nil || len(events) != 0 {
		t.Fatalf("revoked watch fired after detached ack: %+v, %v", events, err)
	}
}

func TestAckDoesNotReArmAfterPermanentDetachRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.db")
	clock := newTestClock()
	ctx := ownerCtx("owner-a")

	backend := openRestartStore(t, path)
	engine := newRestartEngine(t, backend, clock)
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	watch := mustCreateRepeat(t, engine, ctx, "target-1", "ready")
	first, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(first) != 1 {
		t.Fatalf("first fire = %+v, %v", first, err)
	}
	if err := engine.Detach(ctx, "target-1", "identity-1", true); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	// After a restart the durable target row still terminates the old
	// incarnation's watch on acknowledgement instead of re-arming it.
	backend = openRestartStore(t, path)
	defer backend.Close()
	if _, err := backend.AgentWatchEventAck(ctx, first[0].DeliveryID, 1, 5000); err != nil {
		t.Fatal(err)
	}
	after, err := backend.AgentWatchGet(ctx, watch.ID)
	if err != nil || after.Status != store.AgentWatchStatusUnverifiable {
		t.Fatalf("watch after restarted detached ack = %+v, %v", after, err)
	}
}

func TestSameIdentityStaleDetachAndCreateFail(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	stale, err := New(backend, Options{Now: newTestClock().Now})
	if err != nil {
		t.Fatal(err)
	}
	mustAttach(t, stale, ctx, "target-1", "identity-1")
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	if err := engine.Detach(ctx, "target-1", "identity-1", true); err != nil {
		t.Fatal(err)
	}
	// A same-identity reincarnation takes over.
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	fresh, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}
	// The stale presenter's permanent detach must fail, not settle the new
	// incarnation's watch.
	if err := stale.Detach(ctx, "target-1", "identity-1", true); !errors.Is(err, ErrDetached) {
		t.Fatalf("stale permanent detach = %v; want ErrDetached", err)
	}
	if after, err := engine.Get(ctx, fresh.ID); err != nil || after.Status != StatusArmed {
		t.Fatalf("fresh watch after stale detach = %+v, %v", after, err)
	}
	// Its creates fail too until it explicitly re-attaches.
	if _, err := stale.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready")}); !errors.Is(err, ErrDetached) {
		t.Fatalf("stale create = %v; want ErrDetached", err)
	}
	if _, err := stale.Observe(ctx, "target-1", "identity-1", 0, []byte("ready")); !errors.Is(err, ErrDetached) {
		t.Fatalf("stale observe = %v; want ErrDetached", err)
	}
	// An explicit re-attach grants the current credential and everything
	// works again.
	mustAttach(t, stale, ctx, "target-1", "identity-1")
	created, err := stale.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil || created.Generation != fresh.Generation {
		t.Fatalf("re-attached create = %+v, %v", created, err)
	}
}

func TestCaretAssertionSeesTrueLeftContext(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `^ready`}}); err != nil {
		t.Fatal(err)
	}
	if events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("xxxxread")); err != nil || len(events) != 0 {
		t.Fatalf("first chunk = %+v, %v", events, err)
	}
	// The buffer seam is not begin-text: no false ^ match may fire.
	events, err := engine.Observe(ctx, "target-1", "identity-1", 8, []byte("y"))
	if err != nil || len(events) != 0 {
		t.Fatalf("^ matched at a fabricated text start: %+v, %v", events, err)
	}
}

func TestWordBoundarySeesTrueLeftContext(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `\b[0-9]`}}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("a")); err != nil {
		t.Fatal(err)
	}
	// "a1" has no word boundary between the two word characters.
	events, err := engine.Observe(ctx, "target-1", "identity-1", 1, []byte("1"))
	if err != nil || len(events) != 0 {
		t.Fatalf("\b matched across a fabricated boundary: %+v, %v", events, err)
	}
}

func TestAssertionResultsAreChunkInvariant(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		stream  string
		from    int64
		to      int64
		// eof marks matches that may only fire at the explicit stream end:
		// right-edge assertions are deferred until following bytes or EOF.
		eof bool
	}{
		{"caret no false start", `^ready`, "xxready", -1, -1, false},
		{"caret real start", `^ready`, "ready", 0, 5, false},
		{"word boundary none", `\b[0-9]`, "a1", -1, -1, false},
		{"word boundary real", `\b[0-9]`, " 1", 1, 2, true},
		{"line caret split", `(?m)^ready`, "ab\nready", 3, 8, false},
		{"trailing boundary", `ready\b`, "x ready y", 2, 7, false},
		{"end text", `ready$`, "ab ready", 3, 8, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			run := func(t *testing.T, chunks []string, reopen bool) []Event {
				clock := newTestClock()
				ctx := ownerCtx("owner-a")
				restartPath := filepath.Join(t.TempDir(), "watch.db")
				var backend *store.Store
				if reopen {
					backend = openRestartStore(t, restartPath)
				} else {
					var err error
					backend, err = store.OpenInMemory(context.Background())
					if err != nil {
						t.Fatal(err)
					}
				}
				t.Cleanup(func() { _ = backend.Close() })
				newEngine := func() *Engine {
					engine, err := New(backend, Options{Now: clock.Now})
					if err != nil {
						t.Fatal(err)
					}
					mustAttach(t, engine, ctx, "target-1", "identity-1")
					return engine
				}
				engine := newEngine()
				if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
					Condition: Condition{Type: ConditionRegex, Pattern: testCase.pattern}}); err != nil {
					t.Fatal(err)
				}
				var events []Event
				position := int64(0)
				for index, chunk := range chunks {
					if reopen && index > 0 {
						if err := backend.Close(); err != nil {
							t.Fatal(err)
						}
						backend = openRestartStore(t, restartPath)
						engine = newEngine()
					}
					fired, err := engine.Observe(ctx, "target-1", "identity-1", position, []byte(chunk))
					if err != nil {
						t.Fatalf("observe %q: %v", chunk, err)
					}
					events = append(events, fired...)
					position += int64(len(chunk))
				}
				if testCase.eof {
					fired, err := engine.ObserveEOF(ctx, "target-1", "identity-1", position)
					if err != nil {
						t.Fatalf("observe EOF: %v", err)
					}
					events = append(events, fired...)
				}
				return events
			}

			contiguous := run(t, []string{testCase.stream}, false)
			assertEvents(t, contiguous, testCase.from, testCase.to)
			for split := 1; split < len(testCase.stream); split++ {
				chunked := run(t, []string{testCase.stream[:split], testCase.stream[split:]}, false)
				assertEvents(t, chunked, testCase.from, testCase.to)
				restarted := run(t, []string{testCase.stream[:split], testCase.stream[split:]}, true)
				assertEvents(t, restarted, testCase.from, testCase.to)
			}
		})
	}
}

func assertEvents(t *testing.T, events []Event, from, to int64) {
	t.Helper()
	if from < 0 {
		if len(events) != 0 {
			t.Fatalf("events = %+v; want none", events)
		}
		return
	}
	if len(events) != 1 || events[0].MatchedFrom != from || events[0].MatchedTo != to {
		t.Fatalf("events = %+v; want one match %d..%d", events, from, to)
	}
}

func TestGapWhileMatchedSettlesRepeat(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	watch := mustCreateRepeat(t, engine, ctx, "target-1", "ready")
	first, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(first) != 1 {
		t.Fatalf("first fire = %+v, %v", first, err)
	}
	// A gap while matched is a durable continuity failure, not a silent miss.
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 100, []byte("x")); err != nil {
		t.Fatalf("gap observe: %v", err)
	}
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusUnverifiable {
		t.Fatalf("watch after gap while matched = %+v, %v", after, err)
	}
	// The pending event still acks, but the watch never re-arms.
	if _, err := backend.AgentWatchEventAck(ctx, first[0].DeliveryID, 1, 5000); err != nil {
		t.Fatal(err)
	}
	after, err = engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusUnverifiable {
		t.Fatalf("watch after gap ack = %+v, %v", after, err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 101, []byte("ready"))
	if err != nil || len(events) != 0 {
		t.Fatalf("settled watch fired: %+v, %v", events, err)
	}
}

func TestGapWhileMatchedSettlesRepeatRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.db")
	clock := newTestClock()
	ctx := ownerCtx("owner-a")

	backend := openRestartStore(t, path)
	engine := newRestartEngine(t, backend, clock)
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	watch := mustCreateRepeat(t, engine, ctx, "target-1", "ready")
	first, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(first) != 1 {
		t.Fatalf("first fire = %+v, %v", first, err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 100, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	backend = openRestartStore(t, path)
	defer backend.Close()
	after, err := backend.AgentWatchGet(ctx, watch.ID)
	if err != nil || after.Status != store.AgentWatchStatusUnverifiable {
		t.Fatalf("watch after restarted gap = %+v, %v", after, err)
	}
	if _, err := backend.AgentWatchEventAck(ctx, first[0].DeliveryID, 1, 5000); err != nil {
		t.Fatal(err)
	}
	after, err = backend.AgentWatchGet(ctx, watch.ID)
	if err != nil || after.Status != store.AgentWatchStatusUnverifiable {
		t.Fatalf("watch after restarted gap ack = %+v, %v", after, err)
	}
}

func TestLifecycleCredentialMatrix(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	stale, err := New(backend, Options{Now: newTestClock().Now})
	if err != nil {
		t.Fatal(err)
	}
	target, identity := "target-1", "identity-1"

	// Generation 1: both presenters attach; both may create.
	mustAttach(t, stale, ctx, target, identity)
	mustAttach(t, engine, ctx, target, identity)
	first, err := stale.Create(ctx, CreateParams{TargetID: target, Condition: containsCondition("ready")})
	if err != nil || first.Generation != 1 {
		t.Fatalf("gen1 create = %+v, %v", first, err)
	}
	// Permanent detach revokes the incarnation; every lifecycle operation of
	// the stale presenter now fails until it re-attaches.
	if err := engine.Detach(ctx, target, identity, true); err != nil {
		t.Fatal(err)
	}
	if after, err := engine.Get(ctx, first.ID); err != nil || after.Status != StatusUnverifiable {
		t.Fatalf("watch after detach = %+v, %v", after, err)
	}
	if err := stale.Detach(ctx, target, identity, true); !errors.Is(err, ErrDetached) {
		t.Fatalf("stale detach after revoke = %v; want ErrDetached", err)
	}
	if _, err := stale.Create(ctx, CreateParams{TargetID: target, Condition: containsCondition("ready")}); !errors.Is(err, ErrDetached) {
		t.Fatalf("stale create after revoke = %v; want ErrDetached", err)
	}
	// The current presenter re-attaches (new generation) and arms a watch.
	mustAttach(t, engine, ctx, target, identity)
	fresh, err := engine.Create(ctx, CreateParams{TargetID: target, Condition: containsCondition("ready")})
	if err != nil || fresh.Generation != 3 {
		t.Fatalf("gen3 create = %+v, %v", fresh, err)
	}
	// The stale presenter is rejected everywhere until it re-attaches.
	if _, err := stale.Observe(ctx, target, identity, 0, []byte("ready")); !errors.Is(err, ErrDetached) {
		t.Fatalf("stale observe = %v; want ErrDetached", err)
	}
	if err := stale.Detach(ctx, target, identity, true); !errors.Is(err, ErrDetached) {
		t.Fatalf("stale detach = %v; want ErrDetached", err)
	}
	if _, err := stale.Create(ctx, CreateParams{TargetID: target, Condition: containsCondition("ready")}); !errors.Is(err, ErrDetached) {
		t.Fatalf("stale create = %v; want ErrDetached", err)
	}
	if after, err := engine.Get(ctx, fresh.ID); err != nil || after.Status != StatusArmed {
		t.Fatalf("fresh watch after stale operations = %+v, %v", after, err)
	}
	// Explicit re-attach grants the current credential.
	mustAttach(t, stale, ctx, target, identity)
	events, err := stale.Observe(ctx, target, identity, 0, []byte("ready"))
	if err != nil || len(events) != 1 || events[0].WatchID != fresh.ID {
		t.Fatalf("re-attached observe = %+v, %v", events, err)
	}
	// And a freshly created watch of its own is settled when it detaches
	// with the current credential.
	second, err := stale.Create(ctx, CreateParams{TargetID: target, Condition: containsCondition("later")})
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.Detach(ctx, target, identity, true); err != nil {
		t.Fatalf("credential detach: %v", err)
	}
	if after, err := engine.Get(ctx, second.ID); err != nil || after.Status != StatusUnverifiable {
		t.Fatalf("second watch after final detach = %+v, %v", after, err)
	}
}
