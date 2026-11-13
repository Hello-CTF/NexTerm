package keyframe

import (
	"github.com/charmbracelet/x/ansi"

	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

// Scrollback is the replay view's xterm scrollback (TranscriptReplayView).
const Scrollback = 5000

// maxStyles bounds the rendition table; renditions past it fall back to the
// default one.
const maxStyles = 1 << 16

// Screen is the modelled xterm terminal. It is not safe for concurrent use.
type Screen struct {
	parser *ansi.Parser
	cols   int
	rows   int

	normal *buffer
	alt    *buffer
	active *buffer

	pen        attr
	styles     []attr
	styleIndex map[attr]uint16

	charsets [4]charset
	glevel   int

	wraparound   bool
	origin       bool
	insert       bool
	reverseWrap  bool
	cursorHidden bool

	// joinWidth is the width of the preceding printed character for the
	// Unicode 11 join rule; 0 after any other action.
	joinWidth int
	// printing reports that the last parser action was a print: xterm
	// handles a run of printable characters as one batch.
	printing bool
}

// New returns a screen of the given size; non-positive dimensions select
// 80x24.
func New(cols, rows int) *Screen {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	s := &Screen{cols: cols, rows: rows, styleIndex: map[attr]uint16{}}
	s.styles = []attr{{}}
	s.styleIndex[attr{}] = 0
	s.reset()
	s.parser = ansi.NewParser()
	s.parser.SetDataSize(1024)
	s.parser.SetHandler(ansi.Handler{
		Print:     s.print,
		Execute:   s.execute,
		HandleCsi: s.csi,
		HandleEsc: s.esc,
		HandleOsc: func(int, []byte) { s.endJoin() },
		HandleDcs: func(ansi.Cmd, ansi.Params, []byte) { s.endJoin() },
	})
	return s
}

// reset is xterm's full reset: fresh buffers and default modes, the size is
// kept.
func (s *Screen) reset() {
	s.normal = newBuffer(s.cols, s.rows, Scrollback, true)
	s.normal.fillViewport(s.cols, s.rows, 0)
	s.alt = newBuffer(s.cols, s.rows, Scrollback, false)
	s.active = s.normal
	s.pen = attr{}
	s.charsets = [4]charset{'B', 'B', 'B', 'B'}
	s.glevel = 0
	s.wraparound = true
	s.origin = false
	s.insert = false
	s.reverseWrap = false
	s.cursorHidden = false
	s.endJoin()
}

// Write applies terminal output.
func (s *Screen) Write(p []byte) {
	for _, b := range p {
		s.parser.Advance(b)
	}
}

// Size returns the screen size.
func (s *Screen) Size() (cols, rows int) { return s.cols, s.rows }

// Resize changes the size as xterm's BufferSet.resize does.
func (s *Screen) Resize(cols, rows int) {
	if cols <= 1 || rows <= 0 || cols > terminal.MaxTerminalDimension || rows > terminal.MaxTerminalDimension {
		return
	}
	if cols == s.cols && rows == s.rows {
		return
	}
	s.normal.resize(s.cols, s.rows, cols, rows, Scrollback)
	s.alt.resize(s.cols, s.rows, cols, rows, Scrollback)
	s.cols, s.rows = cols, rows
	s.normal.setupTabStops(cols, cols)
	s.alt.setupTabStops(cols, cols)
}

func (s *Screen) style(a attr) uint16 {
	if i, ok := s.styleIndex[a]; ok {
		return i
	}
	if len(s.styles) >= maxStyles {
		return 0
	}
	i := uint16(len(s.styles))
	s.styles = append(s.styles, a)
	s.styleIndex[a] = i
	return i
}

func (s *Screen) penStyle() uint16   { return s.style(s.pen) }
func (s *Screen) eraseStyle() uint16 { return s.style(s.pen.eraseAttr()) }

func (s *Screen) endJoin() {
	s.joinWidth = 0
	s.printing = false
}

func (s *Screen) currentLine() *line {
	return s.active.line(s.active.y)
}

// mapCharset applies the active G set to an ASCII code point.
func (s *Screen) mapCharset(r rune) rune {
	if r >= 127 {
		return r
	}
	switch s.charsets[s.glevel] {
	case '0':
		if mapped, ok := decSpecialGraphics[r]; ok {
			return mapped
		}
	case 'A':
		if r == '#' {
			return '£'
		}
	}
	return r
}

// print is xterm's InputHandler.print for one code point with the Unicode 11
// width provider.
func (s *Screen) print(r rune) {
	if r == 0xAD {
		return
	}
	b := s.active
	row := s.currentLine()
	if row == nil {
		return
	}
	// At the start of a batch, printing over the second half of a wide
	// character clears its first half.
	if !s.printing && b.x > 0 && row.width(b.x-1) == 2 {
		row.setNull(b.x-1, s.penStyle())
	}
	s.printing = true
	r = s.mapCharset(r)
	// A zero-width code point after a printed character joins its cell and
	// takes its width (the Unicode 11 provider's charProperties).
	width := terminal.RuneWidth(r)
	oldWidth := 0
	join := width == 0 && s.joinWidth > 0
	if join {
		oldWidth = s.joinWidth
		width = oldWidth
	}
	s.joinWidth = width
	pen := s.penStyle()
	if b.x+width-oldWidth > s.cols {
		if s.wraparound {
			oldRow := row
			oldCol := b.x - oldWidth
			b.x = oldWidth
			b.y++
			if b.y == b.scrollBottom+1 {
				b.y--
				b.scroll(s.cols, s.eraseStyle(), true)
			} else {
				if b.y >= s.rows {
					b.y = s.rows - 1
				}
				b.setWrapped(b.ybase+b.y, true)
			}
			row = s.currentLine()
			if row == nil {
				return
			}
			if oldWidth > 0 {
				row.copyCellsFrom(oldRow, oldCol, 0, oldWidth, false)
			}
			for ; oldCol < s.cols; oldCol++ {
				oldRow.setNull(oldCol, pen)
			}
		} else {
			b.x = s.cols - 1
			if width == 2 {
				// Dropped; the batch still ends with the stub check.
				s.fixTrailingStub(row)
				return
			}
		}
	}
	if join && b.x > 0 {
		offset := 2
		if row.width(b.x-1) != 0 {
			offset = 1
		}
		row.addComb(b.x-offset, r)
		for delta := width - oldWidth; delta > 0; delta-- {
			row.set(b.x, cell{attr: pen}, nil)
			b.x++
		}
		s.fixTrailingStub(row)
		return
	}
	if s.insert {
		row.insertCells(b.x, width-oldWidth, pen)
		if row.width(s.cols-1) == 2 {
			row.setNull(s.cols-1, pen)
		}
	}
	row.set(b.x, cell{ch: r, attr: pen, width: uint8(width)}, nil)
	b.x++
	for w := width; w > 1; w-- {
		row.set(b.x, cell{attr: pen}, nil)
		b.x++
	}
	s.fixTrailingStub(row)
}

// fixTrailingStub clears the second half of a wide character the print
// overwrote the first half of.
func (s *Screen) fixTrailingStub(row *line) {
	b := s.active
	if b.x < s.cols && row.width(b.x) == 0 && !row.hasContent(b.x) {
		row.setNull(b.x, s.penStyle())
	}
}

func (s *Screen) execute(c byte) {
	s.endJoin()
	switch c {
	case '\n', '\v', '\f':
		s.lineFeed()
	case '\r':
		s.active.x = 0
	case '\b':
		s.backspace()
	case '\t':
		if s.active.x < s.cols {
			s.active.x = s.active.nextStop(s.cols, s.active.x)
		}
	case 0x0e:
		s.glevel = 1
	case 0x0f:
		s.glevel = 0
	}
}

func (s *Screen) lineFeed() {
	b := s.active
	b.y++
	switch {
	case b.y == b.scrollBottom+1:
		b.y--
		b.scroll(s.cols, s.eraseStyle(), false)
	case b.y >= s.rows:
		b.y = s.rows - 1
	default:
		b.setWrapped(b.ybase+b.y, false)
	}
	if b.x >= s.cols {
		b.x--
	}
}

func (s *Screen) backspace() {
	b := s.active
	if !s.reverseWrap {
		s.restrictCursor(s.cols - 1)
		if b.x > 0 {
			b.x--
		}
		return
	}
	s.restrictCursor(s.cols)
	if b.x > 0 {
		b.x--
	} else if b.y > b.scrollTop && b.y <= b.scrollBottom {
		if l := s.currentLine(); l != nil && l.wrapped {
			l.wrapped = false
			b.y--
			b.x = s.cols - 1
			if prev := s.currentLine(); prev != nil && prev.width(b.x) != 0 && !prev.hasContent(b.x) {
				b.x--
			}
		}
	}
	s.restrictCursor(s.cols - 1)
}

func (s *Screen) restrictCursor(maxCol int) {
	b := s.active
	b.x = min(maxCol, max(0, b.x))
	if s.origin {
		b.y = min(b.scrollBottom, max(b.scrollTop, b.y))
	} else {
		b.y = min(s.rows-1, max(0, b.y))
	}
}

func (s *Screen) setCursor(x, y int) {
	b := s.active
	if s.origin {
		b.x, b.y = x, b.scrollTop+y
	} else {
		b.x, b.y = x, y
	}
	s.restrictCursor(s.cols - 1)
}

func (s *Screen) moveCursor(dx, dy int) {
	s.restrictCursor(s.cols - 1)
	s.setCursor(s.active.x+dx, s.active.y+dy)
}

// index is xterm's IND.
func (s *Screen) index() {
	s.restrictCursor(s.cols - 1)
	b := s.active
	b.y++
	if b.y == b.scrollBottom+1 {
		b.y--
		b.scroll(s.cols, s.eraseStyle(), false)
	} else if b.y >= s.rows {
		b.y = s.rows - 1
	}
	s.restrictCursor(s.cols - 1)
}

func (s *Screen) reverseIndex() {
	s.restrictCursor(s.cols - 1)
	b := s.active
	if b.y == b.scrollTop {
		top := b.ybase + b.y
		bottom := b.ybase + b.scrollBottom
		copy(b.lines[top+1:bottom+1], b.lines[top:bottom])
		b.lines[top] = newLine(s.cols, s.eraseStyle(), false)
	} else {
		b.y--
		s.restrictCursor(s.cols - 1)
	}
}

func (s *Screen) saveCursor() {
	b := s.active
	b.saved = savedCursor{
		x: b.x, y: b.ybase + b.y, attr: s.pen,
		charsets: append([]charset(nil), s.charsets[:]...), glevel: s.glevel,
		origin: s.origin, wraparound: s.wraparound,
	}
}

func (s *Screen) restoreCursor() {
	b := s.active
	b.x = b.saved.x
	b.y = max(b.saved.y-b.ybase, 0)
	s.pen.fg, s.pen.bg, s.pen.flags = b.saved.attr.fg, b.saved.attr.bg, b.saved.attr.flags
	for i, cs := range b.saved.charsets {
		s.charsets[i] = cs
	}
	s.glevel = b.saved.glevel
	s.origin = b.saved.origin
	s.wraparound = b.saved.wraparound
	s.restrictCursor(s.cols - 1)
}

func (s *Screen) activateAlt() {
	if s.active == s.alt {
		return
	}
	s.alt.fillViewport(s.cols, s.rows, s.eraseStyle())
	s.alt.x, s.alt.y = s.normal.x, s.normal.y
	s.active = s.alt
}

func (s *Screen) activateNormal() {
	if s.active == s.normal {
		return
	}
	s.normal.x, s.normal.y = s.alt.x, s.alt.y
	s.alt.clear(s.cols, s.rows, Scrollback)
	s.active = s.normal
}

func (s *Screen) softReset() {
	b := s.active
	s.cursorHidden = false
	b.scrollTop, b.scrollBottom = 0, s.rows-1
	s.pen = attr{}
	s.insert = false
	s.wraparound = true
	s.reverseWrap = false
	s.origin = false
	s.charsets = [4]charset{'B', 'B', 'B', 'B'}
	s.glevel = 0
	b.saved.x = 0
	b.saved.y = b.ybase
	b.saved.attr = attr{}
	b.saved.charsets = append([]charset(nil), s.charsets[:]...)
}

func (s *Screen) screenAlignment() {
	s.setCursor(0, 0)
	b := s.active
	c := cell{ch: 'E', attr: s.style(attr{fg: s.pen.fg, bg: s.pen.bg, flags: s.pen.flags}), width: 1}
	for y := 0; y < s.rows; y++ {
		abs := b.ybase + b.y + y
		if abs >= len(b.lines) {
			break
		}
		b.lines[abs].wrapped = false
		b.lines[abs].comb = nil
		for i := range b.lines[abs].cells {
			b.lines[abs].cells[i] = c
		}
	}
	s.setCursor(0, 0)
}

func (s *Screen) esc(cmd ansi.Cmd) {
	defer s.endJoin()
	inter := cmd.Intermediate()
	final := cmd.Final()
	switch inter {
	case 0:
		switch final {
		case '7':
			s.saveCursor()
		case '8':
			s.restoreCursor()
		case 'D':
			s.index()
		case 'E':
			s.active.x = 0
			s.index()
		case 'H':
			s.active.tabs[s.active.x] = true
		case 'M':
			s.reverseIndex()
		case 'c':
			s.reset()
		case 'n':
			s.glevel = 2
		case 'o', '|':
			s.glevel = 3
		case '~':
			s.glevel = 1
		}
	case '%':
		if final == '@' || final == 'G' {
			s.glevel = 0
			s.charsets[0] = 'B'
		}
	case '(', ')', '*', '+', '-', '.':
		g := map[byte]int{'(': 0, ')': 1, '*': 2, '+': 3, '-': 1, '.': 2}[inter]
		switch final {
		case '0', 'A':
			s.charsets[g] = charset(final)
		default:
			s.charsets[g] = 'B'
		}
	case '#':
		if final == '8' {
			s.screenAlignment()
		}
	}
}

// decSpecialGraphics is xterm's CHARSETS['0'].
var decSpecialGraphics = map[rune]rune{
	'`': '◆', 'a': '▒', 'b': '␉', 'c': '␌', 'd': '␍',
	'e': '␊', 'f': '°', 'g': '±', 'h': '␤', 'i': '␋',
	'j': '┘', 'k': '┐', 'l': '┌', 'm': '└', 'n': '┼',
	'o': '⎺', 'p': '⎻', 'q': '─', 'r': '⎼', 's': '⎽',
	't': '├', 'u': '┤', 'v': '┴', 'w': '┬', 'x': '│',
	'y': '≤', 'z': '≥', '{': 'π', '|': '≠', '}': '£',
	'~': '·',
}
