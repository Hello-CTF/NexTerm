package terminalgrid

import (
	"context"
	"fmt"
	"sync"
)

// Model coordinates one terminal tab's grid intent and transport state.
type Model struct {
	mu sync.Mutex

	viewport     Viewport
	desired      Grid
	committed    Grid
	lastGood     Grid
	observedGrid Grid
	mode         Mode
	role         Mode
	revision     uint64
	observedRev  uint64

	resize   ResizeFunc
	onResult func(Result)

	generation uint64
	inFlight   *request
	pending    *request

	settledRev uint64
	lastResult Result

	changed chan struct{}
	closed  bool
}

type request struct {
	grid       Grid
	revision   uint64
	generation uint64
	force      bool
	resize     ResizeFunc
	cancel     context.CancelFunc
}

// New creates an idle model. A hidden initial model has observer role until
// Claim is called; no initial transport size is invented.
func New(config Config) (*Model, error) {
	if !validMode(config.Mode) {
		return nil, fmt.Errorf("terminalgrid: invalid mode %d", config.Mode)
	}
	role := config.Mode
	if role == ModeHidden {
		role = ModeObserver
	}
	return &Model{
		mode:     config.Mode,
		role:     role,
		resize:   config.Resize,
		onResult: config.OnResult,
		changed:  make(chan struct{}),
	}, nil
}

// Snapshot returns a consistent copy of all public state.
func (m *Model) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *Model) snapshotLocked() Snapshot {
	return Snapshot{
		ViewportPx:       m.viewport,
		DesiredGrid:      m.desired,
		CommittedGrid:    m.committed,
		LastGoodGrid:     m.lastGood,
		Mode:             m.mode,
		Revision:         m.revision,
		ObservedRevision: m.observedRev,
		InFlight:         m.inFlight != nil,
		Pending:          m.pending != nil,
		Attached:         m.resize != nil,
		Closed:           m.closed,
	}
}

// SetViewport records the pixel viewport and schedules a newly computed grid.
// Zero-sized measurements are recorded but preserve the last valid desired
// grid. No transport request is derived from such a measurement.
func (m *Model) SetViewport(viewport Viewport, metrics CellMetrics) (uint64, error) {
	grid, ok, err := GridForViewport(viewport, metrics)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, ErrClosed
	}
	m.viewport = viewport
	if ok {
		if err := m.setDesiredLocked(grid); err != nil {
			return 0, err
		}
	}
	m.notifyLocked()
	return m.revision, nil
}

// SetDesired records and, in controller mode, schedules a validated grid.
// Repeating the current grid retains its revision; Flush can force a final
// transport synchronization without changing the desired value.
func (m *Model) SetDesired(grid Grid) (uint64, error) {
	if err := validateGrid(grid); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, ErrClosed
	}
	if err := m.setDesiredLocked(grid); err != nil {
		return 0, err
	}
	m.notifyLocked()
	return m.revision, nil
}

func (m *Model) setDesiredLocked(grid Grid) error {
	if m.desired == grid {
		return nil
	}
	revision, err := m.nextRevisionLocked()
	if err != nil {
		return err
	}
	m.desired = grid
	m.revision = revision
	m.scheduleLocked(false)
	return nil
}

// SetMode changes transport and observer semantics. Entering controller mode
// from hidden or observer mode forces synchronization of the latest valid
// desired grid. Entering hidden or observer mode cancels controller work.
func (m *Model) SetMode(mode Mode) error {
	if !validMode(mode) {
		return fmt.Errorf("terminalgrid: invalid mode %d", mode)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if m.mode == mode {
		return nil
	}
	if mode == ModeController {
		revision, err := m.nextRevisionLocked()
		if err != nil {
			return err
		}
		m.fenceLocked()
		m.mode = ModeController
		m.role = ModeController
		m.revision = revision
		m.scheduleLocked(true)
	} else {
		if m.mode == ModeController {
			m.fenceLocked()
		}
		m.mode = mode
		if mode == ModeObserver {
			m.role = ModeObserver
		}
		m.pending = nil
	}
	m.notifyLocked()
	return nil
}

// Claim acquires controller role and forces the latest desired grid to be
// flushed. Claim while hidden changes role but remains transport-silent until
// controller mode is entered.
func (m *Model) Claim() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	revision, err := m.nextRevisionLocked()
	if err != nil {
		return err
	}
	m.fenceLocked()
	m.role = ModeController
	if m.mode != ModeHidden {
		m.mode = ModeController
	}
	m.revision = revision
	m.scheduleLocked(true)
	m.notifyLocked()
	return nil
}

// Reattach replaces the transport and establishes a new completion generation.
// Committed state becomes unknown, last known-good state is retained, and a
// visible controller forces the latest desired grid onto the new attachment.
// A nil resize function represents a disconnected transport.
func (m *Model) Reattach(resize ResizeFunc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	revision, err := m.nextRevisionLocked()
	if err != nil {
		return err
	}
	m.fenceLocked()
	m.resize = resize
	m.committed = Grid{}
	m.revision = revision
	m.scheduleLocked(true)
	m.notifyLocked()
	return nil
}

// Observe installs an authoritative remote grid for an observer. Revisions
// below the high-water mark are stale. An equal revision is accepted only for
// an identical replay while reattachment leaves the committed grid unknown.
func (m *Model) Observe(revision uint64, grid Grid) (bool, error) {
	if revision == 0 {
		return false, fmt.Errorf("terminalgrid: observer revision must be positive")
	}
	if err := validateGrid(grid); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false, ErrClosed
	}
	if m.role != ModeObserver {
		return false, ErrNotObserver
	}
	if revision < m.observedRev || (revision == m.observedRev && (m.committed.Valid() || grid != m.observedGrid)) {
		return false, nil
	}
	m.observedRev = revision
	m.observedGrid = grid
	m.committed = grid
	m.lastGood = grid
	m.notifyLocked()
	return true, nil
}

// Flush waits for the latest desired intent and returns its transport error.
// When idle, it forces one final request even if the grid equals the committed
// value. Superseded intents are satisfied by the newest completed intent.
func (m *Model) Flush(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	if err := m.flushEligibleLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	if m.resize != nil {
		if m.inFlight == nil && m.pending == nil {
			revision, err := m.nextRevisionLocked()
			if err != nil {
				m.mu.Unlock()
				return err
			}
			m.revision = revision
			m.scheduleLocked(true)
			m.notifyLocked()
		} else if m.pending != nil && !m.pending.force {
			m.pending.force = true
			m.notifyLocked()
		}
	}
	target := m.revision
	for {
		if m.closed {
			m.mu.Unlock()
			return ErrClosed
		}
		if err := m.flushEligibleLocked(); err != nil {
			m.mu.Unlock()
			return err
		}
		if m.settledRev >= target {
			err := m.lastResult.Err
			m.mu.Unlock()
			return err
		}
		if m.resize == nil && m.inFlight == nil {
			m.mu.Unlock()
			return ErrDisconnected
		}
		changed := m.changed
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
		m.mu.Lock()
	}
}

func (m *Model) flushEligibleLocked() error {
	if m.closed {
		return ErrClosed
	}
	if m.mode == ModeHidden {
		return ErrHidden
	}
	if m.mode != ModeController {
		return ErrNotController
	}
	if !m.desired.Valid() {
		return ErrNoDesiredGrid
	}
	return nil
}

// Close cancels transport work and rejects future operations. It is idempotent
// and does not wait for a transport that ignores cancellation.
func (m *Model) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	m.mode = ModeHidden
	m.fenceLocked()
	m.pending = nil
	m.resize = nil
	m.notifyLocked()
}

func (m *Model) nextRevisionLocked() (uint64, error) {
	if m.revision == ^uint64(0) {
		return 0, ErrRevisionExhausted
	}
	return m.revision + 1, nil
}

// fenceLocked invalidates delayed completions and cancels the active request.
// An interrupted request leaves the active grid unknown rather than guessed.
func (m *Model) fenceLocked() {
	m.generation++
	m.pending = nil
	if m.inFlight != nil {
		m.inFlight.cancel()
		m.committed = Grid{}
	}
}

func (m *Model) scheduleLocked(force bool) {
	if m.mode != ModeController || m.closed || !m.desired.Valid() {
		return
	}
	if m.pending != nil {
		m.pending.grid = m.desired
		m.pending.revision = m.revision
		m.pending.generation = m.generation
		m.pending.force = m.pending.force || force
		return
	}
	if m.inFlight == nil && !force && m.desired == m.committed {
		m.settleLocked(Result{Revision: m.revision, Grid: m.desired})
		return
	}
	req := &request{
		grid:       m.desired,
		revision:   m.revision,
		generation: m.generation,
		force:      force,
	}
	if m.inFlight == nil && m.resize != nil {
		m.startLocked(req)
		return
	}
	m.pending = req
}

func (m *Model) startNextLocked() {
	if m.closed || m.mode != ModeController || m.inFlight != nil || m.pending == nil || m.resize == nil {
		return
	}
	req := m.pending
	m.pending = nil
	if req.generation != m.generation {
		m.settleLocked(Result{Revision: req.revision, Grid: req.grid, Err: context.Canceled})
		return
	}
	if !req.force && req.grid == m.committed {
		m.settleLocked(Result{Revision: req.revision, Grid: req.grid})
		return
	}
	m.startLocked(req)
}

func (m *Model) startLocked(req *request) {
	ctx, cancel := context.WithCancel(context.Background())
	req.cancel = cancel
	req.resize = m.resize
	m.inFlight = req
	go m.run(ctx, req)
}

func (m *Model) run(ctx context.Context, req *request) {
	err := req.resize(ctx, req.grid)
	req.cancel()
	result := Result{Revision: req.revision, Grid: req.grid}
	if err != nil {
		result.Err = &ResizeError{Revision: req.revision, Grid: req.grid, Err: err}
	}

	m.mu.Lock()
	if m.inFlight != req {
		m.mu.Unlock()
		return
	}
	m.inFlight = nil
	if err == nil && req.generation == m.generation && m.mode == ModeController && !m.closed {
		m.committed = req.grid
		m.lastGood = req.grid
		result.Committed = true
	}
	m.settleLocked(result)
	m.startNextLocked()
	m.notifyLocked()
	onResult := m.onResult
	m.mu.Unlock()

	if onResult != nil {
		onResult(result)
	}
}

func (m *Model) settleLocked(result Result) {
	if result.Revision <= m.settledRev {
		return
	}
	m.settledRev = result.Revision
	m.lastResult = result
}

func (m *Model) notifyLocked() {
	close(m.changed)
	m.changed = make(chan struct{})
}
