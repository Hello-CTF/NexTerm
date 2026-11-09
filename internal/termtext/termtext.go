// Package termtext streams recorded terminal output as plain text, the way the lines read in the terminal's main screen.
//
// The renderer is line oriented and keeps only the line being written in memory. It drops escape sequences (colors, OSC titles,
// hyperlinks, mode changes), applies carriage returns, backspaces, tabs and in-line editing (erase, insert and delete characters,
// horizontal cursor moves) to the current line, and writes every finished line with "\n" endings and trailing blanks trimmed.
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

	alt    bool
	result Result
	err    error
	closed bool
	prefix []byte
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
		r.cols = cols
	}
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
	if intermediate != 0 {
		return // charset designations and other two-byte sequences do not change the text
	}
	switch final {
	case 'D', 'E': // IND, NEL
		r.newLine()
	case 'M': // reverse index: a vertical move
		r.verticalMove(r.col - r.rowStart())
	case 'c': // full reset
		if r.alt {
			r.alt = false
		}
		r.verticalMove(0)
	}
}

func (r *Renderer) control(b byte) {
	if r.alt {
		return
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
		if r.cols > 0 {
			next = min(next, r.rowStart()+r.cols-1)
		}
		r.col = max(next, r.col)
	}
}

// rowStart is the first column of the screen row holding the cursor when the line wraps at a known width.
func (r *Renderer) rowStart() int {
	if r.cols <= 0 || r.col == 0 {
		return 0
	}
	// A cursor just past the last column still sits on the row it filled (pending wrap).
	return (r.col - 1) / r.cols * r.cols
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
	n := param(values, 0, 1)
	if n <= 0 {
		n = 1
	}
	switch final {
	case 'C', 'a':
		r.col += n
		r.clampCol()
	case 'D':
		r.col = max(0, r.col-n)
	case 'G', '`':
		r.col = r.rowStart() + n - 1
		r.clampCol()
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

func (r *Renderer) clampCol() {
	if r.cols > 0 {
		r.col = min(r.col, r.rowStart()+r.cols-1)
	}
	r.col = min(r.col, maxLineCells)
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
	width := 1
	if value >= 0x80 {
		width = runewidth.RuneWidth(value)
		if width <= 0 {
			r.addMark(value)
			return
		}
		width = min(width, 2)
	}
	if r.cols > 0 && width == 2 && r.col%r.cols == r.cols-1 {
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

func (r *Renderer) eraseLine(mode int) {
	switch mode {
	case 0:
		if r.col < len(r.line) {
			r.clearPair(r.col)
			r.line = r.line[:r.col]
		}
	case 1:
		r.clearPair(r.col)
		for c := 0; c <= r.col && c < len(r.line); c++ {
			r.line[c] = textCell{r: ' ', width: 1}
		}
	case 2:
		r.line = r.line[:0]
	}
}

func (r *Renderer) deleteChars(n int) {
	if r.col >= len(r.line) {
		return
	}
	r.clearPair(r.col)
	end := min(r.col+n, len(r.line))
	r.clearPair(end)
	r.line = append(r.line[:r.col], r.line[end:]...)
}

func (r *Renderer) insertChars(n int) {
	if r.col >= len(r.line) {
		return
	}
	n = min(n, maxLineCells-len(r.line))
	if n <= 0 {
		return
	}
	r.clearPair(r.col)
	blanks := make([]textCell, n)
	for i := range blanks {
		blanks[i] = textCell{r: ' ', width: 1}
	}
	r.line = append(r.line[:r.col], append(blanks, r.line[r.col:]...)...)
}

func (r *Renderer) eraseChars(n int) {
	end := min(r.col+n, len(r.line))
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
	r.col = min(col, maxLineCells)
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
		r.prefix = append(r.prefix, "] "...)
		if _, err := r.out.Write(r.prefix); err != nil {
			r.err = err
			return
		}
	}
	end := len(r.line)
	for end > 0 && r.line[end-1].r == ' ' && r.line[end-1].width == 1 && len(r.line[end-1].marks) == 0 {
		end--
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
