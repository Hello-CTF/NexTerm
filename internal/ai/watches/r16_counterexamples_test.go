package watches

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func TestZeroWidthBeginAssertionDefersRightEdge(t *testing.T) {
	cases := []struct {
		pattern string
		stream  string
	}{
		{`(?m)ready\n$^`, "ready\ny"},
		{`(?m)ready\n$^x?`, "ready\ny"},
		{`(?m)ready\n$^x{0,2}`, "ready\ny"},
		{`ready\b(\b){1,2}`, "readyx"},
		{`ready\b(x{0,0})`, "readyx"},
	}
	for _, testCase := range cases {
		t.Run(testCase.pattern, func(t *testing.T) {
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
			// Contiguous input refutes the assertion: no fire, even at EOF.
			assertEvents(t, run(t, []string{testCase.stream}, false, false), -1, -1)
			assertEvents(t, run(t, []string{testCase.stream}, false, true), -1, -1)
			// Every split point defers and is then refuted: no fire, even
			// across a restart and with EOF.
			for split := 1; split < len(testCase.stream); split++ {
				assertEvents(t, run(t, []string{testCase.stream[:split], testCase.stream[split:]}, false, false), -1, -1)
				assertEvents(t, run(t, []string{testCase.stream[:split], testCase.stream[split:]}, true, false), -1, -1)
				assertEvents(t, run(t, []string{testCase.stream[:split], testCase.stream[split:]}, true, true), -1, -1)
			}
		})
	}
}
