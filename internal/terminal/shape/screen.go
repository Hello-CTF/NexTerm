package shape

import (
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// glyph is one printed character of a row, a base rune plus any combining
// marks, held as the byte range [start, end) of the row text. It starts at
// display column col.
type glyph struct {
	start, end int
	col        int
}

// runeWidth mirrors the terminal emulator: ASCII is one column, zero-width
// runes combine with the previous glyph and anything wider is two columns.
func runeWidth(r rune) int {
	if r < 0x80 {
		return 1
	}
	w := runewidth.RuneWidth(r)
	if w > 2 {
		w = 2
	}
	return w
}

// line is a parsed screen row.
type line struct {
	s string
	g []glyph
}

func parseRow(s string) line {
	out := make([]glyph, 0, len(s))
	col := 0
	for i, r := range s {
		w := runeWidth(r)
		if w <= 0 {
			if n := len(out); n > 0 {
				out[n-1].end = i + utf8.RuneLen(r)
			}
			continue
		}
		out = append(out, glyph{start: i, end: i + utf8.RuneLen(r), col: col})
		col += w
	}
	return line{s: s, g: out}
}

func (l line) text(i int) string { return l.s[l.g[i].start:l.g[i].end] }

func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += max(runeWidth(r), 0)
	}
	return w
}

// slice returns the text of glyphs starting in display columns [lo, hi).
func (l line) slice(lo, hi int) string {
	first, last := -1, -1
	for i, x := range l.g {
		if x.col >= lo && x.col < hi {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return ""
	}
	return l.s[l.g[first].start:l.g[last].end]
}

// glyphAt returns the glyph text starting at display column col of s.
func glyphAt(s string, col int) string {
	c, start := 0, -1
	for i, r := range s {
		w := runeWidth(r)
		if w <= 0 {
			continue
		}
		if start >= 0 {
			return s[start:i]
		}
		if c == col {
			start = i
		} else if c > col {
			return ""
		}
		c += w
	}
	if start >= 0 {
		return s[start:]
	}
	return ""
}

// Vertical border glyphs drawn by multiplexers and TUI frames.
func isVBar(s string) bool {
	switch s {
	case "│", "┃", "║", "╎", "╏", "┆", "┇", "┊", "┋":
		return true
	}
	return false
}

// cleanRow blanks one frame border at each edge of a row so framed content
// lines up with unframed content, and trims trailing space.
func cleanRow(s string) string {
	s = strings.TrimRight(s, " ")
	body := strings.TrimLeft(s, " ")
	if r, size := utf8.DecodeRuneInString(body); size > 0 && isVBar(string(r)) {
		s = s[:len(s)-len(body)] + " " + body[size:]
	}
	if r, size := utf8.DecodeLastRuneInString(s); size > 0 && isVBar(string(r)) {
		s = s[:len(s)-size]
	}
	return strings.TrimRight(s, " ")
}

type screen struct {
	rows []string
	cr   int
	cc   int
}

func newScreen(rows []string, cr, cc int) screen {
	return screen{rows: rows, cr: cr, cc: cc}
}

// paneWindow bounds how many rows around the cursor a border must span to
// count as a pane or frame edge.
const paneWindow = 3

// clipPane restricts the screen to the multiplexer pane or frame that holds
// the cursor: a vertical border glyph on the cursor row that continues for
// at least paneWindow rows through the cursor row bounds the region.
func (s screen) clipPane() screen {
	cur := parseRow(s.rows[s.cr])
	left, right := -1, -1
	for i := len(cur.g) - 1; i >= 0; i-- {
		if cur.g[i].col >= s.cc {
			continue
		}
		if t := cur.text(i); isVBar(t) && s.verticalRun(cur.g[i].col, t) {
			left = cur.g[i].col
			break
		}
	}
	for i, g := range cur.g {
		t := cur.text(i)
		if g.col < s.cc || t == " " {
			continue
		}
		if isVBar(t) && s.verticalRun(g.col, t) {
			right = g.col
		}
		break
	}
	if left < 0 && right < 0 {
		return s
	}
	lo, hi := left+1, int(^uint(0)>>1)
	if right >= 0 {
		hi = right
	}
	rows := make([]string, len(s.rows))
	for i, r := range s.rows {
		rows[i] = strings.TrimRight(parseRow(r).slice(lo, hi), " ")
	}
	return screen{rows: rows, cr: s.cr, cc: s.cc - lo}
}

func (s screen) verticalRun(col int, bar string) bool {
	n := 1
	for i := s.cr - 1; i >= 0 && glyphAt(s.rows[i], col) == bar; i-- {
		n++
	}
	for i := s.cr + 1; i < len(s.rows) && glyphAt(s.rows[i], col) == bar; i++ {
		n++
	}
	return n >= paneWindow
}

// split returns the cursor row's text before and after the cursor column.
func (s screen) split() (before, after string) {
	l := parseRow(s.rows[s.cr])
	return l.slice(0, s.cc), l.slice(s.cc, int(^uint(0)>>1))
}

func blank(s string) bool { return strings.TrimSpace(cleanRow(s)) == "" }

// nonBlankBelow counts non-blank rows after row r.
func (s screen) nonBlankBelow(r int) int {
	n := 0
	for i := r + 1; i < len(s.rows); i++ {
		if !blank(s.rows[i]) {
			n++
		}
	}
	return n
}

// prevNonBlank returns the nearest non-blank row above r within limit rows.
func (s screen) prevNonBlank(r, limit int) int {
	for i := r - 1; i >= 0 && i >= r-limit; i-- {
		if !blank(s.rows[i]) {
			return i
		}
	}
	return -1
}
