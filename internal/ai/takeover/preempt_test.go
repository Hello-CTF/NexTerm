package takeover

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
)

// Preempt and Enter serialize on the tab's operation lock: however they
// interleave, at most one entered token still owns the tab afterwards, and
// neither writes to the terminal.
func TestR4PreemptAndEnterAreAtomic(t *testing.T) {
	var aiWrites atomic.Int32
	manager := NewManager(Dependencies{
		Shared: true,
		Snapshot: func(context.Context, string) (tools.Screen, error) {
			return tools.Screen{Text: "$ ", IdleMS: 301}, nil
		},
		WriteAI: func(context.Context, string, []byte) error {
			aiWrites.Add(1)
			return nil
		},
	})
	defer manager.Close()
	for round := 0; round < 50; round++ {
		first, err := manager.Enter(context.Background(), "tab")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		tokens := make(chan string, 1)
		wg.Add(2)
		go func() {
			defer wg.Done()
			manager.Preempt("tab")
		}()
		go func() {
			defer wg.Done()
			token, err := manager.Enter(context.Background(), "tab")
			if err != nil {
				t.Error(err)
				return
			}
			tokens <- token
		}()
		wg.Wait()
		close(tokens)
		active := 0
		for _, token := range []string{first, <-tokens} {
			status, err := manager.Status(context.Background(), "tab", token)
			if err != nil {
				t.Fatal(err)
			}
			if status.Active {
				active++
			}
		}
		if active > 1 {
			t.Fatalf("round %d: %d tokens own the tab", round, active)
		}
		if active == 1 && !manager.Active("tab") {
			t.Fatalf("round %d: a token is active but the tab is not", round)
		}
		manager.Preempt("tab")
	}
	if aiWrites.Load() != 0 {
		t.Fatalf("ownership operations wrote %d times to the terminal", aiWrites.Load())
	}
}
