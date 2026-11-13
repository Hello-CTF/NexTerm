package keyframe

import (
	"strconv"
	"strings"
)

// State serializes the screen. Writing it into a fresh xterm.js terminal of
// the screen's size (reset, resize, write) rebuilds both buffers with their
// scrollback, wrapped-line flags, cell renditions, cursors, saved cursors,
// scroll regions, tab stops, modes and character sets.
//
// Null cells that only carry a background are rebuilt with erases so they
// stay contentless, as reflow requires; null cells carrying other renditions
// become spaces, which render the same.
func (s *Screen) State() string {
	w := &stateWriter{s: s}
	w.savedCursor(&s.normal.saved)
	w.lines(s.normal.lines)
	w.bufferState(s.normal)
	if s.active == s.normal && !defaultSavedCursor(s.alt.saved) {
		// Clearing the alternate buffer keeps its saved cursor, which a later
		// restore there reads.
		w.setPen(attr{})
		w.raw("\x1b[?47h")
		w.savedCursor(&s.alt.saved)
		w.raw("\x1b[?47l")
	}
	if s.active == s.alt {
		// The normal buffer keeps its cursor while the alternate one is
		// shown; a resize meanwhile depends on it.
		w.place(s.normal, false)
		w.setPen(attr{})
		w.raw("\x1b[?47h")
		w.savedCursor(&s.alt.saved)
		w.raw("\x1b[H")
		// Only the viewport: xterm keeps an alternate buffer's line limit when
		// it is resized while empty, so it can scroll lines above the viewport
		// that a fresh terminal has no room for. They are never shown.
		w.lines(s.alt.lines[s.alt.ybase:])
		w.bufferState(s.alt)
	}
	if s.origin {
		w.raw("\x1b[?6h")
	}
	w.place(s.active, s.origin)
	if s.insert {
		w.raw("\x1b[4h")
	}
	if !s.wraparound {
		w.raw("\x1b[?7l")
	}
	if s.reverseWrap {
		w.raw("\x1b[?45h")
	}
	if s.cursorHidden {
		w.raw("\x1b[?25l")
	}
	w.charsets(s.charsets, s.glevel)
	w.setPen(s.pen)
	return w.out.String()
}

type stateWriter struct {
	s   *Screen
	out strings.Builder
	pen attr
	// pendingErase is the rendition of a null last cell that was printed as a
	// placeholder to carry a wrap into the next line; it is erased once the
	// next line wrapped.
	pendingErase *attr
	// wrapWithWide reports that the line just written left its last cell for
	// the next line's first, wide character to wrap over.
	wrapWithWide bool
	// wrappedOver reports that a throwaway wide character already wrapped
	// into the next line; onNextLine that the cursor already stands at the
	// start of the next line.
	wrappedOver bool
	onNextLine  bool
	// dirtyStart reports that column 0 of the first line holds a
	// placeholder.
	dirtyStart bool
}

func (w *stateWriter) raw(text string) { w.out.WriteString(text) }

func (w *stateWriter) csi(n int, final byte) {
	w.out.WriteString("\x1b[")
	w.out.WriteString(strconv.Itoa(n))
	w.out.WriteByte(final)
}

func (w *stateWriter) cup(row, col int) {
	w.out.WriteString("\x1b[")
	w.out.WriteString(strconv.Itoa(row + 1))
	w.out.WriteByte(';')
	w.out.WriteString(strconv.Itoa(col + 1))
	w.out.WriteByte('H')
}

func (w *stateWriter) setPen(a attr) {
	if a == w.pen {
		return
	}
	w.pen = a
	w.out.WriteString(sgr(a))
}

func colorParams(c color, base int) []string {
	value := int(c &^ colorMode)
	switch c & colorMode {
	case colorP16:
		if base == 58 {
			return []string{"58", "5", strconv.Itoa(value)}
		}
		if value < 8 {
			return []string{strconv.Itoa(base - 8 + value)}
		}
		return []string{strconv.Itoa(base + 52 + value - 8)}
	case colorP256:
		return []string{strconv.Itoa(base), "5", strconv.Itoa(value)}
	case colorRGB:
		return []string{strconv.Itoa(base), "2", strconv.Itoa(value >> 16 & 0xff), strconv.Itoa(value >> 8 & 0xff), strconv.Itoa(value & 0xff)}
	}
	return nil
}

// sgr returns the SGR sequence that sets a from scratch.
func sgr(a attr) string {
	parts := []string{"0"}
	for _, f := range []struct {
		flag uint16
		code string
	}{
		{flagBold, "1"}, {flagDim, "2"}, {flagItalic, "3"}, {flagBlink, "5"}, {flagInverse, "7"},
		{flagInvisible, "8"}, {flagStrike, "9"}, {flagOverline, "53"},
	} {
		if a.flags&f.flag != 0 {
			parts = append(parts, f.code)
		}
	}
	if a.flags&flagUnderline != 0 {
		if a.ulStyle > 1 {
			parts = append(parts, "4:"+strconv.Itoa(int(a.ulStyle)))
		} else {
			parts = append(parts, "4")
		}
	}
	parts = append(parts, colorParams(a.fg, 38)...)
	parts = append(parts, colorParams(a.bg, 48)...)
	parts = append(parts, colorParams(a.ul, 58)...)
	return "\x1b[" + strings.Join(parts, ";") + "m"
}

func (w *stateWriter) attrOf(c cell) attr { return w.s.styles[c.attr] }

// printable reports whether the cell at i is rebuilt by printing it: it has
// content, a width the print reproduces and room for that width.
func (w *stateWriter) printable(l *line, i int) bool {
	c := l.cells[i]
	if !l.hasContent(i) || c.width == 0 {
		return false
	}
	if c.width == 2 {
		return i+1 < l.length() && l.cells[i+1].width == 0 || w.strandedWide(l, i)
	}
	return true
}

// strandedWide reports a wide character in the last two columns whose
// second half became a null cell of its rendition: a wide character that did
// not fit with autowrap off was dropped after it, and the end of that print
// cleared the half.
func (w *stateWriter) strandedWide(l *line, i int) bool {
	last := l.length() - 1
	return i == last-1 && l.cells[i].width == 2 && l.cells[last].width == 1 && !l.hasContent(last) &&
		l.cells[last].attr == l.cells[i].attr
}

func (w *stateWriter) printCell(l *line, i int) {
	c, comb := l.load(i)
	w.setPen(w.attrOf(c))
	if c.ch != 0 {
		w.out.WriteRune(c.ch)
	}
	for _, r := range comb {
		w.out.WriteRune(r)
	}
}

// lines writes a buffer's lines from the top of a fresh screen.
func (w *stateWriter) lines(lines []*line) {
	if len(lines) > 0 && lines[0].wrapped && w.s.rows > 1 {
		// The first line continues one that left the buffer. A full row and
		// one more character wrap into the second row, and deleting the
		// first row moves the flagged row to the top.
		w.setPen(attr{})
		w.raw(strings.Repeat("X", w.s.cols+1))
		w.raw("\x1b[H\x1b[M")
		w.dirtyStart = true
	}
	for i, l := range lines {
		continuation := i > 0 && l.wrapped
		if i > 0 && !continuation && !w.onNextLine {
			if w.pen.bg != colorDefault {
				w.setPen(attr{})
			}
			w.raw("\r\n")
		}
		w.onNextLine = false
		var next *line
		if i+1 < len(lines) && lines[i+1].wrapped {
			next = lines[i+1]
		}
		w.line(l, continuation, next, i+1 < len(lines))
	}
}

// wideWrap reports that l ends in a null cell its wrapped continuation
// cleared when its first, wide character did not fit: printing that character
// in the last column rebuilds the null cell with the same rendition.
func (w *stateWriter) wideWrap(l, next *line) bool {
	last := l.length() - 1
	return next != nil && last > 0 && l.cells[last].width == 1 && !l.hasContent(last) &&
		w.printable(next, 0) && next.cells[0].width == 2 && l.cells[last].attr == next.cells[0].attr
}

func (w *stateWriter) line(l *line, continuation bool, next *line, hasNext bool) {
	cols := w.s.cols
	x, c := 0, 0
	dirty, dirtyAll := false, false
	end := cols
	stranded := cols > 1 && w.strandedWide(l, cols-2)
	wideWrap := !stranded && w.wideWrap(l, next)
	// A null last cell with a rendition beyond a background is what a wide
	// character that did not fit leaves; it is rebuilt the same way, with a
	// throwaway wide character, unless no line follows to wrap into.
	last := cols - 1
	clearWrap := !stranded && !wideWrap && hasNext && last > 0 && l.cells[last].width == 1 && !l.hasContent(last) &&
		w.attrOf(l.cells[last]) != w.attrOf(l.cells[last]).eraseAttr()
	if wideWrap || clearWrap {
		end = cols - 1
	}
	if continuation && w.wrappedOver {
		// The previous line's throwaway wide character wrapped into this
		// line; everything is rewritten from column 0.
		w.wrappedOver = false
		w.raw("\r")
		dirtyAll = true
	} else if continuation && w.wrapWithWide {
		// The previous line left its last cell for this wide character.
		w.wrapWithWide = false
		w.printCell(l, 0)
		x, c = 2, 2
		dirtyAll = true
	} else if w.dirtyStart {
		w.dirtyStart = false
		dirty = true
	} else if continuation {
		// The wrap is carried by a placeholder printed with the default pen:
		// a wrap at the bottom scrolls in a line filled with the pen's
		// background. Column 0 is then rewritten below.
		w.setPen(attr{})
		w.raw("X\x1b[D")
		dirty = true
		if w.pendingErase != nil {
			w.raw("\x1b[A")
			w.csi(cols, 'G')
			w.setPen(*w.pendingErase)
			w.raw("\x1b[X\x1b[B")
			w.csi(x+1, 'G')
			w.pendingErase = nil
		}
	}
	// moveTo brings the cursor forward to column c before anything is
	// written there; skipped default cells cost nothing.
	moveTo := func(c int) {
		if c > x {
			w.csi(c-x, 'C')
			x = c
		}
	}
	for c < end && c < l.length() {
		if w.printable(l, c) {
			moveTo(c)
			w.printCell(l, c)
			width := int(l.cells[c].width)
			x += width
			c += width
			if width == 2 && w.strandedWide(l, c-2) {
				// Print it again with autowrap off: it is dropped and the
				// end of the print clears the second half.
				w.raw("\x1b[?7l")
				w.printCell(l, c-2)
				w.raw("\x1b[?7h")
				x = cols - 1
			}
			continue
		}
		if c > 0 && l.cells[c].width == 0 && !l.hasContent(c) && l.cells[c-1].width == 2 && w.printable(l, c-1) {
			c++
			continue
		}
		run := c + 1
		for run < end && !w.printable(l, run) && l.cells[run].attr == l.cells[c].attr &&
			!(l.cells[run].width == 0 && l.cells[run-1].width == 2 && w.printable(l, run-1)) {
			run++
		}
		a := w.attrOf(l.cells[c])
		switch {
		case a != a.eraseAttr():
			moveTo(c)
			w.setPen(a)
			w.raw(strings.Repeat(" ", run-c))
			x = run
		case a != (attr{}) || (dirty && c == 0) || dirtyAll:
			// A line a wrap scrolled in is filled with the pen's
			// background, so its default cells are erased too.
			moveTo(c)
			w.setPen(a)
			w.csi(run-c, 'X')
		}
		c = run
	}
	if wideWrap {
		moveTo(cols - 1)
		w.wrapWithWide = true
		return
	}
	if clearWrap {
		moveTo(cols - 1)
		w.setPen(w.attrOf(l.cells[last]))
		w.raw("\u4e2d") // any wide character
		if next != nil {
			w.wrappedOver = true
			return
		}
		// The next line is not a continuation: erasing it from column 0
		// clears the wrap flag and the throwaway character.
		w.setPen(attr{})
		w.raw("\r\x1b[K")
		w.onNextLine = true
		return
	}
	if next != nil && x < cols {
		lastCell := w.attrOf(l.cells[cols-1])
		w.csi(cols, 'G')
		w.setPen(attr{})
		w.raw("X")
		w.pendingErase = &lastCell
	}
}

// bufferState restores the tab stops, the saved cursor and the scroll
// region of b, which is the buffer the writer is in.
func (w *stateWriter) bufferState(b *buffer) {
	s := w.s
	if !defaultTabs(b.tabs, s.cols) {
		w.raw("\x1b[3g")
		for col := 0; col < s.cols; col++ {
			if b.tabs[col] {
				w.csi(col+1, 'G')
				w.raw("\x1bH")
			}
		}
	}
	if b.scrollTop != 0 || b.scrollBottom != s.rows-1 {
		w.raw("\x1b[" + strconv.Itoa(b.scrollTop+1) + ";" + strconv.Itoa(b.scrollBottom+1) + "r")
	}
}

func defaultSavedCursor(saved savedCursor) bool {
	return saved.charsets == nil && saved.x == 0 && saved.y == 0 && saved.attr == (attr{}) &&
		!saved.origin && saved.wraparound && saved.glevel == 0
}

// savedCursor rebuilds a buffer's DECSC state before anything is written
// to the fresh buffer: line feeds scroll the save's absolute row into the
// viewport, the save is made with the same column, rendition, character sets
// and modes, and the scrollback and screen are then cleared. xterm adjusts
// no saved row on scrolls or ED 3, so even a row past the buffer's end (left
// by a trimmed scrollback) is kept. A column past the last one (saved with a
// wrap pending) is reached by printing a placeholder the clear removes.
func (w *stateWriter) savedCursor(saved *savedCursor) {
	s := w.s
	if defaultSavedCursor(*saved) {
		return
	}
	row := max(saved.y, 0)
	if row > s.rows-1 {
		w.cup(s.rows-1, 0)
		w.raw(strings.Repeat("\n", row-(s.rows-1)))
		row = s.rows - 1
	}
	if saved.origin {
		w.raw("\x1b[?6h")
	}
	if saved.x >= s.cols {
		w.cup(row, s.cols-1)
		w.setPen(attr{})
		w.raw("X")
	} else {
		w.cup(row, saved.x)
	}
	w.setPen(attr{fg: saved.attr.fg, bg: saved.attr.bg, flags: saved.attr.flags})
	if saved.charsets != nil {
		var sets [4]charset
		copy(sets[:], saved.charsets)
		w.charsets(sets, saved.glevel)
	} else {
		w.charsets([4]charset{'B', 'B', 'B', 'B'}, saved.glevel)
	}
	if !saved.wraparound {
		w.raw("\x1b[?7l")
	}
	w.raw("\x1b7")
	if !saved.wraparound {
		w.raw("\x1b[?7h")
	}
	w.charsets([4]charset{'B', 'B', 'B', 'B'}, 0)
	w.setPen(attr{})
	if saved.origin {
		w.raw("\x1b[?6l")
	}
	w.raw("\x1b[3J\x1b[2J\x1b[H")
}

func defaultTabs(tabs map[int]bool, cols int) bool {
	for col := 0; col < cols; col++ {
		if tabs[col] != (col%8 == 0) {
			return false
		}
	}
	return true
}

// charsets designates G0-G3 and selects the GL set; it always writes the
// designations so it can return to the defaults.
func (w *stateWriter) charsets(sets [4]charset, glevel int) {
	for g, inter := range []byte{'(', ')', '*', '+'} {
		final := byte(sets[g])
		if final == 0 {
			final = 'B'
		}
		w.out.WriteByte(0x1b)
		w.out.WriteByte(inter)
		w.out.WriteByte(final)
	}
	switch glevel {
	case 0:
		w.raw("\x0f")
	case 1:
		w.raw("\x0e")
	case 2:
		w.raw("\x1bn")
	case 3:
		w.raw("\x1bo")
	}
}

// place moves the cursor to b's position. A cursor past the last column (a
// pending wrap) is rebuilt by printing the last cell again.
func (w *stateWriter) place(b *buffer, origin bool) {
	s := w.s
	row := b.y
	if origin {
		row = b.y - b.scrollTop
	}
	l := b.line(b.y)
	strandedPending := b.x >= s.cols && (l == nil || !w.printable(l, s.cols-1) &&
		!(s.cols > 1 && l.cells[s.cols-1].width == 0 && w.printable(l, s.cols-2)))
	if b == s.normal && (origin && (b.y < b.scrollTop || b.y > b.scrollBottom) || strandedPending) {
		// A cursor outside the scroll region in origin mode cannot be
		// addressed, and a wrap pending over a null last cell cannot be
		// printed; the cursor is placed in the alternate buffer, whose region
		// is the whole screen, and carried back by the buffer switch.
		w.setPen(attr{})
		w.raw("\x1b[?47h")
		w.cup(b.y, min(b.x, s.cols-1))
		if b.x >= s.cols {
			// The placeholder is printed into the alternate buffer, which
			// the switch back clears.
			w.raw("X")
		}
		w.raw("\x1b[?47l")
		return
	}
	if b.x < s.cols {
		w.cup(row, b.x)
		return
	}
	col := s.cols - 1
	if l != nil && col > 0 && l.cells[col].width == 0 && l.cells[col-1].width == 2 {
		col--
	}
	if l == nil {
		w.cup(row, s.cols-1)
		return
	}
	w.cup(row, col)
	if w.printable(l, col) {
		w.printCell(l, col)
		return
	}
	// No print rebuilds a null last cell with the wrap pending; a space of
	// its rendition renders the same.
	w.setPen(w.attrOf(l.cells[s.cols-1]))
	w.raw(" ")
}
