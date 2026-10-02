package tasks

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestConcurrentOperations hammers the manager from many goroutines. Run
// with -race; assertions stay deliberately loose where a kill race
// legitimately changes the outcome.
func TestConcurrentOperations(t *testing.T) {
	requireShell(t)
	m := testManager(t, func(o *Options) {
		o.HeadBytes = 64
		o.TailBytes = 128
	})
	ctx := context.Background()
	var wg sync.WaitGroup

	shared, err := m.Run(ctx, ownerA, gatedCommand(filepath.Join(t.TempDir(), "gate"), "x", "y"), 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Kill(ctx, ownerA, shared.Info.ID); err != nil {
				t.Errorf("shared kill: %v", err)
			}
			if _, err := m.Kill(ctx, ownerB, shared.Info.ID); !errors.Is(err, ErrNotFound) {
				t.Errorf("foreign kill: %v", err)
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, err := m.Output(ctx, ownerA, shared.Info.ID); err != nil {
					t.Errorf("shared output: %v", err)
					return
				}
				if _, err := m.ReadOutput(ctx, ownerA, shared.Info.ID, 0, 32); err != nil {
					t.Errorf("shared read: %v", err)
					return
				}
			}
		}()
	}

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 6; j++ {
				script := fmt.Sprintf("awk 'BEGIN{for(i=0;i<400;i++)printf \"w%d\"}'", worker)
				timeout := time.Duration(j%3-1) * time.Millisecond
				res, err := m.Run(ctx, ownerA, shell(script), timeout)
				if err != nil {
					t.Errorf("run: %v", err)
					return
				}
				if !res.Detached {
					continue
				}
				if _, err := m.Output(ctx, ownerA, res.Info.ID); err != nil && !errors.Is(err, ErrNotFound) {
					t.Errorf("output: %v", err)
				}
				if _, err := m.ReadOutput(ctx, ownerA, res.Info.ID, 0, 16); err != nil && !errors.Is(err, ErrNotFound) {
					t.Errorf("read: %v", err)
				}
				if j%2 == 1 {
					if _, err := m.Kill(ctx, ownerA, res.Info.ID); err != nil && !errors.Is(err, ErrNotFound) {
						t.Errorf("kill: %v", err)
					}
				}
			}
		}(i)
	}

	stop := make(chan struct{})
	var listers sync.WaitGroup
	for i := 0; i < 2; i++ {
		listers.Add(1)
		go func() {
			defer listers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := m.List(ctx, ownerA); err != nil {
					t.Errorf("list: %v", err)
					return
				}
				if _, err := m.List(ctx, ownerB); err != nil {
					t.Errorf("foreign list: %v", err)
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent operations deadlocked")
	}
	close(stop)
	listers.Wait()
	info := waitTerminal(t, m, ownerA, shared.Info.ID)
	if info.State != StateKilled {
		t.Fatalf("shared task terminal state: %+v", info)
	}
}
