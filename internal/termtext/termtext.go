// Package termtext streams recorded terminal output as plain text, the way the lines read in the terminal's main screen.
//
// The renderer is line oriented and keeps only the line being written in memory. It drops escape sequences (colors, OSC titles,
// hyperlinks, mode changes), applies carriage returns, backspaces, tabs and in-line editing (erase, insert and delete characters,
// horizontal cursor moves, saved cursor columns) to the current line, maps DEC line-drawing characters, and writes every
// finished line with "\n" endings and trailing blanks trimmed.
//
// A logical line keeps soft-wrapped text together. Cursor moves and character edits stay on the screen row the cursor is on,
// as in a terminal, using the recorded width (fallbackCols until a resize reports one), which also bounds what one sequence
// can add to the line.
//
// Vertical cursor positioning cannot be expressed in a text stream: a vertical move finishes the current line and continues on a
// new one, so programs that redraw several lines in place (multi-line progress displays) leave each redraw as separate lines.
// Output written while a program uses the alternate screen (vim, less, top, htop and other full-screen applications) is left out,
// as it is from a terminal's scrollback; the result counts those segments.
package termtext

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

const (
	// maxLineCells bounds the buffered line; a longer line is written out and continued on the next one.
	maxLineCells = 64 * 1024
	// maxCombining bounds the combining marks kept on one cell.
	maxCombining = 8
	maxCSIParams = 32
	tabWidth     = 8
	// fallbackCols is the screen width assumed until the recording reports one; it bounds cursor moves and row edits.
	fallbackCols = 512
	// maxCols bounds a reported screen width, matching the terminal's own limit.
	maxCols = 1024
	// TimestampLayout is the per-line prefix layout when Options.Timestamps is set.
	TimestampLayout = "2006-01-02 15:04:05"
)

// Options configures a Renderer.
type Options struct {
	// Timestamps prefixes each line with the time its first output arrived.
	Timestamps bool
	// Location formats the timestamps; nil uses time.Local.
	Location *time.Location
}

// Result summarizes a finished rendering.
type Result struct {
	Bytes int64
	Lines int64
	// FullScreenSegments counts alternate-screen sessions whose output was left out.
	FullScreenSegments int64
}

type state uint8

const (
	stateGround state = iota
	stateEscape
	stateEscapeIntermediate
	stateCSI
	stateOSC
	stateString
	stateStringEscape
)

type textCell struct {
	r rune
	// width is 1 or 2 for a cell that starts a character and 0 for the right half of a wide character.
	width uint8
	marks []rune
}

// Renderer converts terminal output into plain text lines. It is not safe for concurrent use.
type Renderer struct {
	out      *bufio.Writer
	counter  *countingWriter
	opts     Options
	state    state
	pending  []byte // incomplete UTF-8 sequence
	params   []byte // CSI parameter and intermediate bytes
	csiOver  bool   // the CSI parameters overflowed and the sequence is ignored
	escInter byte

	line   []textCell
	col    int
	cols   int // terminal width, or 0 while unknown
	lineTS int64
	nowTS  int64
	dirty  bool // the current line holds output or was explicitly started by output
	// wrapPending is set when output filled the last column of a row: the cursor still sits on that row until the next print.
	wrapPending bool

	alt    bool
	result Result
	err    error
	closed bool
	prefix []byte

	// DEC special graphics (line drawing) designations and the active set (SO/SI).
	g0dec, g1dec, glG1 bool
	// savedCol and savedRow hold the cursor saved by DECSC or CSI s.
	savedCol int
	savedRow int64
	saved    bool
	// row counts the screen rows the cursor has been moved to: every finished line and every vertical move, even onto a
	// blank row, starts a new one.
	row int64
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// NewRenderer returns a renderer writing text to out.
func NewRenderer(out io.Writer, opts Options) *Renderer {
	if opts.Location == nil {
		opts.Location = time.Local
	}
	counter := &countingWriter{w: out}
	return &Renderer{out: bufio.NewWriterSize(counter, 64*1024), counter: counter, opts: opts}
}

// Resize records the terminal width, which locates carriage returns and absolute columns on soft-wrapped lines.
func (r *Renderer) Resize(cols int) {
	if cols > 0 {
		r.cols = min(cols, maxCols)
	}
}

// width is the screen width in effect.
func (r *Renderer) width() int {
	if r.cols > 0 {
		return r.cols
	}
	return fallbackCols
}

// rowEnd is the exclusive end column of the screen row holding the cursor.
func (r *Renderer) rowEnd() int {
	return r.rowStart() + r.width()
}

// Write renders output that arrived at tsMS (Unix milliseconds; zero or negative when unknown).
func (r *Renderer) Write(tsMS int64, data []byte) error {
	if r.err != nil {
		return r.err
	}
	if tsMS > 0 {
		r.nowTS = tsMS
	}
	for _, b := range data {
		r.advance(b)
		if r.err != nil {
			return r.err
		}
	}
	return r.err
}

// Close writes the unfinished last line and flushes the output.
func (r *Renderer) Close() (Result, error) {
	if r.closed {
		return r.result, r.err
	}
	r.closed = true
	if r.err == nil && r.state == stateGround && len(r.pending) > 0 {
		r.pending = r.pending[:0]
		r.print(utf8.RuneError)
	}
	if r.err == nil && (r.dirty || len(r.line) > 0) && !r.lineBlank() {
		r.finishLine()
	}
	if r.err == nil {
		r.err = r.out.Flush()
	}
	r.result.Bytes = r.counter.n
	return r.result, r.err
}

func (r *Renderer) advance(b byte) {
	switch r.state {
	case stateGround:
		r.ground(b)
	case stateEscape:
		r.escape(b)
	case stateEscapeIntermediate:
		switch {
		case b >= 0x20 && b <= 0x2f:
			r.escInter = b
		case b >= 0x30 && b <= 0x7e:
			r.state = stateGround
			r.escDispatch(r.escInter, b)
		case b == 0x1b:
			r.state = stateEscape
		default:
			r.state = stateGround
		}
	case stateCSI:
		switch {
		case b >= 0x20 && b <= 0x3f:
			if len(r.params) >= 64 {
				r.csiOver = true
			} else {
				r.params = append(r.params, b)
			}
		case b >= 0x40 && b <= 0x7e:
			r.state = stateGround
			if !r.csiOver {
				r.csiDispatch(b)
			}
		case b == 0x1b:
			r.state = stateEscape
		case b == 0x18 || b == 0x1a:
			r.state = stateGround
		case b < 0x20:
			r.control(b)
		default:
			r.state = stateGround
		}
	case stateOSC:
		switch b {
		case 0x07, 0x18, 0x1a:
			r.state = stateGround
		case 0x1b:
			r.state = stateStringEscape
		}
	case stateString:
		switch b {
		case 0x18, 0x1a:
			r.state = stateGround
		case 0x1b:
			r.state = stateStringEscape
		}
	case stateStringEscape:
		if b == '\\' {
			r.state = stateGround
			return
		}
		// Any other escape ends the string and starts a new sequence.
		r.state = stateEscape
		r.escape(b)
	}
}

func (r *Renderer) ground(b byte) {
	if len(r.pending) > 0 || b >= 0x80 {
		if b < 0x80 || utf8.RuneStart(b) && len(r.pending) > 0 {
			// The pending sequence was cut short.
			r.pending = r.pending[:0]
			r.print(utf8.RuneError)
			if b < 0x80 {
				r.ground(b)
				return
			}
		}
		r.pending = append(r.pending, b)
		if !utf8.FullRune(r.pending) {
			return
		}
		value, size := utf8.DecodeRune(r.pending)
		rest := append([]byte(nil), r.pending[size:]...)
		r.pending = r.pending[:0]
		r.rune(value)
		for _, extra := range rest {
			r.advance(extra)
		}
		return
	}
	switch {
	case b == 0x1b:
		r.state = stateEscape
	case b < 0x20 || b == 0x7f:
		r.control(b)
	default:
		r.print(rune(b))
	}
}

// rune handles a decoded non-ASCII rune, including the C1 controls a UTF-8 terminal accepts as code points.
func (r *Renderer) rune(value rune) {
	switch {
	case value == 0x9b:
		r.startCSI()
	case value == 0x9d:
		r.state = stateOSC
	case value == 0x90 || value == 0x98 || value == 0x9e || value == 0x9f:
		r.state = stateString
	case value >= 0x80 && value <= 0x9f:
		if value == 0x85 {
			r.newLine()
		}
	default:
		r.print(value)
	}
}

func (r *Renderer) escape(b byte) {
	switch {
	case b == '[':
		r.startCSI()
	case b == ']':
		r.state = stateOSC
	case b == 'P' || b == 'X' || b == '^' || b == '_':
		r.state = stateString
	case b >= 0x20 && b <= 0x2f:
		r.escInter = b
		r.state = stateEscapeIntermediate
	case b >= 0x30 && b <= 0x7e:
		r.state = stateGround
		r.escDispatch(0, b)
	case b == 0x1b:
	case b < 0x20:
		r.control(b)
	default:
		r.state = stateGround
	}
}

func (r *Renderer) startCSI() {
	r.state = stateCSI
	r.params = r.params[:0]
	r.csiOver = false
}

func (r *Renderer) escDispatch(intermediate, final byte) {
	switch intermediate {
	case 0:
	case '(', ')':
		// Charset designation: '0' selects DEC special graphics, anything else a text set.
		if intermediate == '(' {
			r.g0dec = final == '0'
		} else {
			r.g1dec = final == '0'
		}
		return
	default:
		return // other two-byte sequences do not change the text
	}
	switch final {
	case '7':
		r.saveCursor()
	case '8':
		r.restoreCursor()
	case 'D', 'E': // IND, NEL
		r.newLine()
	case 'M': // reverse index: a vertical move
		r.verticalMove(r.col - r.rowStart())
	case 'c': // full reset
		r.alt = false
		r.g0dec, r.g1dec, r.glG1, r.saved = false, false, false, false
		r.verticalMove(0)
	}
}

func (r *Renderer) clearWrap() {
	r.wrapPending = false
}

func (r *Renderer) saveCursor() {
	if r.alt {
		return
	}
	r.savedCol = min(r.col-r.rowStart(), r.width()-1)
	r.savedRow = r.row
	r.saved = true
}

// restoreCursor returns to the saved column. When the cursor moved to another row since the save (a finished line or a
// vertical move such as a status line drawn at the bottom), it continues on a new line like any vertical move.
func (r *Renderer) restoreCursor() {
	if r.alt || !r.saved {
		return
	}
	if r.row != r.savedRow {
		r.verticalMove(r.savedCol)
		return
	}
	r.moveInRow(r.savedCol)
}

func (r *Renderer) control(b byte) {
	if r.alt {
		return
	}
	switch b {
	case '\r', '\b', '\t':
		defer r.clearWrap()
	}
	switch b {
	case '\r':
		r.col = r.rowStart()
	case '\n', '\v', '\f':
		r.newLine()
	case '\b':
		if r.col > 0 {
			r.col--
		}
	case '\t':
		next := (r.col/tabWidth + 1) * tabWidth
		next = min(next, r.rowEnd()-1)
		r.col = max(next, r.col)
	case 0x0e:
		r.glG1 = true
	case 0x0f:
		r.glG1 = false
	}
}

// rowStart is the first column of the screen row holding the cursor.
func (r *Renderer) rowStart() int {
	width := r.width()
	if r.wrapPending && r.col > 0 {
		return (r.col - 1) / width * width
	}
	return r.col / width * width
}

func (r *Renderer) csiDispatch(final byte) {
	private := byte(0)
	params := r.params
	if len(params) > 0 && params[0] >= '<' && params[0] <= '?' {
		private = params[0]
		params = params[1:]
	}
	for _, b := range params {
		if b >= 0x20 && b <= 0x2f {
			return // sequences with intermediates (DECSCUSR and friends) do not change the text
		}
	}
	values := parseParams(params)
	if private == '?' && (final == 'h' || final == 'l') {
		for _, mode := range values {
			if mode == 47 || mode == 1047 || mode == 1049 {
				r.setAlt(final == 'h')
			}
		}
		return
	}
	if private != 0 || r.alt {
		return
	}
	if strings.IndexByte("CaDG`ABFEHfdeKP@Xu", final) >= 0 {
		// Cursor moves and edits use the row the cursor is on, then end a pending wrap; SGR and other modes keep it.
		defer r.clearWrap()
	}
	if final == 's' && len(values) == 0 {
		r.saveCursor()
		return
	}
	if final == 'u' && len(values) == 0 {
		r.restoreCursor()
		return
	}
	n := param(values, 0, 1)
	if n <= 0 {
		n = 1
	}
	switch final {
	case 'C', 'a':
		r.moveInRow(r.col - r.rowStart() + n)
	case 'D':
		r.moveInRow(r.col - r.rowStart() - n)
	case 'G', '`':
		r.moveInRow(n - 1)
	case 'A', 'B', 'F', 'E', 'H', 'f', 'd', 'e':
		col := r.col - r.rowStart()
		switch final {
		case 'E', 'F':
			col = 0
		case 'H', 'f':
			col = max(0, param(values, 1, 1)-1)
		}
		r.verticalMove(col)
	case 'K':
		r.eraseLine(param(values, 0, 0))
	case 'P':
		r.deleteChars(n)
	case '@':
		r.insertChars(n)
	case 'X':
		r.eraseChars(n)
	}
}

// moveInRow puts the cursor at column offset of its screen row, clamped to the row like a terminal cursor.
func (r *Renderer) moveInRow(offset int) {
	start := r.rowStart()
	offset = max(0, min(offset, r.width()-1))
	r.col = min(start+offset, maxLineCells)
	r.wrapPending = false
}

func parseParams(raw []byte) []int {
	var values []int
	current, have, sub := 0, false, false
	for _, b := range raw {
		switch {
		case b >= '0' && b <= '9':
			if !sub && current < 1<<20 {
				current = current*10 + int(b-'0')
				have = true
			}
		case b == ':':
			sub = true
		case b == ';':
			if !have {
				current = -1
			}
			if len(values) < maxCSIParams {
				values = append(values, current)
			}
			current, have, sub = 0, false, false
		}
	}
	if have || len(values) > 0 {
		if !have {
			current = -1
		}
		if len(values) < maxCSIParams {
			values = append(values, current)
		}
	}
	return values
}

func param(values []int, index, def int) int {
	if index >= len(values) || values[index] < 0 {
		return def
	}
	return values[index]
}

func (r *Renderer) setAlt(on bool) {
	if on == r.alt {
		return
	}
	r.alt = on
	if on {
		r.result.FullScreenSegments++
	}
}

func (r *Renderer) print(value rune) {
	if r.alt {
		return
	}
	if r.glG1 && r.g1dec || !r.glG1 && r.g0dec {
		if mapped, ok := decGraphics[value]; ok {
			value = mapped
		}
	}
	width := 1
	if value >= 0x80 {
		width = cellWidth(value)
		if width <= 0 {
			r.addMark(value)
			return
		}
		width = min(width, 2)
	}
	if width == 2 && r.col%r.width() == r.width()-1 {
		// A wide character that does not fit the last column moves to the next row.
		r.col++
	}
	if r.col+width > maxLineCells {
		r.finishLine()
		if r.err != nil {
			return
		}
	}
	r.touch()
	r.ensure(r.col + width)
	r.clearPair(r.col)
	if width == 2 {
		r.clearPair(r.col + 1)
	}
	r.line[r.col] = textCell{r: value, width: uint8(width)}
	if width == 2 {
		r.line[r.col+1] = textCell{width: 0}
	}
	r.col += width
	r.wrapPending = r.col%r.width() == 0
}

func (r *Renderer) addMark(mark rune) {
	index := r.col - 1
	for index > 0 && index < len(r.line) && r.line[index].width == 0 {
		index--
	}
	if index < 0 || index >= len(r.line) || len(r.line[index].marks) >= maxCombining {
		return
	}
	r.line[index].marks = append(r.line[index].marks, mark)
}

func (r *Renderer) touch() {
	if !r.dirty {
		r.dirty = true
		r.lineTS = r.nowTS
	}
}

// ensure grows the line with blanks so that it holds n cells.
func (r *Renderer) ensure(n int) {
	for len(r.line) < n {
		r.line = append(r.line, textCell{r: ' ', width: 1})
	}
}

// clearPair blanks both halves of a wide character that overlaps column c.
func (r *Renderer) clearPair(c int) {
	if c < 0 || c >= len(r.line) {
		return
	}
	cell := r.line[c]
	switch {
	case cell.width == 0 && c > 0:
		r.line[c-1] = textCell{r: ' ', width: 1}
		r.line[c] = textCell{r: ' ', width: 1}
	case cell.width == 2 && c+1 < len(r.line):
		r.line[c] = textCell{r: ' ', width: 1}
		r.line[c+1] = textCell{r: ' ', width: 1}
	}
}

// eraseLine erases part of the cursor's screen row; the other rows of a soft-wrapped line stay.
func (r *Renderer) eraseLine(mode int) {
	start, end := r.rowStart(), r.rowEnd()
	// With a pending wrap the cursor still sits on the row's last cell.
	cursor := min(r.col, end-1)
	switch mode {
	case 0:
		r.blank(cursor, end)
	case 1:
		r.blank(start, cursor+1)
	case 2:
		r.blank(start, end)
	}
}

// blank erases the cells in [from, to); a range reaching the end of the line shortens it.
func (r *Renderer) blank(from, to int) {
	if from >= len(r.line) || from >= to {
		return
	}
	r.clearPair(from)
	if to >= len(r.line) {
		r.line = r.line[:from]
		return
	}
	if r.line[to].width == 0 {
		r.clearPair(to) // a wide character cut by the end of the range
	}
	for c := from; c < to; c++ {
		r.line[c] = textCell{r: ' ', width: 1}
	}
}

// deleteChars removes n cells at the cursor; like a terminal it only shifts the rest of the cursor's screen row.
func (r *Renderer) deleteChars(n int) {
	end := min(len(r.line), r.rowEnd())
	if r.col >= end {
		return
	}
	n = min(n, end-r.col)
	// Only a wide character cut in half is blanked; one that starts at the edit point moves with the row.
	if r.line[r.col].width == 0 {
		r.clearPair(r.col)
	}
	if r.col+n < end && r.line[r.col+n].width == 0 {
		r.clearPair(r.col + n)
	}
	copy(r.line[r.col:end], r.line[r.col+n:end])
	for c := end - n; c < end; c++ {
		r.line[c] = textCell{r: ' ', width: 1}
	}
}

// insertChars inserts n blanks at the cursor; cells pushed past the end of the cursor's screen row are lost.
func (r *Renderer) insertChars(n int) {
	rowEnd := r.rowEnd()
	if r.col >= len(r.line) || r.col >= rowEnd {
		return
	}
	n = min(n, rowEnd-r.col)
	end := min(rowEnd, min(len(r.line), rowEnd)+n)
	if r.line[r.col].width == 0 {
		r.clearPair(r.col)
	}
	r.ensure(end)
	copy(r.line[r.col+n:end], r.line[r.col:end-n])
	for c := r.col; c < r.col+n; c++ {
		r.line[c] = textCell{r: ' ', width: 1}
	}
	if last := &r.line[end-1]; last.width == 2 {
		*last = textCell{r: ' ', width: 1}
	}
}

func (r *Renderer) eraseChars(n int) {
	end := min(r.col+n, len(r.line), r.rowEnd())
	if r.col >= end {
		return
	}
	r.clearPair(r.col)
	r.clearPair(end)
	for c := r.col; c < end; c++ {
		r.line[c] = textCell{r: ' ', width: 1}
	}
}

func (r *Renderer) newLine() {
	if r.alt {
		return
	}
	r.touch()
	r.finishLine()
}

// verticalMove ends the current line, unless it is empty, and continues at column col of a new line.
func (r *Renderer) verticalMove(col int) {
	if r.alt {
		return
	}
	if !r.lineBlank() {
		r.finishLine()
	} else {
		r.line = r.line[:0]
		r.dirty = false
	}
	r.col = max(0, min(col, r.width()-1))
	r.wrapPending = false
	r.row++
}

func (r *Renderer) lineBlank() bool {
	for _, cell := range r.line {
		if cell.width != 0 && cell.r != ' ' || len(cell.marks) > 0 {
			return false
		}
	}
	return true
}

func (r *Renderer) finishLine() {
	if r.err != nil {
		return
	}
	end := len(r.line)
	for end > 0 && r.line[end-1].r == ' ' && r.line[end-1].width == 1 && len(r.line[end-1].marks) == 0 {
		end--
	}
	if r.opts.Timestamps {
		ts := r.lineTS
		if !r.dirty || ts <= 0 {
			ts = r.nowTS
		}
		r.prefix = r.prefix[:0]
		r.prefix = append(r.prefix, '[')
		if ts > 0 {
			r.prefix = time.UnixMilli(ts).In(r.opts.Location).AppendFormat(r.prefix, TimestampLayout)
		} else {
			r.prefix = append(r.prefix, "????-??-?? ??:??:??"...)
		}
		r.prefix = append(r.prefix, ']')
		if end > 0 {
			r.prefix = append(r.prefix, ' ')
		}
		if _, err := r.out.Write(r.prefix); err != nil {
			r.err = err
			return
		}
	}
	for _, cell := range r.line[:end] {
		if cell.width == 0 {
			continue
		}
		if _, err := r.out.WriteRune(cell.r); err != nil {
			r.err = err
			return
		}
		for _, mark := range cell.marks {
			if _, err := r.out.WriteRune(mark); err != nil {
				r.err = err
				return
			}
		}
	}
	if err := r.out.WriteByte('\n'); err != nil {
		r.err = err
		return
	}
	r.result.Lines++
	r.line = r.line[:0]
	r.col = 0
	r.dirty = false
	r.wrapPending = false
	r.row++
}

// FixedOffset returns a fixed zone for a UTC offset in minutes.
func FixedOffset(minutes int) *time.Location {
	sign := "+"
	value := minutes
	if value < 0 {
		sign, value = "-", -value
	}
	name := "UTC" + sign + strconv.Itoa(value/60)
	if value%60 != 0 {
		name += ":" + strconv.Itoa(value%60)
	}
	return time.FixedZone(name, minutes*60)
}

// cellWidth is the number of screen cells a non-ASCII rune takes; zero marks a combining rune.
func cellWidth(value rune) int {
	return runewidth.RuneWidth(value)
}

// decGraphics maps the DEC special graphics set to Unicode, as the terminal emulator does.
var decGraphics = map[rune]rune{
	'`': '◆', 'a': '▒', 'b': '␉', 'c': '␌', 'd': '␍',
	'e': '␊', 'f': '°', 'g': '±', 'h': '␤', 'i': '␋',
	'j': '┘', 'k': '┐', 'l': '┌', 'm': '└', 'n': '┼',
	'o': '⎺', 'p': '⎻', 'q': '─', 'r': '⎼', 's': '⎽',
	't': '├', 'u': '┤', 'v': '┴', 'w': '┬', 'x': '│',
	'y': '⩽', 'z': '⩾', '{': 'π', '|': '≠', '}': '£',
	'~': '·',
}
