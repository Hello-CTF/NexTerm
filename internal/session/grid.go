package session

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/terminalgrid"
)

func (m *Manager) initGrid(tab *Tab, channel *channelHandle, generation uint64) error {
	grid, err := terminalgrid.New(terminalgrid.Config{
		Mode:        terminalgrid.ModeController,
		InitialGrid: terminalgrid.Grid{Cols: int(tab.cols), Rows: int(tab.rows)},
		Resize:      m.gridResize(tab, channel, generation),
	})
	if err != nil {
		return err
	}
	tab.grid = grid
	tab.retire = make(chan struct{})
	return nil
}

func (m *Manager) gridResize(tab *Tab, channel *channelHandle, generation uint64) terminalgrid.ResizeFunc {
	return func(ctx context.Context, desired terminalgrid.Grid) error {
		return m.applyGrid(ctx, tab, channel, generation, desired)
	}
}

func (m *Manager) applyGrid(ctx context.Context, tab *Tab, channel *channelHandle, generation uint64, desired terminalgrid.Grid) error {
	var event ControlEvent
	changed := false
	err := func() error {
		if err := tab.lockFeed(ctx); err != nil {
			return err
		}
		defer tab.unlockFeed()

		tab.mu.Lock()
		current := tab.gridCurrentLocked(ctx, channel, generation)
		tab.mu.Unlock()
		if !current {
			return ErrStaleGeneration
		}
		if channel != nil {
			if err := resizeChannel(ctx, tab, channel, desired); err != nil {
				return err
			}
		}

		tab.mu.Lock()
		defer tab.mu.Unlock()
		if !tab.gridCurrentLocked(ctx, channel, generation) {
			return ErrStaleGeneration
		}
		if err := tab.terminal.Resize(desired.Cols, desired.Rows); err != nil {
			return err
		}
		changed = tab.cols != uint32(desired.Cols) || tab.rows != uint32(desired.Rows)
		tab.cols, tab.rows = uint32(desired.Cols), uint32(desired.Rows)
		if changed {
			tab.gridRevision++
			event = tab.controlEventLocked()
		}
		return nil
	}()
	if err != nil {
		return err
	}
	if changed {
		m.emit(context.Background(), TopicTerminalControl, event)
	}
	return nil
}

func resizeChannel(ctx context.Context, tab *Tab, channel *channelHandle, desired terminalgrid.Grid) error {
	result := make(chan error, 1)
	go func() {
		result <- channel.Resize(ctx, uint32(desired.Cols), uint32(desired.Rows))
	}()
	select {
	case err := <-result:
		return err
	case <-tab.retire:
		return ErrTabClosed
	}
}

func (t *Tab) gridCurrentLocked(ctx context.Context, channel *channelHandle, generation uint64) bool {
	return !t.closed && t.generation == generation && t.channel == channel && t.grid != nil && t.grid.Current(ctx)
}

func (m *Manager) GridSnapshot(tabID string) (terminalgrid.Snapshot, error) {
	tab, err := m.Tab(tabID)
	if err != nil {
		return terminalgrid.Snapshot{}, err
	}
	tab.mu.Lock()
	grid := tab.grid
	closed := tab.closed
	tab.mu.Unlock()
	if closed {
		return terminalgrid.Snapshot{}, ErrTabClosed
	}
	if grid == nil {
		return terminalgrid.Snapshot{}, ErrUnsupported
	}
	return grid.Snapshot(), nil
}

func (m *Manager) FlushResize(ctx context.Context, tabID, client string) error {
	tab, err := m.Tab(tabID)
	if err != nil {
		return err
	}
	tab.mu.Lock()
	if tab.closed {
		tab.mu.Unlock()
		return ErrTabClosed
	}
	if tab.controller != clientID(client) {
		tab.mu.Unlock()
		return ErrNotController
	}
	grid := tab.grid
	tab.mu.Unlock()
	if grid == nil {
		return ErrUnsupported
	}
	return mapGridError(grid.Flush(ctx))
}

func mapGridError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, terminalgrid.ErrClosed):
		return ErrTabClosed
	case errors.Is(err, terminalgrid.ErrDisconnected):
		return ErrDisconnected
	case errors.Is(err, terminalgrid.ErrNotController):
		return ErrNotController
	case errors.Is(err, terminalgrid.ErrHidden):
		return ErrHidden
	case errors.Is(err, terminalgrid.ErrNoDesiredGrid):
		return ErrInvalidSize
	default:
		return err
	}
}
