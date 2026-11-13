// Package keyframe models what xterm.js holds for a stream of terminal
// output, so a transcript's seek keyframes can be built while it is recorded.
//
// The replay view renders transcripts with xterm.js. A keyframe is restored
// there by writing its serialized state into a fresh terminal, so this model
// follows xterm's own algorithms (src/common/InputHandler.ts and
// src/common/buffer/*, xterm 6.1) rather than a generic VT100: cursor
// positions past the last column, wrapped-line flags, reflow on resize, the
// scrollback trimming and the erase attributes all match, and a restored
// keyframe leaves the same buffer a replay from the start would.
//
// Not modelled, because the recorded shells do not rely on them and xterm
// renders them without buffer state: mouse and keyboard modes, cursor style,
// hyperlinks, protected cells, horizontal margins and column scrolling,
// national character sets other than DEC special graphics and UK.
package keyframe

// color is an xterm color: the mode in the top byte, the value below.
type color uint32

const (
	colorDefault = 0
	colorP16     = 1 << 24
	colorP256    = 2 << 24
	colorRGB     = 3 << 24
	colorMode    = 0xff << 24
)

const (
	flagBold = 1 << iota
	flagDim
	flagItalic
	flagBlink
	flagInverse
	flagInvisible
	flagStrike
	flagOverline
	flagUnderline
)

// attr is the rendition of a cell. As in xterm, whether a cell is underlined
// is a flag (saved and restored with the colors) while ulStyle, the
// underline style (1 single, 2 double, 3 curly, 4 dotted, 5 dashed), is an
// extended attribute a cursor restore leaves alone.
type attr struct {
	fg, bg, ul color
	flags      uint16
	ulStyle    uint8
}

// eraseAttr is the rendition xterm gives erased cells: the current
// background color and nothing else.
func (a attr) eraseAttr() attr {
	return attr{bg: a.bg}
}

// cell is one buffer cell. width is 1 for a normal cell, 2 for the head of
// a wide character and 0 for the cell after it. A null cell has ch 0 and
// width 1. attr indexes the screen's rendition table.
type cell struct {
	ch    rune
	attr  uint16
	width uint8
}

// line is one buffer row. comb holds the combining runes appended to cells;
// wrapped reports that the row continues the one above (xterm isWrapped).
type line struct {
	cells   []cell
	comb    map[int][]rune
	wrapped bool
}

func newLine(cols int, fill uint16, wrapped bool) *line {
	l := &line{cells: make([]cell, cols), wrapped: wrapped}
	for i := range l.cells {
		l.cells[i] = cell{attr: fill, width: 1}
	}
	return l
}

func (l *line) length() int { return len(l.cells) }

func (l *line) width(i int) int {
	if i < 0 || i >= len(l.cells) {
		return 1
	}
	return int(l.cells[i].width)
}

func (l *line) hasContent(i int) bool {
	if i < 0 || i >= len(l.cells) {
		return false
	}
	return l.cells[i].ch != 0 || l.comb[i] != nil
}

func (l *line) setNull(i int, a uint16) {
	l.set(i, cell{attr: a, width: 1}, nil)
}

func (l *line) set(i int, c cell, comb []rune) {
	if i < 0 || i >= len(l.cells) {
		return
	}
	l.cells[i] = c
	if comb == nil {
		if l.comb != nil {
			delete(l.comb, i)
		}
		return
	}
	if l.comb == nil {
		l.comb = make(map[int][]rune)
	}
	l.comb[i] = comb
}

func (l *line) addComb(i int, r rune) {
	if i < 0 || i >= len(l.cells) {
		return
	}
	if l.comb == nil {
		l.comb = make(map[int][]rune)
	}
	l.comb[i] = append(append([]rune(nil), l.comb[i]...), r)
}

func (l *line) load(i int) (cell, []rune) {
	return l.cells[i], l.comb[i]
}

// trimmedLength is xterm's getTrimmedLength: the end of the last cell with
// content. Cells that only carry a background do not count.
func (l *line) trimmedLength() int {
	for i := len(l.cells) - 1; i >= 0; i-- {
		if l.hasContent(i) {
			return i + int(l.cells[i].width)
		}
	}
	return 0
}

func (l *line) fill(a uint16) {
	l.comb = nil
	for i := range l.cells {
		l.cells[i] = cell{attr: a, width: 1}
	}
}

// replaceCells is xterm's replaceCells without protection.
func (l *line) replaceCells(start, end int, a uint16) {
	if start > 0 && l.width(start-1) == 2 {
		l.setNull(start-1, a)
	}
	if end < len(l.cells) && end > 0 && l.width(end-1) == 2 {
		l.setNull(end, a)
	}
	for ; start < end && start < len(l.cells); start++ {
		l.setNull(start, a)
	}
}

func (l *line) insertCells(pos, n int, a uint16) {
	size := len(l.cells)
	pos %= size
	if pos > 0 && l.width(pos-1) == 2 {
		l.setNull(pos-1, a)
	}
	if n < size-pos {
		for i := size - pos - n - 1; i >= 0; i-- {
			c, comb := l.load(pos + i)
			l.set(pos+n+i, c, comb)
		}
		for i := 0; i < n; i++ {
			l.setNull(pos+i, a)
		}
	} else {
		for i := pos; i < size; i++ {
			l.setNull(i, a)
		}
	}
	if l.width(size-1) == 2 {
		l.setNull(size-1, a)
	}
}

func (l *line) deleteCells(pos, n int, a uint16) {
	size := len(l.cells)
	pos %= size
	if n < size-pos {
		for i := 0; i < size-pos-n; i++ {
			c, comb := l.load(pos + n + i)
			l.set(pos+i, c, comb)
		}
		for i := size - n; i < size; i++ {
			l.setNull(i, a)
		}
	} else {
		for i := pos; i < size; i++ {
			l.setNull(i, a)
		}
	}
	if pos > 0 && l.width(pos-1) == 2 {
		l.setNull(pos-1, a)
	}
	if l.width(pos) == 0 && !l.hasContent(pos) {
		l.setNull(pos, a)
	}
}

// resize changes the row length; new cells are null cells.
func (l *line) resize(cols int) {
	if cols == len(l.cells) {
		return
	}
	if cols > len(l.cells) {
		grown := make([]cell, cols)
		copy(grown, l.cells)
		for i := len(l.cells); i < cols; i++ {
			grown[i] = cell{width: 1}
		}
		l.cells = grown
		return
	}
	l.cells = l.cells[:cols:cols]
	for i := range l.comb {
		if i >= cols {
			delete(l.comb, i)
		}
	}
}

// copyCellsFrom copies n cells of src starting at srcCol to destCol, in
// reverse order when the ranges of one line overlap that way.
func (l *line) copyCellsFrom(src *line, srcCol, destCol, n int, reverse bool) {
	if reverse {
		for i := n - 1; i >= 0; i-- {
			c, comb := src.load(srcCol + i)
			l.set(destCol+i, c, comb)
		}
		return
	}
	for i := 0; i < n; i++ {
		c, comb := src.load(srcCol + i)
		l.set(destCol+i, c, comb)
	}
}

func (l *line) clone() *line {
	out := &line{cells: append([]cell(nil), l.cells...), wrapped: l.wrapped}
	if len(l.comb) > 0 {
		out.comb = make(map[int][]rune, len(l.comb))
		for i, runes := range l.comb {
			out.comb[i] = runes
		}
	}
	return out
}
