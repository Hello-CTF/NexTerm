package terminalgrid

import (
	"context"
	"errors"
	"fmt"
	"math"
)

// MaxDimension bounds either grid axis, matching the terminal and PTY cores.
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

// Grid is a terminal size in character cells. The zero value is unknown.
type Grid struct {
	Cols int
	Rows int
}

// Valid reports whether both axes are usable by the terminal and PTY cores.
func (g Grid) Valid() bool {
	return g.Cols > 0 && g.Rows > 0 && g.Cols <= MaxDimension && g.Rows <= MaxDimension
}

// Viewport is the available terminal rendering area in CSS pixels.
type Viewport struct {
	WidthPx  int
	HeightPx int
}

// CellMetrics is the measured size of one terminal cell in CSS pixels.
type CellMetrics struct {
	WidthPx  float64
	HeightPx float64
}

// Mode controls whether local intent may reach the transport.
type Mode uint8

const (
	// ModeHidden suppresses transport work. It is the safe zero value.
	ModeHidden Mode = iota
	// ModeObserver accepts authoritative remote grids but sends no local intent.
	ModeObserver
	// ModeController schedules local intent on the transport.
	ModeController
)

// Snapshot is an immutable copy of the model state.
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

// ResizeFunc applies a grid to the real terminal transport.
type ResizeFunc func(ctx context.Context, grid Grid) error

// Result reports a real transport completion. Committed is false for a result
// invalidated by a later mode, claim or attachment generation.
type Result struct {
	Revision  uint64
	Grid      Grid
	Committed bool
	Err       error
}

// Config constructs a Model. InitialGrid is an already active transport size,
// not a default. OnResult is called without model locks after a real transport
// completion; it may call back into the Model.
type Config struct {
	Mode        Mode
	InitialGrid Grid
	Resize      ResizeFunc
	OnResult    func(Result)
}

// ResizeError associates a transport failure with its intent.
type ResizeError struct {
	Revision uint64
	Grid     Grid
	Err      error
}

func (e *ResizeError) Error() string {
	return fmt.Sprintf("terminalgrid: resize revision %d to %dx%d: %v", e.Revision, e.Grid.Cols, e.Grid.Rows, e.Err)
}

// Unwrap exposes the transport error for errors.Is and errors.As.
func (e *ResizeError) Unwrap() error { return e.Err }

// GridForViewport converts pixels to the largest grid that fits. Positive
// viewports smaller than one cell produce one cell on that axis. A viewport
// with either non-positive axis returns ok=false and no grid.
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
