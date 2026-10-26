package terminal

// Emulator is a bare screen model: it keeps the visible grid and cursor of
// a UTF-8 output stream and nothing else (no scrollback, output ring,
// recording, fan-out or input modes). It suits background observers that
// only need to look at the current screen, such as the durable helper's
// state detection.
//
// Emulator is not safe for concurrent use.
type Emulator struct {
	scr *screen
}

// NewEmulator returns an emulator of the given size; non-positive
// dimensions select 80x24.
func NewEmulator(cols, rows int) *Emulator {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	scr := newScreen(min(cols, MaxTerminalDimension), min(rows, MaxTerminalDimension), 1)
	scr.history = nil
	return &Emulator{scr: scr}
}

// Write applies output to the screen. Replies the program asked for (device
// status reports) are discarded.
func (e *Emulator) Write(p []byte) {
	e.scr.write(p)
	e.scr.responses = e.scr.responses[:0]
}

// Resize changes the screen size; invalid sizes are ignored.
func (e *Emulator) Resize(cols, rows int) {
	if cols <= 0 || rows <= 0 || cols > MaxTerminalDimension || rows > MaxTerminalDimension {
		return
	}
	e.scr.resize(cols, rows)
}

// Snapshot returns the visible screen. LastOutputMsAgo is left zero.
func (e *Emulator) Snapshot() Snapshot {
	text := e.scr.text()
	row, col := e.scr.cursor()
	cols, rows := e.scr.size()
	return Snapshot{
		Text:      text,
		Lines:     splitScreenLines(text),
		CursorRow: row,
		CursorCol: col,
		Cols:      cols,
		Rows:      rows,
		AltScreen: e.scr.altScreen(),
		Title:     e.scr.title,
	}
}
