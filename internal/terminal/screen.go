package terminal

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

const ScrollbackLines = 100_000

type lineHistory struct {
	lines []string
	head  int
	size  int
}

func newLineHistory(capacity int) *lineHistory {
	if capacity <= 0 {
		capacity = 1
	}
	return &lineHistory{lines: make([]string, capacity)}
}

func (h *lineHistory) push(line string) {
	capacity := len(h.lines)
	if h.size < capacity {
		h.lines[(h.head+h.size)%capacity] = line
		h.size++
		return
	}
	h.lines[h.head] = line
	h.head = (h.head + 1) % capacity
}

func (h *lineHistory) len() int { return h.size }

func (h *lineHistory) clear() { h.head, h.size = 0, 0 }

func (h *lineHistory) last(n int) []string {
	if n <= 0 || h.size == 0 {
		return nil
	}
	if n > h.size {
		n = h.size
	}
	capacity := len(h.lines)
	out := make([]string, 0, n)
	for i := h.size - n; i < h.size; i++ {
		out = append(out, h.lines[(h.head+i)%capacity])
	}
	return out
}

type cell struct {
	r    rune
	comb []rune
	wide bool
	cont bool
}

type row struct{ cells []cell }

func newRow(cols int) *row {
	r := &row{cells: make([]cell, cols)}
	r.reset()
	return r
}

func (r *row) reset() {
	for i := range r.cells {
		r.cells[i] = cell{}
	}
}

func (r *row) resize(cols int) {
	if cols == len(r.cells) {
		return
	}
	cells := make([]cell, cols)
	copy(cells, r.cells)
	r.cells = cells
	r.normalizeWide()
}

func (r *row) normalizeWide() {
	for i := range r.cells {
		if r.cells[i].wide && (i+1 >= len(r.cells) || !r.cells[i+1].cont) {
			r.cells[i] = cell{}
		}
		if r.cells[i].cont && (i == 0 || !r.cells[i-1].wide) {
			r.cells[i] = cell{}
		}
	}
}

func (r *row) String() string {
	last := -1
	for i := len(r.cells) - 1; i >= 0; i-- {
		c := &r.cells[i]
		if c.cont {
			continue
		}
		if (c.r != 0 && c.r != ' ') || len(c.comb) > 0 {
			last = i
			break
		}
	}
	if last < 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i <= last; i++ {
		c := &r.cells[i]
		if c.cont {
			if i > 0 && r.cells[i-1].wide {
				continue
			}
			b.WriteByte(' ')
			continue
		}
		if c.r == 0 {
			b.WriteByte(' ')
		} else {
			b.WriteRune(c.r)
		}
		for _, cr := range c.comb {
			b.WriteRune(cr)
		}
	}
	return b.String()
}

type cursorState struct {
	r, c        int
	wrapPending bool
}

type grid struct {
	rows []*row
}

func newGrid(cols, rows int) *grid {
	g := &grid{rows: make([]*row, rows)}
	for i := range g.rows {
		g.rows[i] = newRow(cols)
	}
	return g
}

func (g *grid) resize(cols, rows int, history *lineHistory, keepHistory bool, cur *cursorState) {
	for _, r := range g.rows {
		r.resize(cols)
	}
	if rows < len(g.rows) {
		drop := len(g.rows) - rows
		for i := 0; i < drop; i++ {
			if keepHistory {
				history.push(g.rows[i].String())
			}
		}
		g.rows = append([]*row(nil), g.rows[drop:]...)
		cur.r -= drop
	} else if rows > len(g.rows) {
		for len(g.rows) < rows {
			g.rows = append(g.rows, newRow(cols))
		}
	}
}

type screen struct {
	parser *ansi.Parser
	cols   int
	rows   int
	grid   *grid
	main   *grid
	alt    bool

	cur          cursorState
	saved        cursorState
	savedMain    cursorState
	scrollTop    int
	scrollBot    int
	autoWrap     bool
	origin       bool
	insert       bool
	g0dec, g1dec bool
	glG1         bool

	history   *lineHistory
	responses []byte
}

func newScreen(cols, rows, history int) *screen {
	s := &screen{
		cols:      cols,
		rows:      rows,
		grid:      newGrid(cols, rows),
		scrollBot: rows - 1,
		autoWrap:  true,
		history:   newLineHistory(history),
	}
	s.parser = ansi.NewParser()
	s.parser.SetHandler(ansi.Handler{
		Print:     s.print,
		Execute:   s.execute,
		HandleCsi: s.csi,
		HandleEsc: s.esc,
	})
	return s
}

func (s *screen) write(p []byte) {
	for _, b := range p {
		s.parser.Advance(b)
	}
}

func (s *screen) takeResponses() []byte {
	if len(s.responses) == 0 {
		return nil
	}
	out := s.responses
	s.responses = nil
	return out
}

func (s *screen) text() string {
	var b strings.Builder
	for i, r := range s.grid.rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(r.String())
	}
	return b.String()
}

func (s *screen) cursor() (row, col int) { return s.cur.r, s.cur.c }

func (s *screen) size() (cols, rows int) { return s.cols, s.rows }

func (s *screen) altScreen() bool { return s.alt }

func (s *screen) scrollbackLen() int { return s.history.len() }

func (s *screen) historyLines(n int) []string { return s.history.last(n) }

func (s *screen) resize(cols, rows int) {
	s.grid.resize(cols, rows, s.history, !s.alt, &s.cur)
	if s.main != nil {
		var saved cursorState
		s.main.resize(cols, rows, s.history, false, &saved)
	}
	s.cols, s.rows = cols, rows
	s.cur.r = clamp(s.cur.r, 0, rows-1)
	s.cur.c = clamp(s.cur.c, 0, cols-1)
	s.cur.wrapPending = false
	s.saved.r = clamp(s.saved.r, 0, rows-1)
	s.saved.c = clamp(s.saved.c, 0, cols-1)
	s.scrollTop = clamp(s.scrollTop, 0, rows-1)
	s.scrollBot = clamp(s.scrollBot, 0, rows-1)
	if s.scrollTop >= s.scrollBot {
		s.scrollTop, s.scrollBot = 0, rows-1
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (s *screen) print(r rune) {
	if s.glG1 && s.g1dec || !s.glG1 && s.g0dec {
		if mapped, ok := decGraphics[r]; ok {
			r = mapped
		}
	}
	if r < 0x80 {
		s.put(r, 1)
		return
	}
	w := runewidth.RuneWidth(r)
	if w <= 0 {
		s.putCombining(r)
		return
	}
	if w > 2 {
		w = 2
	}
	s.put(r, w)
}

const maxCombiningRunes = 8

func (s *screen) putCombining(r rune) {
	targetR, targetC := s.cur.r, s.cur.c
	if !s.cur.wrapPending && targetC > 0 {
		targetC--
	}
	row := s.grid.rows[targetR]
	if row.cells[targetC].cont && targetC > 0 {
		targetC--
	}
	c := &row.cells[targetC]
	if len(c.comb) >= maxCombiningRunes {
		return
	}
	c.comb = append(c.comb, r)
}

func (r *row) clearPairAt(c int) {
	if c < 0 || c >= len(r.cells) {
		return
	}
	switch {
	case r.cells[c].cont:
		if c > 0 && r.cells[c-1].wide {
			r.cells[c-1] = cell{}
		}
	case r.cells[c].wide:
		if c+1 < len(r.cells) && r.cells[c+1].cont {
			r.cells[c+1] = cell{}
		}
	}
}

func (s *screen) put(r rune, width int) {
	if s.cur.wrapPending && s.autoWrap {
		s.wrap()
	}
	if width == 2 && s.cur.c == s.cols-1 {
		if s.autoWrap {
			row := s.grid.rows[s.cur.r]
			row.clearPairAt(s.cur.c)
			row.cells[s.cur.c] = cell{}
			s.wrap()
		} else {
			width = 1
		}
	}
	if s.insert {
		s.insertChars(width)
	}
	row := s.grid.rows[s.cur.r]
	row.clearPairAt(s.cur.c)
	if width == 2 && s.cur.c+1 < s.cols {
		row.clearPairAt(s.cur.c + 1)
	}
	c := &row.cells[s.cur.c]
	c.r, c.comb, c.wide, c.cont = r, nil, width == 2, false
	if width == 2 && s.cur.c+1 < s.cols {
		next := &row.cells[s.cur.c+1]
		next.r, next.comb, next.wide, next.cont = 0, nil, false, true
	}
	next := s.cur.c + width
	switch {
	case next >= s.cols && s.autoWrap:
		s.cur.c = s.cols - 1
		s.cur.wrapPending = true
	case next >= s.cols:
		s.cur.c = s.cols - 1
	default:
		s.cur.c = next
	}
}

func (s *screen) wrap() {
	s.cur.wrapPending = false
	s.cur.c = 0
	s.lineFeed()
}

func (s *screen) lineFeed() {
	s.cur.wrapPending = false
	if s.cur.r == s.scrollBot {
		s.scrollUp(1)
	} else if s.cur.r < s.rows-1 {
		s.cur.r++
	}
}

func (s *screen) scrollUp(n int) {
	n = min(n, s.scrollBot-s.scrollTop+1)
	fullRegion := s.scrollTop == 0 && s.scrollBot == s.rows-1
	for i := 0; i < n; i++ {
		off := s.grid.rows[s.scrollTop]
		if fullRegion && !s.alt {
			s.history.push(off.String())
		}
		copy(s.grid.rows[s.scrollTop:s.scrollBot], s.grid.rows[s.scrollTop+1:s.scrollBot+1])
		s.grid.rows[s.scrollBot] = off
		off.reset()
	}
}

func (s *screen) scrollDown(n int) {
	n = min(n, s.scrollBot-s.scrollTop+1)
	for i := 0; i < n; i++ {
		off := s.grid.rows[s.scrollBot]
		copy(s.grid.rows[s.scrollTop+1:s.scrollBot+1], s.grid.rows[s.scrollTop:s.scrollBot])
		s.grid.rows[s.scrollTop] = off
		off.reset()
	}
}

func (s *screen) execute(b byte) {
	switch b {
	case '\r':
		s.cur.c = 0
		s.cur.wrapPending = false
	case '\n', '\v', '\f':
		s.lineFeed()
	case '\b':
		if s.cur.wrapPending {
			s.cur.wrapPending = false
		} else if s.cur.c > 0 {
			s.cur.c--
		}
	case '\t':
		s.cur.c = min((s.cur.c/8+1)*8, s.cols-1)
		s.cur.wrapPending = false
	case 0x0e:
		s.glG1 = true
	case 0x0f:
		s.glG1 = false
	}
}

func param(params ansi.Params, i, def int) int {
	n, _, ok := params.Param(i, def)
	if !ok {
		return def
	}
	return n
}

func count(params ansi.Params, i int) int {
	if n := param(params, i, 1); n > 0 {
		return n
	}
	return 1
}

func (s *screen) csi(cmd ansi.Cmd, params ansi.Params) {
	switch cmd {
	case 'A':
		s.moveCursor(s.cur.r-count(params, 0), s.cur.c)
	case 'B':
		s.moveCursor(s.cur.r+count(params, 0), s.cur.c)
	case 'C':
		s.moveCursor(s.cur.r, s.cur.c+count(params, 0))
	case 'D':
		s.moveCursor(s.cur.r, s.cur.c-count(params, 0))
	case 'E':
		s.moveCursor(s.cur.r+count(params, 0), 0)
	case 'F':
		s.moveCursor(s.cur.r-count(params, 0), 0)
	case 'G', '`':
		s.moveCursor(s.cur.r, count(params, 0)-1)
	case 'H', 'f':
		s.absoluteCursor(count(params, 0)-1, count(params, 1)-1)
	case 'd':
		s.absoluteCursor(count(params, 0)-1, s.cur.c)
	case 'J':
		s.eraseDisplay(param(params, 0, 0))
	case 'K':
		s.eraseLine(param(params, 0, 0))
	case 'L':
		s.insertLines(count(params, 0))
	case 'M':
		s.deleteLines(count(params, 0))
	case 'P':
		s.deleteChars(count(params, 0))
	case '@':
		s.insertChars(count(params, 0))
	case 'X':
		s.eraseChars(count(params, 0))
	case 'S':
		s.scrollUp(count(params, 0))
	case 'T':
		s.scrollDown(count(params, 0))
	case 'r':
		s.setScrollRegion(params)
	case 'h':
		if param(params, 0, 0) == 4 {
			s.insert = true
		}
	case 'l':
		if param(params, 0, 0) == 4 {
			s.insert = false
		}
	case 's':
		s.saved = s.cur
	case 'u':
		s.cur = s.saved
		s.clampCursor()
	case 'n':
		s.deviceStatus(param(params, 0, 0))
	case 'c':
		s.responses = append(s.responses, "\x1b[?62;1;6;22c"...)
	case ansi.Cmd(ansi.Command('?', 0, 'h')):
		s.decMode(params, true)
	case ansi.Cmd(ansi.Command('?', 0, 'l')):
		s.decMode(params, false)
	}
}

func (s *screen) clampCursor() {
	if s.origin {
		s.cur.r = clamp(s.cur.r, s.scrollTop, s.scrollBot)
	} else {
		s.cur.r = clamp(s.cur.r, 0, s.rows-1)
	}
	s.cur.c = clamp(s.cur.c, 0, s.cols-1)
}

func (s *screen) moveCursor(r, c int) {
	s.cur.r, s.cur.c = r, c
	s.clampCursor()
	s.cur.wrapPending = false
}

func (s *screen) absoluteCursor(r, c int) {
	if s.origin {
		r += s.scrollTop
	}
	s.moveCursor(r, c)
}

func (s *screen) eraseDisplay(mode int) {
	switch mode {
	case 0:
		s.eraseLine(0)
		for r := s.cur.r + 1; r < s.rows; r++ {
			s.grid.rows[r].reset()
		}
	case 1:
		s.eraseLine(1)
		for r := 0; r < s.cur.r; r++ {
			s.grid.rows[r].reset()
		}
	case 2:
		for _, r := range s.grid.rows {
			r.reset()
		}
	case 3:
		s.history.clear()
	}
}

func (s *screen) eraseLine(mode int) {
	row := s.grid.rows[s.cur.r]
	switch mode {
	case 0:
		for c := s.cur.c; c < s.cols; c++ {
			row.cells[c] = cell{}
		}
		row.normalizeWide()
	case 1:
		for c := 0; c <= s.cur.c && c < s.cols; c++ {
			row.cells[c] = cell{}
		}
		row.normalizeWide()
	case 2:
		row.reset()
	}
}

func (s *screen) insertLines(n int) {
	if s.cur.r < s.scrollTop || s.cur.r > s.scrollBot {
		return
	}
	for i := 0; i < n && i < s.scrollBot-s.cur.r+1; i++ {
		off := s.grid.rows[s.scrollBot]
		copy(s.grid.rows[s.cur.r+1:s.scrollBot+1], s.grid.rows[s.cur.r:s.scrollBot])
		s.grid.rows[s.cur.r] = off
		off.reset()
	}
}

func (s *screen) deleteLines(n int) {
	if s.cur.r < s.scrollTop || s.cur.r > s.scrollBot {
		return
	}
	for i := 0; i < n && i < s.scrollBot-s.cur.r+1; i++ {
		off := s.grid.rows[s.cur.r]
		copy(s.grid.rows[s.cur.r:s.scrollBot], s.grid.rows[s.cur.r+1:s.scrollBot+1])
		s.grid.rows[s.scrollBot] = off
		off.reset()
	}
}

func (s *screen) deleteChars(n int) {
	row := s.grid.rows[s.cur.r]
	cells := row.cells[s.cur.c:]
	n = min(n, len(cells))
	copy(cells, cells[n:])
	for i := len(cells) - n; i < len(cells); i++ {
		cells[i] = cell{}
	}
	row.normalizeWide()
}

func (s *screen) insertChars(n int) {
	row := s.grid.rows[s.cur.r]
	cells := row.cells[s.cur.c:]
	n = min(n, len(cells))
	copy(cells[n:], cells[:len(cells)-n])
	for i := 0; i < n; i++ {
		cells[i] = cell{}
	}
	row.normalizeWide()
}

func (s *screen) eraseChars(n int) {
	row := s.grid.rows[s.cur.r]
	for i := 0; i < n && s.cur.c+i < s.cols; i++ {
		row.cells[s.cur.c+i] = cell{}
	}
	row.normalizeWide()
}

func (s *screen) setScrollRegion(params ansi.Params) {
	top := count(params, 0)
	bot := param(params, 1, s.rows)
	if top < 1 {
		top = 1
	}
	if bot > s.rows {
		bot = s.rows
	}
	if top >= bot {
		s.scrollTop, s.scrollBot = 0, s.rows-1
	} else {
		s.scrollTop, s.scrollBot = top-1, bot-1
	}
	s.absoluteCursor(0, 0)
}

func (s *screen) deviceStatus(n int) {
	switch n {
	case 5:
		s.responses = append(s.responses, "\x1b[0n"...)
	case 6:
		s.responses = append(s.responses, fmt.Sprintf("\x1b[%d;%dR", s.cur.r+1, s.cur.c+1)...)
	}
}

func (s *screen) decMode(params ansi.Params, set bool) {
	params.ForEach(0, func(_, mode int, _ bool) {
		switch mode {
		case 6:
			s.origin = set
			s.absoluteCursor(0, 0)
		case 7:
			s.autoWrap = set
		case 47, 1047:
			s.setAltScreen(set, false)
		case 1048:
			if set {
				s.saved = s.cur
			} else {
				s.cur = s.saved
				s.clampCursor()
			}
		case 1049:
			s.setAltScreen(set, true)
		}
	})
}

func (s *screen) setAltScreen(on, saveCursor bool) {
	if on == s.alt {
		return
	}
	if on {
		if saveCursor {
			s.savedMain = s.cur
			s.cur = cursorState{}
		}
		s.main = s.grid
		s.grid = newGrid(s.cols, s.rows)
		s.alt = true
	} else {
		s.grid = s.main
		s.main = nil
		s.alt = false
		if saveCursor {
			s.cur = s.savedMain
			s.clampCursor()
		}
	}
}

func (s *screen) esc(cmd ansi.Cmd) {
	switch cmd {
	case '7':
		s.saved = s.cur
	case '8':
		s.cur = s.saved
		s.clampCursor()
	case 'D':
		s.lineFeed()
	case 'M':
		if s.cur.r == s.scrollTop {
			s.scrollDown(1)
		} else if s.cur.r > 0 {
			s.cur.r--
		}
	case 'E':
		s.cur.c = 0
		s.lineFeed()
	case 'c':
		s.ris()
	case ansi.Cmd(ansi.Command(0, '(', '0')):
		s.g0dec = true
	case ansi.Cmd(ansi.Command(0, ')', '0')):
		s.g1dec = true
	case ansi.Cmd(ansi.Command(0, '(', 'A')), ansi.Cmd(ansi.Command(0, '(', 'B')):
		s.g0dec = false
	case ansi.Cmd(ansi.Command(0, ')', 'A')), ansi.Cmd(ansi.Command(0, ')', 'B')):
		s.g1dec = false
	}
}

func (s *screen) ris() {
	alt := s.alt
	s.grid = newGrid(s.cols, s.rows)
	if alt {
		s.main = newGrid(s.cols, s.rows)
	}
	s.cur = cursorState{}
	s.saved = cursorState{}
	s.savedMain = cursorState{}
	s.scrollTop, s.scrollBot = 0, s.rows-1
	s.autoWrap = true
	s.origin = false
	s.insert = false
	s.g0dec, s.g1dec = false, false
	s.glG1 = false
}

var decGraphics = map[rune]rune{
	'`': '◆', 'a': '▒', 'b': '␉', 'c': '␌', 'd': '␍',
	'e': '␊', 'f': '°', 'g': '±', 'h': '␤', 'i': '␋',
	'j': '┘', 'k': '┐', 'l': '┌', 'm': '└', 'n': '┼',
	'o': '⎺', 'p': '⎻', 'q': '─', 'r': '⎼', 's': '⎽',
	't': '├', 'u': '┤', 'v': '┴', 'w': '┬', 'x': '│',
	'y': '⩽', 'z': '⩾', '{': 'π', '|': '≠', '}': '£',
	'~': '·',
}
