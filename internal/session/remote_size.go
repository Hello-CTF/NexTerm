package session

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/terminalgrid"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// remoteSizeOwner is the size owner of a durable tab whose PTY another
// process sized last, such as a second desktop attached to the same helper
// through its own connection. No client of this manager owns the size then,
// so every local view renders the PTY size until one of them types or takes
// the size back.
const remoteSizeOwner = "other-device"

// watchRemoteSize follows the size pushes of the durable attachment behind
// the tab's output pump, which starts with ctx.
func (m *Manager) watchRemoteSize(ctx context.Context, tab *Tab, generation uint64) {
	tab.mu.Lock()
	reporter, _ := tab.durable.(base.DurableSizeReporter)
	current := !tab.closed && tab.generation == generation
	tab.mu.Unlock()
	if !current || reporter == nil {
		return
	}
	updates := reporter.DurableSizeUpdates()
	m.startGoroutine(func() {
		for {
			select {
			case <-updates:
				if cols, rows, ok := reporter.DurablePushedSize(); ok {
					m.applyRemoteSize(ctx, tab, generation, cols, rows)
				}
			case <-ctx.Done():
				return
			}
		}
	})
}

// applyRemoteSize adopts a size another process gave the tab's PTY: the
// tab's screen model takes it and the size owner becomes remoteSizeOwner,
// so local views follow it instead of drawing the output at their own size.
// The push counts as the remote owner's latest input for the handoff hold.
func (m *Manager) applyRemoteSize(ctx context.Context, tab *Tab, generation uint64, cols, rows uint32) {
	if !validSize(cols, rows) {
		return
	}
	if err := tab.lockFeed(ctx); err != nil {
		return
	}
	tab.mu.Lock()
	if tab.closed || tab.destroying || tab.generation != generation {
		tab.mu.Unlock()
		tab.unlockFeed()
		return
	}
	if err := tab.terminal.Resize(int(cols), int(rows)); err != nil {
		tab.mu.Unlock()
		tab.unlockFeed()
		return
	}
	resized := tab.cols != cols || tab.rows != rows
	changed := resized || tab.sizeOwner != remoteSizeOwner
	tab.cols, tab.rows = cols, rows
	if resized {
		tab.gridRevision++
	}
	tab.sizeOwner = remoteSizeOwner
	tab.sizeOwnerInput = m.now()
	tab.sizeOwned = true
	if tab.grid != nil && !tab.hidden {
		// Fence a resize still in flight for the previous local owner; the
		// next local owner claims the grid and sends its own size.
		_ = tab.grid.SetMode(terminalgrid.ModeObserver)
	}
	var event ControlEvent
	if changed {
		event = tab.controlEventLocked()
	}
	tab.mu.Unlock()
	tab.unlockFeed()
	if changed {
		m.emit(context.Background(), TopicTerminalControl, event)
	}
	if resized {
		m.transcriptResize(ctx, tab, cols, rows)
	}
}
