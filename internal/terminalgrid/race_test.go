package terminalgrid_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	grid "github.com/Hello-CTF/NexTerm/internal/terminalgrid"
)

func TestConcurrentModelOperations(t *testing.T) {
	var active atomic.Int32
	var maxActive atomic.Int32
	transport := func(ctx context.Context, desired grid.Grid) error {
		if !desired.Valid() {
			t.Errorf("invalid transport grid: %+v", desired)
		}
		current := active.Add(1)
		defer active.Add(-1)
		for {
			maximum := maxActive.Load()
			if current <= maximum || maxActive.CompareAndSwap(maximum, current) {
				break
			}
		}
		timer := time.NewTimer(50 * time.Microsecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	model := newModel(t, grid.Config{Mode: grid.ModeController, Resize: transport})

	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_, _ = model.SetViewport(
					grid.Viewport{WidthPx: 500 + worker*100 + i, HeightPx: 300 + i},
					grid.CellMetrics{WidthPx: 8.5, HeightPx: 17},
				)
				_ = model.Snapshot()
			}
		}(worker)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 60; i++ {
			_ = model.SetMode(grid.ModeHidden)
			_ = model.SetMode(grid.ModeController)
			_ = model.SetMode(grid.ModeObserver)
			_, _ = model.Observe(uint64(i+1), grid.Grid{Cols: 80 + i%20, Rows: 30})
			_ = model.Claim()
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			_ = model.Reattach(transport)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Microsecond)
			err := model.Flush(ctx)
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) &&
				!errors.Is(err, grid.ErrHidden) && !errors.Is(err, grid.ErrNotController) && !errors.Is(err, grid.ErrNoDesiredGrid) {
				t.Errorf("unexpected Flush error: %v", err)
			}
			cancel()
		}
	}()
	wg.Wait()

	final := grid.Grid{Cols: 123, Rows: 41}
	if err := model.SetMode(grid.ModeController); err != nil && !errors.Is(err, grid.ErrClosed) {
		t.Fatal(err)
	}
	mustSetDesired(t, model, final)
	if err := model.Reattach(transport); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := model.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := model.Snapshot().CommittedGrid; got != final {
		t.Fatalf("final committed grid = %+v, want %+v", got, final)
	}
	if got := maxActive.Load(); got > 1 {
		t.Fatalf("maximum concurrent transport calls = %d", got)
	}
}
