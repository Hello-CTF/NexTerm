package watches

import (
	"context"
	"errors"
	"fmt"
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

type chunkObservation struct {
	condition     Condition
	chunks        [][]byte
	restart       bool
	eof           bool
	maxCarryBytes int
	from          int64
	to            int64
}

func stringChunks(chunks ...string) [][]byte {
	result := make([][]byte, len(chunks))
	for index, chunk := range chunks {
		result[index] = []byte(chunk)
	}
	return result
}

func splitChunkObservations(condition Condition, stream string, from, to int64) []chunkObservation {
	observations := []chunkObservation{
		{condition: condition, chunks: stringChunks(stream), from: from, to: to},
	}
	for split := 1; split < len(stream); split++ {
		chunks := stringChunks(stream[:split], stream[split:])
		observations = append(observations,
			chunkObservation{condition: condition, chunks: chunks, from: from, to: to},
			chunkObservation{condition: condition, chunks: chunks, restart: true, from: from, to: to},
		)
	}
	return observations
}

// assertChunkObservations keeps an independent target, clock, and engine for
// every case. Restart cases are grouped by chunk count so each one crosses the
// same real close/reopen boundaries as an isolated run without migrating an
// identical schema for every split point.
func assertChunkObservations(t *testing.T, cases []chunkObservation) {
	t.Helper()
	type observationGroup struct {
		restart    bool
		chunkCount int
		indices    []int
	}
	var groups []observationGroup
	for index, testCase := range cases {
		if len(testCase.chunks) == 0 {
			t.Fatalf("chunk observation %d has no input", index)
		}
		chunkCount := len(testCase.chunks)
		if !testCase.restart {
			chunkCount = 0
		}
		groupIndex := -1
		for index := range groups {
			if groups[index].restart == testCase.restart && groups[index].chunkCount == chunkCount {
				groupIndex = index
				break
			}
		}
		if groupIndex < 0 {
			groups = append(groups, observationGroup{restart: testCase.restart, chunkCount: chunkCount})
			groupIndex = len(groups) - 1
		}
		groups[groupIndex].indices = append(groups[groupIndex].indices, index)
	}

	ctx := ownerCtx("owner-a")
	clocks := make([]*testClock, len(cases))
	engines := make([]*Engine, len(cases))
	targets := make([]string, len(cases))
	positions := make([]int64, len(cases))
	events := make([][]Event, len(cases))
	for index := range cases {
		clocks[index] = newTestClock()
		targets[index] = fmt.Sprintf("chunk-observation-%d", index)
	}
	for _, group := range groups {
		func() {
			var backend *store.Store
			var restartPath string
			if group.restart {
				restartPath = filepath.Join(t.TempDir(), "watch.db")
				backend = openRestartStore(t, restartPath)
			} else {
				var err error
				backend, err = store.OpenInMemory(context.Background())
				if err != nil {
					t.Fatal(err)
				}
			}
			defer func() { _ = backend.Close() }()
			newEngine := func(index int) {
				engine, err := New(backend, Options{Now: clocks[index].Now, MaxCarryBytes: cases[index].maxCarryBytes})
				if err != nil {
					t.Fatal(err)
				}
				mustAttach(t, engine, ctx, targets[index], "identity-1")
				engines[index] = engine
			}
			for _, index := range group.indices {
				newEngine(index)
				if _, err := engines[index].Create(ctx, CreateParams{TargetID: targets[index], Condition: cases[index].condition}); err != nil {
					t.Fatal(err)
				}
			}
			for chunkIndex := 0; ; chunkIndex++ {
				active := false
				for _, index := range group.indices {
					if chunkIndex < len(cases[index].chunks) {
						active = true
						break
					}
				}
				if !active {
					break
				}
				if group.restart && chunkIndex > 0 {
					if err := backend.Close(); err != nil {
						t.Fatal(err)
					}
					backend = openRestartStore(t, restartPath)
					for _, index := range group.indices {
						newEngine(index)
					}
				}
				for _, index := range group.indices {
					if chunkIndex >= len(cases[index].chunks) {
						continue
					}
					chunk := cases[index].chunks[chunkIndex]
					fired, err := engines[index].Observe(ctx, targets[index], "identity-1", positions[index], chunk)
					if err != nil {
						t.Fatalf("observe case %d chunk %d: %v", index, chunkIndex, err)
					}
					events[index] = append(events[index], fired...)
					positions[index] += int64(len(chunk))
				}
			}
			for _, index := range group.indices {
				if cases[index].eof {
					fired, err := engines[index].ObserveEOF(ctx, targets[index], "identity-1", positions[index])
					if err != nil {
						t.Fatalf("observe case %d EOF: %v", index, err)
					}
					events[index] = append(events[index], fired...)
				}
				assertEvents(t, events[index], cases[index].from, cases[index].to)
			}
		}()
	}
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
	attachEngine(t, engine, ctx, "target-1", "identity-1")
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
	if len(got) != 1 || got[0].Event.DeliveryID != event.DeliveryID || got[0].Event.RunID != event.RunID {
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
	if len(got) != 2 || got[0].Event.DeliveryID != event.DeliveryID || got[1].Event.DeliveryID != event.DeliveryID ||
		got[0].Event.RunID != event.RunID || got[1].Event.RunID != event.RunID {
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
	attachEngine(t, engine, ctx, "target-1", "identity-1")
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
	attachEngine(t, engine, ctx, "target-1", "identity-1")
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
	attachEngine(t, engine, ctx, "target-1", "identity-1")
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
	attachEngine(t, engine, ctx, "target-1", "identity-2")
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
	attachEngine(t, engine, ctx, "target-1", "identity-2")
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
	attachEngine(t, engine, ctx, "target-1", "identity-1")
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
	attachEngine(t, engine, ctx, "target-1", "identity-1")
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
	attachEngine(t, engine, ctx, "target-1", "identity-1")
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusUnverifiable {
		t.Fatalf("after restart = %+v, %v", after, err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 101, []byte("ready"))
	if err != nil || len(events) != 0 {
		t.Fatalf("unverifiable watch fired after restart: %+v, %v", events, err)
	}
}
