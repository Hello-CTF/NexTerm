package terminalgrid

import (
	"context"
	"errors"
	"fmt"
	"math"
)

const MaxDimension = 1024

var (
	ErrClosed            = errors.New("terminalgrid: model closed")
	ErrDisconnected      = errors.New("terminalgrid: resize transport disconnected")
	ErrNoDesiredGrid     = errors.New("terminalgrid: no valid desired grid")
	ErrNotController     = errors.New("terminalgrid: resize requires controller mode")
	ErrHidden            = errors.New("terminalgrid: resize is suppressed while hidden")
	ErrNotObserver       = errors.New("terminalgrid: authoritative grid requires observer role")
	ErrRevisionExhausted = errors.New("terminalgrid: revision exhausted")
)

type Grid struct {
	Cols int
	Rows int
}

func (g Grid) Valid() bool {
	return g.Cols > 0 && g.Rows > 0 && g.Cols <= MaxDimension && g.Rows <= MaxDimension
}

type Viewport struct {
	WidthPx  int
	HeightPx int
}

type CellMetrics struct {
	WidthPx  float64
	HeightPx float64
}

type Mode uint8

const (
	ModeHidden Mode = iota
	ModeObserver
	ModeController
)

type Snapshot struct {
	ViewportPx       Viewport
	DesiredGrid      Grid
	CommittedGrid    Grid
	LastGoodGrid     Grid
	Mode             Mode
	Revision         uint64
	ObservedRevision uint64
	InFlight         bool
	Pending          bool
	Attached         bool
	Closed           bool
}

type ResizeFunc func(ctx context.Context, grid Grid) error

type Result struct {
	Revision  uint64
	Grid      Grid
	Committed bool
	Err       error
}

type Config struct {
	Mode        Mode
	InitialGrid Grid
	Resize      ResizeFunc
	OnResult    func(Result)
}

type ResizeError struct {
	Revision uint64
	Grid     Grid
	Err      error
}

func (e *ResizeError) Error() string {
	return fmt.Sprintf("terminalgrid: resize revision %d to %dx%d: %v", e.Revision, e.Grid.Cols, e.Grid.Rows, e.Err)
}

func (e *ResizeError) Unwrap() error { return e.Err }

func GridForViewport(viewport Viewport, metrics CellMetrics) (grid Grid, ok bool, err error) {
	if viewport.WidthPx <= 0 || viewport.HeightPx <= 0 {
		return Grid{}, false, nil
	}
	if !positiveFinite(metrics.WidthPx) || !positiveFinite(metrics.HeightPx) {
		return Grid{}, false, fmt.Errorf("terminalgrid: invalid cell metrics %vx%v", metrics.WidthPx, metrics.HeightPx)
	}
	return Grid{
		Cols: fitAxis(viewport.WidthPx, metrics.WidthPx),
		Rows: fitAxis(viewport.HeightPx, metrics.HeightPx),
	}, true, nil
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func fitAxis(pixels int, cell float64) int {
	ratio := float64(pixels) / cell
	if ratio < 1 {
		return 1
	}
	if math.IsInf(ratio, 1) || ratio >= MaxDimension {
		return MaxDimension
	}
	return int(math.Floor(ratio))
}

func validateGrid(grid Grid) error {
	if !grid.Valid() {
		return fmt.Errorf("terminalgrid: invalid grid %dx%d", grid.Cols, grid.Rows)
	}
	return nil
}

func validMode(mode Mode) bool {
	return mode == ModeHidden || mode == ModeObserver || mode == ModeController
}
