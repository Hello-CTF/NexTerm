package watches

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestZeroMinRepeatFiresWhenConfirmed(t *testing.T) {
	cases := []struct {
		pattern   string
		stream    string
		noEOFFrom int64
		noEOFTo   int64
		eofFrom   int64
		eofTo     int64
	}{
		{`ready\bx{0,2}`, "ready ", 0, 5, 0, 5},      // x{0,2} empty; the space confirms mid-buffer
		{`ready$z{0,2}`, "ready ", -1, -1, -1, -1},   // $ refuted by the space
		{`ready\b(?:x{0,2})`, "ready ", 0, 5, 0, 5},  // capture of a nullable repeat stays nullable
		{`ready\bx{0,2}`, "ready", -1, -1, 0, 5},     // deferred, fires at EOF
		{`ready$z{0,2}`, "ready", -1, -1, 0, 5},      // deferred, fires at EOF
		{`ready\b(?:x{0,2})`, "ready", -1, -1, 0, 5}, // deferred, fires at EOF
	}
	for _, testCase := range cases {
		t.Run(testCase.pattern+"/"+testCase.stream, func(t *testing.T) {
			run := func(t *testing.T, chunks []string, reopen bool, eof bool) []Event {
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
				if eof {
					fired, err := engine.ObserveEOF(ctx, "target-1", "identity-1", position)
					if err != nil {
						t.Fatal(err)
					}
					events = append(events, fired...)
				}
				return events
			}
			assertEvents(t, run(t, []string{testCase.stream}, false, false), testCase.noEOFFrom, testCase.noEOFTo)
			for split := 1; split < len(testCase.stream); split++ {
				assertEvents(t, run(t, []string{testCase.stream[:split], testCase.stream[split:]}, false, false), testCase.noEOFFrom, testCase.noEOFTo)
				assertEvents(t, run(t, []string{testCase.stream[:split], testCase.stream[split:]}, true, false), testCase.noEOFFrom, testCase.noEOFTo)
			}
			assertEvents(t, run(t, []string{testCase.stream}, false, true), testCase.eofFrom, testCase.eofTo)
		})
	}
}
