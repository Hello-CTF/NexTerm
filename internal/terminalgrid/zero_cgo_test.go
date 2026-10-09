//go:build !cgo

package terminalgrid_test

import (
	"context"
	"testing"

	grid "github.com/Hello-CTF/NexTerm/internal/terminalgrid"
)

func TestZeroCGOGridRoundTrip(t *testing.T) {
	model := newModel(t, grid.Config{
		Mode: grid.ModeController,
		Resize: func(_ context.Context, desired grid.Grid) error {
			if !desired.Valid() {
				t.Fatalf("invalid zero-cgo transport grid: %+v", desired)
			}
			return nil
		},
	})
	if _, err := model.SetViewport(
		grid.Viewport{WidthPx: 999, HeightPx: 499},
		grid.CellMetrics{WidthPx: 9.5, HeightPx: 19},
	); err != nil {
		t.Fatal(err)
	}
	if err := model.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := grid.Grid{Cols: 105, Rows: 26}
	if got := model.Snapshot().CommittedGrid; got != want {
		t.Fatalf("committed grid = %+v, want %+v", got, want)
	}
}
