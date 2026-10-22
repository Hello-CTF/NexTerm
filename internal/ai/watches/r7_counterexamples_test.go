package watches

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestDynamicPrefixUTF8TailCompletion(t *testing.T) {
	condition := Condition{Type: ConditionRegex, Pattern: `x[界]*z`}
	chunks := [][]byte{{'x', 0xE7}, {0x95, 0x8C}, {'z'}}
	assertChunkObservations(t, []chunkObservation{
		{condition: condition, chunks: [][]byte{[]byte("x界z")}, from: 0, to: 5},
		{condition: condition, chunks: chunks, from: 0, to: 5},
		{condition: condition, chunks: chunks, restart: true, from: 0, to: 5},
	})
}

func TestTrailingEmptyGroupDefersRightEdge(t *testing.T) {
	patterns := []string{`ready\b()`, `ready$()`}
	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			condition := Condition{Type: ConditionRegex, Pattern: pattern}
			continuationFrom, continuationTo := int64(-1), int64(-1)
			if strings.Contains(pattern, `\b`) {
				continuationFrom, continuationTo = 0, 5
			}
			assertChunkObservations(t, []chunkObservation{
				// A word continuation refutes the match, including across a
				// split and a restart.
				{condition: condition, chunks: stringChunks("readyx"), from: -1, to: -1},
				{condition: condition, chunks: stringChunks("ready", "x"), from: -1, to: -1},
				{condition: condition, chunks: stringChunks("ready", "x"), restart: true, from: -1, to: -1},
				// A non-word continuation confirms a word boundary; for
				// end-text only EOF confirms the match.
				{condition: condition, chunks: stringChunks("ready", " "), from: continuationFrom, to: continuationTo},
				{condition: condition, chunks: stringChunks("ready"), eof: true, from: 0, to: 5},
			})
		})
	}
}

func TestMatchedConsumePreservesTailAcrossAck(t *testing.T) {
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
	// Two matched chunks carry an still-incomplete rune across the ack.
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 3, []byte{0xE7}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 4, []byte{0x95}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AgentWatchEventAck(ctx, first[0].DeliveryID, 1, 5000); err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 5, []byte{0x8C})
	if err != nil || len(events) != 1 {
		t.Fatalf("completed rune across ack = %+v, %v; want one fire", events, err)
	}
	if events[0].MatchedFrom != 3 || events[0].MatchedTo != 6 {
		t.Fatalf("match = %d..%d; want 3..6", events[0].MatchedFrom, events[0].MatchedTo)
	}
}

func TestMatchedConsumePreservesTailAcrossAckRestart(t *testing.T) {
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
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 4, []byte{0x95}); err != nil {
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
	events, err := engine.Observe(ctx, "target-1", "identity-1", 5, []byte{0x8C})
	if err != nil || len(events) != 1 || events[0].MatchedFrom != 3 || events[0].MatchedTo != 6 {
		t.Fatalf("completed rune across restarted ack = %+v, %v", events, err)
	}
}

func TestContextBeforeAnchorDoesNotForgeLivePrefix(t *testing.T) {
	clock := newTestClock()
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	engine, err := New(backend, Options{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ownerCtx("owner-a")
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	// The context ends with the condition's first character, but no eligible
	// live prefix exists after the anchor.
	watch, err := engine.Create(ctx, CreateParams{TargetID: "target-1", AnchorSeq: 1, LeftContext: []byte("x"),
		Condition: Condition{Type: ConditionRegex, Pattern: `x.*z`}})
	if err != nil {
		t.Fatal(err)
	}
	long := []byte(strings.Repeat("a", DefaultMaxCarryBytes+1))
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 1, long); err != nil {
		t.Fatal(err)
	}
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusArmed {
		t.Fatalf("watch with ineligible live prefix = %+v, %v", after, err)
	}
	// The same watch keeps evaluating: no eligible prefix ever exists after
	// the anchor, so it stays armed without firing.
	events, err := engine.Observe(ctx, "target-1", "identity-1", int64(1+len(long)), []byte("z"))
	if err != nil || len(events) != 0 {
		t.Fatalf("observe after long output = %+v, %v", events, err)
	}
}

func TestContextBeforeAnchorDoesNotForgeSmallCapacity(t *testing.T) {
	clock := newTestClock()
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	engine, err := New(backend, Options{Now: clock.Now, MaxCarryBytes: 3})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ownerCtx("owner-a")
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	watch, err := engine.Create(ctx, CreateParams{TargetID: "target-1", AnchorSeq: 1, LeftContext: []byte("x"),
		Condition: Condition{Type: ConditionRegex, Pattern: `x.*z`}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 1, []byte("aaaa")); err != nil {
		t.Fatal(err)
	}
	after, err := engine.Get(ctx, watch.ID)
	if err != nil || after.Status != StatusArmed {
		t.Fatalf("watch with small capacity and ineligible prefix = %+v, %v", after, err)
	}
}
