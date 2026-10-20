package watches

import (
	"context"

	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestRepeatCaretDoesNotFalseFireAfterAck(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	watch, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `^ready`}, Repeat: true})
	if err != nil {
		t.Fatal(err)
	}
	first, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(first) != 1 || first[0].MatchedFrom != 0 {
		t.Fatalf("first fire = %+v, %v", first, err)
	}
	if _, err := backend.AgentWatchEventAck(ctx, first[0].DeliveryID, 1, 5000); err != nil {
		t.Fatal(err)
	}
	// The re-armed watch must see the real left context: "readyready" has no
	// begin-text "ready" at position 5.
	events, err := engine.Observe(ctx, "target-1", "identity-1", 5, []byte("ready"))
	if err != nil || len(events) != 0 {
		t.Fatalf("^ fired at a fabricated text start after re-arm: %+v, %v", events, err)
	}
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusArmed {
		t.Fatalf("watch after re-armed observe = %+v, %v", after, err)
	}
}

func TestRepeatCaretDoesNotFalseFireAfterAckRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.db")
	clock := newTestClock()
	ctx := ownerCtx("owner-a")

	backend := openRestartStore(t, path)
	engine := newRestartEngine(t, backend, clock)
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `^ready`}, Repeat: true}); err != nil {
		t.Fatal(err)
	}
	first, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("ready"))
	if err != nil || len(first) != 1 {
		t.Fatalf("first fire = %+v, %v", first, err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	backend = openRestartStore(t, path)
	defer backend.Close()
	if _, err := backend.AgentWatchEventAck(ctx, first[0].DeliveryID, 1, 5000); err != nil {
		t.Fatal(err)
	}
	engine = newRestartEngine(t, backend, clock)
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	events, err := engine.Observe(ctx, "target-1", "identity-1", 5, []byte("ready"))
	if err != nil || len(events) != 0 {
		t.Fatalf("^ fired at a fabricated text start after restart: %+v, %v", events, err)
	}
}

func TestMatchedConsumePreservesSplitRune(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `[界]`}, Repeat: true}); err != nil {
		t.Fatal(err)
	}
	first, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("界"))
	if err != nil || len(first) != 1 || first[0].MatchedTo != 3 {
		t.Fatalf("first fire = %+v, %v", first, err)
	}
	// A rune starts mid-way through the matched window; the suspended watch
	// must preserve the incomplete tail with the anchor at the rune start.
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 3, []byte{0xE7}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchEventAck(ctx, first[0].DeliveryID, 1, 5000); err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 4, []byte{0x95, 0x8C})
	if err != nil || len(events) != 1 {
		t.Fatalf("completed rune after consume = %+v, %v; want one fire", events, err)
	}
	if events[0].MatchedFrom != 3 || events[0].MatchedTo != 6 {
		t.Fatalf("match = %d..%d; want 3..6", events[0].MatchedFrom, events[0].MatchedTo)
	}
}

func TestMatchedConsumePreservesSplitRuneDot(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `.`}, Repeat: true}); err != nil {
		t.Fatal(err)
	}
	first, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("a"))
	if err != nil || len(first) != 1 {
		t.Fatalf("first fire = %+v, %v", first, err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 1, []byte{0xE7}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchEventAck(ctx, first[0].DeliveryID, 1, 5000); err != nil {
		t.Fatal(err)
	}
	// Continuation bytes are not RuneError data: only the completed rune
	// may fire, and the already-consumed "a" must not fire again.
	events, err := engine.Observe(ctx, "target-1", "identity-1", 2, []byte{0x95, 0x8C})
	if err != nil || len(events) != 1 {
		t.Fatalf("dot after consume = %+v, %v; want one fire", events, err)
	}
	if events[0].MatchedFrom != 1 || events[0].MatchedTo != 4 {
		t.Fatalf("match = %d..%d; want 1..4 (the completed rune, not RuneError)", events[0].MatchedFrom, events[0].MatchedTo)
	}
}

func TestMatchedConsumePreservesSplitRuneRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.db")
	clock := newTestClock()
	ctx := ownerCtx("owner-a")

	backend := openRestartStore(t, path)
	engine := newRestartEngine(t, backend, clock)
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `[界]`}, Repeat: true}); err != nil {
		t.Fatal(err)
	}
	first, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("界"))
	if err != nil || len(first) != 1 {
		t.Fatalf("first fire = %+v, %v", first, err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 3, []byte{0xE7}); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	backend = openRestartStore(t, path)
	defer backend.Close()
	if _, err := backend.AgentWatchEventAck(ctx, first[0].DeliveryID, 1, 5000); err != nil {
		t.Fatal(err)
	}
	engine = newRestartEngine(t, backend, clock)
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	events, err := engine.Observe(ctx, "target-1", "identity-1", 4, []byte{0x95, 0x8C})
	if err != nil || len(events) != 1 || events[0].MatchedFrom != 3 || events[0].MatchedTo != 6 {
		t.Fatalf("completed rune after restarted consume = %+v, %v", events, err)
	}
}

func TestFoldedLiteralSplitMatch(t *testing.T) {
	kelvin := "K"
	stream := kelvin + kelvin
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
			Condition: Condition{Type: ConditionRegex, Pattern: `(?i)kk`}}); err != nil {
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
		return events
	}

	contiguous := run(t, []string{stream}, false)
	assertEvents(t, contiguous, 0, 6)
	for split := 1; split < len(stream); split++ {
		chunked := run(t, []string{stream[:split], stream[split:]}, false)
		assertEvents(t, chunked, 0, 6)
		restarted := run(t, []string{stream[:split], stream[split:]}, true)
		assertEvents(t, restarted, 0, 6)
	}
}

func TestNullableRepetitionSplitMatch(t *testing.T) {
	run := func(t *testing.T, chunks []string, reopen bool, capBytes int) []Event {
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
			engine, err := New(backend, Options{Now: clock.Now, MaxCarryBytes: capBytes})
			if err != nil {
				t.Fatal(err)
			}
			mustAttach(t, engine, ctx, "target-1", "identity-1")
			return engine
		}
		engine := newEngine()
		if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
			Condition: Condition{Type: ConditionRegex, Pattern: `x(a?)*b`}}); err != nil {
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
		return events
	}

	stream := "xaaab"
	contiguous := run(t, []string{stream}, false, DefaultMaxCarryBytes)
	assertEvents(t, contiguous, 0, 5)
	for split := 1; split < len(stream); split++ {
		chunked := run(t, []string{stream[:split], stream[split:]}, false, DefaultMaxCarryBytes)
		assertEvents(t, chunked, 0, 5)
		restarted := run(t, []string{stream[:split], stream[split:]}, true, DefaultMaxCarryBytes)
		assertEvents(t, restarted, 0, 5)
	}
	// A live partial that outgrows the configured capacity settles as
	// unverifiable instead of silently dropping the possible match.
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	engine, err := New(backend, Options{Now: newTestClock().Now, MaxCarryBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ownerCtx("owner-a")
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	overflow, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `x(a?)*b`}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("xaaaa")); err != nil {
		t.Fatal(err)
	}
	after, err := engine.Get(ctx, overflow.ID)
	if err != nil || after.Status != StatusUnverifiable || !strings.Contains(after.LastError, "carry capacity") {
		t.Fatalf("over-capacity watch = %+v, %v", after, err)
	}
}

func TestErrorDigitsSplitFiresOnce(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `error \d+`}}); err != nil {
		t.Fatal(err)
	}
	// The pattern fires as soon as the first digit completes the match; the
	// start position is chunk-invariant even though greedy extent may differ.
	events, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("error 4"))
	if err != nil || len(events) != 1 || events[0].MatchedFrom != 0 {
		t.Fatalf("first chunk = %+v, %v", events, err)
	}
	events, err = engine.Observe(ctx, "target-1", "identity-1", 7, []byte("2 more"))
	if err != nil || len(events) != 0 {
		t.Fatalf("second chunk = %+v, %v", events, err)
	}
}

func TestContainsOverCapacitySettlesUnverifiable(t *testing.T) {
	clock := newTestClock()
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	engine, err := New(backend, Options{Now: clock.Now, MaxCarryBytes: 2})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ownerCtx("owner-a")
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	watch, err := engine.Create(ctx, CreateParams{TargetID: "target-1", Condition: containsCondition("ready")})
	if err != nil {
		t.Fatal(err)
	}
	// "rea" is a live partial of "ready" but exceeds the 2-byte capacity.
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("rea")); err != nil {
		t.Fatal(err)
	}
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusUnverifiable || !strings.Contains(after.LastError, "carry capacity") {
		t.Fatalf("over-capacity contains watch = %+v, %v", after, err)
	}
}
