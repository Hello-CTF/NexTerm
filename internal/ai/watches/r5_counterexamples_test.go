package watches

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestConcatZeroRepeatSplitMatch(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
	}{
		{"zero repeat", `xy*z`},
		{"star any", `x.*z`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			condition := Condition{Type: ConditionRegex, Pattern: testCase.pattern}
			assertChunkObservations(t, splitChunkObservations(condition, "xz", 0, 2))
		})
	}
}

func TestMultiRuneStarPlusSplitMatch(t *testing.T) {
	patterns := []string{`x(ab)*c`, `x(?:ab)+c`}
	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			condition := Condition{Type: ConditionRegex, Pattern: pattern}
			assertChunkObservations(t, splitChunkObservations(condition, "xababc", 0, 6))
		})
	}
	// An over-capacity live partial settles as unverifiable instead of
	// staying armed and silently losing the match.
	backend, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	engine, err := New(backend, Options{Now: newTestClock().Now, MaxCarryBytes: 3})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ownerCtx("owner-a")
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	overflow, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `x(ab)*c`}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("xaba")); err != nil {
		t.Fatal(err)
	}
	after, err := engine.Get(ctx, overflow.ID)
	if err != nil || after.Status != StatusUnverifiable || !strings.Contains(after.LastError, "carry capacity") {
		t.Fatalf("over-capacity watch = %+v, %v", after, err)
	}
}

func TestAssertionPrefixScanUsesRealContext(t *testing.T) {
	condition := Condition{Type: ConditionRegex, Pattern: `\Bfoo.*`}
	assertChunkObservations(t, splitChunkObservations(condition, "afoo", 1, 4))
}

func TestOverlapMatchAfterAnchor(t *testing.T) {
	engine, _ := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1", AnchorSeq: 1, LeftContext: []byte("a"),
		Condition: Condition{Type: ConditionRegex, Pattern: `a.`}}); err != nil {
		t.Fatal(err)
	}
	// The true stream is "aaa": the leftmost eligible match starts at 1, and
	// a context match at 0 must not consume it.
	events, err := engine.Observe(ctx, "target-1", "identity-1", 1, []byte("aa"))
	if err != nil || len(events) != 1 {
		t.Fatalf("overlap observe = %+v, %v", events, err)
	}
	if events[0].MatchedFrom != 1 || events[0].MatchedTo != 3 {
		t.Fatalf("match = %d..%d; want 1..3", events[0].MatchedFrom, events[0].MatchedTo)
	}
}

func TestOverlapMatchAfterAnchorRepeat(t *testing.T) {
	engine, backend := newTestEngine(t, newTestClock())
	ctx := ownerCtx("owner-a")
	mustAttach(t, engine, ctx, "target-1", "identity-1")
	if _, err := engine.Create(ctx, CreateParams{TargetID: "target-1",
		Condition: Condition{Type: ConditionRegex, Pattern: `a.`}, Repeat: true}); err != nil {
		t.Fatal(err)
	}
	first, err := engine.Observe(ctx, "target-1", "identity-1", 0, []byte("a.a"))
	if err != nil || len(first) != 1 || first[0].MatchedTo != 2 {
		t.Fatalf("first fire = %+v, %v", first, err)
	}
	if _, err := backend.AgentWatchEventAck(ctx, first[0].DeliveryID, 1, 5000); err != nil {
		t.Fatal(err)
	}
	events, err := engine.Observe(ctx, "target-1", "identity-1", 3, []byte("aa"))
	if err != nil || len(events) != 1 {
		t.Fatalf("overlap repeat observe = %+v, %v", events, err)
	}
	if events[0].MatchedFrom != 3 || events[0].MatchedTo != 5 {
		t.Fatalf("match = %d..%d; want 3..5", events[0].MatchedFrom, events[0].MatchedTo)
	}
}

func TestRightEdgeAssertionsDefer(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		stream  string
		from    int64
		to      int64
	}{
		{"trailing boundary refuted by word", `ready\b`, "readyx", -1, -1},
		{"trailing end refuted by more output", `ready$`, "readyx", -1, -1},
		{"trailing boundary confirmed by space", `ready\b`, "ready ", 0, 5},
		{"trailing boundary confirmed by punct", `ready\b`, "ready.", 0, 5},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			condition := Condition{Type: ConditionRegex, Pattern: testCase.pattern}
			observations := []chunkObservation{
				{condition: condition, chunks: stringChunks(testCase.stream), from: testCase.from, to: testCase.to},
			}
			for split := 1; split < len(testCase.stream); split++ {
				chunks := stringChunks(testCase.stream[:split], testCase.stream[split:])
				observations = append(observations,
					chunkObservation{condition: condition, chunks: chunks, from: testCase.from, to: testCase.to},
					chunkObservation{condition: condition, chunks: chunks, restart: true, from: testCase.from, to: testCase.to},
				)
			}
			assertChunkObservations(t, observations)
		})
	}
}

func TestRightEdgeAssertionsFireAtEOF(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		stream  string
		from    int64
		to      int64
	}{
		{"trailing boundary at EOF", `ready\b`, "ready", 0, 5},
		{"end text at EOF", `ready$`, "ab ready", 3, 8},
		{"boundary digit at EOF", `\b[0-9]`, " 1", 1, 2},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			condition := Condition{Type: ConditionRegex, Pattern: testCase.pattern}
			observations := []chunkObservation{
				{condition: condition, chunks: stringChunks(testCase.stream), eof: true, from: testCase.from, to: testCase.to},
			}
			for split := 1; split < len(testCase.stream); split++ {
				chunks := stringChunks(testCase.stream[:split], testCase.stream[split:])
				observations = append(observations,
					chunkObservation{condition: condition, chunks: chunks, eof: true, from: testCase.from, to: testCase.to},
					chunkObservation{condition: condition, chunks: chunks, restart: true, eof: true, from: testCase.from, to: testCase.to},
				)
			}
			assertChunkObservations(t, observations)
		})
	}
}
