package keyframe

// charset is an xterm character set designation: 'B' (US, the default), '0'
// (DEC special graphics) or 'A' (UK).
type charset byte

// savedCursor is what DECSC stores in a buffer.
type savedCursor struct {
	x, y       int // y is absolute (ybase + row)
	attr       attr
	charsets   []charset // nil until the first save: a restore then keeps the designations
	glevel     int
	origin     bool
	wraparound bool
}

// buffer is xterm's Buffer: lines holds the scrollback and the viewport,
// ybase is the index of the viewport's first line, and x may equal cols
// while a wrap is pending.
type buffer struct {
	lines         []*line
	maxLength     int
	hasScrollback bool
	ybase         int
	x, y          int
	scrollTop     int
	scrollBottom  int
	tabs          map[int]bool
	saved         savedCursor
}

func newBuffer(cols, rows, scrollback int, hasScrollback bool) *buffer {
	b := &buffer{hasScrollback: hasScrollback, scrollBottom: rows - 1}
	b.maxLength = b.correctLength(rows, scrollback)
	b.saved = savedCursor{wraparound: true}
	b.setupTabStops(cols, -1)
	return b
}

func (b *buffer) correctLength(rows, scrollback int) int {
	if !b.hasScrollback {
		return rows
	}
	return rows + scrollback
}

// fillViewport adds the viewport rows to an empty buffer.
func (b *buffer) fillViewport(cols, rows int, fill uint16) {
	if len(b.lines) > 0 {
		return
	}
	for i := 0; i < rows; i++ {
		b.lines = append(b.lines, newLine(cols, fill, false))
	}
}

// clear empties the buffer as xterm's Buffer.clear does.
func (b *buffer) clear(cols, rows, scrollback int) {
	b.ybase, b.x, b.y = 0, 0, 0
	b.lines = nil
	b.maxLength = b.correctLength(rows, scrollback)
	b.scrollTop, b.scrollBottom = 0, rows-1
	b.setupTabStops(cols, -1)
}

func (b *buffer) line(row int) *line {
	i := b.ybase + row
	if i < 0 || i >= len(b.lines) {
		return nil
	}
	return b.lines[i]
}

func (b *buffer) setWrapped(abs int, wrapped bool) {
	if abs >= 0 && abs < len(b.lines) {
		b.lines[abs].wrapped = wrapped
	}
}

// setupTabStops sets the default stops every 8 columns from i, or from the
// start when i is negative (xterm's setupTabStops).
func (b *buffer) setupTabStops(cols, i int) {
	if i >= 0 {
		if !b.tabs[i] {
			i = b.prevStop(cols, i)
		}
	} else {
		b.tabs = make(map[int]bool)
		i = 0
	}
	for ; i < cols; i += 8 {
		b.tabs[i] = true
	}
}

func (b *buffer) prevStop(cols, x int) int {
	for {
		x--
		if b.tabs[x] || x <= 0 {
			break
		}
	}
	return clampStop(x, cols)
}

func (b *buffer) nextStop(cols, x int) int {
	for {
		x++
		if b.tabs[x] || x >= cols {
			break
		}
	}
	return clampStop(x, cols)
}

func clampStop(x, cols int) int {
	if x >= cols {
		return cols - 1
	}
	if x < 0 {
		return 0
	}
	return x
}

// insertLine inserts l at index i, trimming the oldest line when the buffer
// is full; it reports whether a line was trimmed.
func (b *buffer) insertLine(i int, l *line) bool {
	b.lines = append(b.lines, nil)
	copy(b.lines[i+1:], b.lines[i:])
	b.lines[i] = l
	if len(b.lines) > b.maxLength {
		b.lines = b.lines[1:]
		return true
	}
	return false
}

func (b *buffer) removeLine(i int) {
	copy(b.lines[i:], b.lines[i+1:])
	b.lines[len(b.lines)-1] = nil
	b.lines = b.lines[:len(b.lines)-1]
}

// scroll is xterm's BufferService.scroll for a terminal the user does not
// scroll: a blank line enters at the bottom of the scroll region and, when
// the region starts at the top, the top line moves into the scrollback.
func (b *buffer) scroll(cols int, fill uint16, wrapped bool) {
	blank := newLine(cols, fill, wrapped)
	top := b.ybase + b.scrollTop
	bottom := b.ybase + b.scrollBottom
	if b.scrollTop == 0 {
		full := len(b.lines) >= b.maxLength
		b.insertLine(bottom+1, blank)
		if !full {
			b.ybase++
		}
		return
	}
	copy(b.lines[top:bottom], b.lines[top+1:bottom+1])
	b.lines[bottom] = blank
}

// resize is xterm's Buffer.resize.
func (b *buffer) resize(oldCols, oldRows, newCols, newRows, scrollback int) {
	newMax := b.correctLength(newRows, scrollback)
	if newMax > b.maxLength {
		b.maxLength = newMax
	}
	if len(b.lines) > 0 {
		if oldCols < newCols {
			for _, l := range b.lines {
				l.resize(newCols)
			}
		}
		addToY := 0
		if oldRows < newRows {
			for y := oldRows; y < newRows; y++ {
				if len(b.lines) < newRows+b.ybase {
					if b.ybase > 0 && len(b.lines) <= b.ybase+b.y+addToY+1 {
						b.ybase--
						addToY++
					} else {
						b.lines = append(b.lines, newLine(newCols, 0, false))
					}
				}
			}
		} else {
			for y := oldRows; y > newRows; y-- {
				if len(b.lines) > newRows+b.ybase {
					if len(b.lines) > b.ybase+b.y+1 {
						b.lines = b.lines[:len(b.lines)-1]
					} else {
						b.ybase++
					}
				}
			}
		}
		if newMax < b.maxLength {
			if trim := len(b.lines) - newMax; trim > 0 {
				b.lines = b.lines[trim:]
				b.ybase = max(b.ybase-trim, 0)
				b.saved.y = max(b.saved.y-trim, 0)
			}
			b.maxLength = newMax
		}
		b.x = min(b.x, newCols-1)
		b.y = min(b.y, newRows-1)
		if addToY > 0 {
			b.y += addToY
		}
		b.saved.x = min(b.saved.x, newCols-1)
		b.scrollTop = 0
	}
	b.scrollBottom = newRows - 1
	if b.hasScrollback && oldCols != newCols {
		if newCols > oldCols {
			b.reflowLarger(oldCols, newCols, newRows)
		} else {
			b.reflowSmaller(oldCols, newCols, newRows)
		}
		if oldCols > newCols {
			for _, l := range b.lines {
				l.resize(newCols)
			}
		}
	}
	if len(b.lines) > 0 {
		b.y = min(b.y, max(0, len(b.lines)-b.ybase-1))
	}
}

// wrappedTrimmedLength is xterm's getWrappedLineTrimmedLength.
func wrappedTrimmedLength(lines []*line, i, cols int) int {
	if i == len(lines)-1 {
		return lines[i].trimmedLength()
	}
	endsInNull := !lines[i].hasContent(cols-1) && lines[i].width(cols-1) == 1
	followingStartsWide := lines[i+1].width(0) == 2
	if endsInNull && followingStartsWide {
		return cols - 1
	}
	return cols
}

// reflowLarger is xterm's _reflowLarger with reflowCursorLine off.
func (b *buffer) reflowLarger(oldCols, newCols, newRows int) {
	cursor := b.ybase + b.y
	type removal struct{ index, count int }
	var toRemove []removal
	for y := 0; y < len(b.lines)-1; y++ {
		i := y + 1
		next := b.lines[i]
		if !next.wrapped {
			continue
		}
		wrapped := []*line{b.lines[y]}
		for i < len(b.lines) && next.wrapped {
			wrapped = append(wrapped, next)
			i++
			if i < len(b.lines) {
				next = b.lines[i]
			}
		}
		if cursor >= y && cursor < i {
			y += len(wrapped) - 1
			continue
		}
		destIndex := 0
		destCol := wrappedTrimmedLength(wrapped, destIndex, oldCols)
		srcIndex := 1
		srcCol := 0
		for srcIndex < len(wrapped) {
			srcLength := wrappedTrimmedLength(wrapped, srcIndex, oldCols)
			n := min(srcLength-srcCol, newCols-destCol)
			wrapped[destIndex].copyCellsFrom(wrapped[srcIndex], srcCol, destCol, n, false)
			destCol += n
			if destCol == newCols {
				destIndex++
				destCol = 0
			}
			srcCol += n
			if srcCol == srcLength {
				srcIndex++
				srcCol = 0
			}
			if destCol == 0 && destIndex != 0 && wrapped[destIndex-1].width(newCols-1) == 2 {
				wrapped[destIndex].copyCellsFrom(wrapped[destIndex-1], newCols-1, destCol, 1, false)
				destCol++
				wrapped[destIndex-1].setNull(newCols-1, 0)
			}
		}
		wrapped[destIndex].replaceCells(destCol, newCols, 0)
		count := 0
		for k := len(wrapped) - 1; k > 0; k-- {
			if k > destIndex || wrapped[k].trimmedLength() == 0 {
				count++
			} else {
				break
			}
		}
		if count > 0 {
			toRemove = append(toRemove, removal{index: y + len(wrapped) - count, count: count})
		}
		y += len(wrapped) - 1
	}
	if len(toRemove) == 0 {
		return
	}
	kept := make([]*line, 0, len(b.lines))
	next := 0
	removed := 0
	for i := 0; i < len(b.lines); i++ {
		if next < len(toRemove) && toRemove[next].index == i {
			i += toRemove[next].count - 1
			removed += toRemove[next].count
			next++
			continue
		}
		kept = append(kept, b.lines[i])
	}
	b.lines = kept
	for adjust := removed; adjust > 0; adjust-- {
		if b.ybase == 0 {
			if b.y > 0 {
				b.y--
			}
			if len(b.lines) < newRows {
				b.lines = append(b.lines, newLine(newCols, 0, false))
			}
		} else {
			b.ybase--
		}
	}
	b.saved.y = max(b.saved.y-removed, 0)
}

// newLineLengths is xterm's reflowSmallerGetNewLineLengths.
func newLineLengths(wrapped []*line, oldCols, newCols int) []int {
	needed := 0
	for i := range wrapped {
		needed += wrappedTrimmedLength(wrapped, i, oldCols)
	}
	var lengths []int
	srcCol, srcLine, available := 0, 0, 0
	for available < needed {
		if needed-available < newCols {
			lengths = append(lengths, needed-available)
			break
		}
		srcCol += newCols
		oldLength := wrappedTrimmedLength(wrapped, srcLine, oldCols)
		if srcCol > oldLength {
			srcCol -= oldLength
			srcLine++
		}
		endsWide := wrapped[srcLine].width(srcCol-1) == 2
		if endsWide {
			srcCol--
		}
		length := newCols
		if endsWide {
			length = newCols - 1
		}
		lengths = append(lengths, length)
		available += length
	}
	return lengths
}

// reflowSmaller is xterm's _reflowSmaller with reflowCursorLine off.
func (b *buffer) reflowSmaller(oldCols, newCols, newRows int) {
	// at is the index in the original lines the inserted rows go before.
	type insertion struct {
		at    int
		lines []*line
	}
	var toInsert []insertion
	countToInsert := 0
	for y := len(b.lines) - 1; y >= 0; y-- {
		// The viewport adjustment below pops lines, so y can pass the end
		// (xterm's `!nextLine`).
		if y >= len(b.lines) {
			continue
		}
		next := b.lines[y]
		if !next.wrapped && next.trimmedLength() <= newCols {
			continue
		}
		wrapped := []*line{next}
		for next.wrapped && y > 0 {
			y--
			next = b.lines[y]
			wrapped = append([]*line{next}, wrapped...)
		}
		cursor := b.ybase + b.y
		if cursor >= y && cursor < y+len(wrapped) {
			continue
		}
		lastLength := wrapped[len(wrapped)-1].trimmedLength()
		lengths := newLineLengths(wrapped, oldCols, newCols)
		linesToAdd := len(lengths) - len(wrapped)
		var trimmed int
		if b.ybase == 0 && b.y != len(b.lines)-1 {
			trimmed = max(0, b.y-b.maxLength+linesToAdd)
		} else {
			trimmed = max(0, len(b.lines)-b.maxLength+linesToAdd)
		}
		var added []*line
		for i := 0; i < linesToAdd; i++ {
			added = append(added, newLine(newCols, 0, true))
		}
		if len(added) > 0 {
			toInsert = append(toInsert, insertion{at: y + len(wrapped), lines: added})
			countToInsert += len(added)
		}
		wrapped = append(wrapped, added...)
		destIndex := len(lengths) - 1
		destCol := lengths[destIndex]
		if destCol == 0 {
			destIndex--
			destCol = lengths[destIndex]
		}
		srcIndex := len(wrapped) - linesToAdd - 1
		srcCol := lastLength
		for srcIndex >= 0 {
			n := min(srcCol, destCol)
			if destIndex < 0 || destIndex >= len(wrapped) {
				break
			}
			wrapped[destIndex].copyCellsFrom(wrapped[srcIndex], srcCol-n, destCol-n, n, true)
			destCol -= n
			if destCol == 0 {
				destIndex--
				if destIndex >= 0 {
					destCol = lengths[destIndex]
				}
			}
			srcCol -= n
			if srcCol == 0 {
				srcIndex--
				srcCol = wrappedTrimmedLength(wrapped, max(srcIndex, 0), oldCols)
			}
		}
		for i := range wrapped {
			if i < len(lengths) && lengths[i] < newCols {
				wrapped[i].setNull(lengths[i], 0)
			}
		}
		for adjust := linesToAdd - trimmed; adjust > 0; adjust-- {
			if b.ybase == 0 {
				if b.y < newRows-1 {
					b.y++
					b.lines = b.lines[:len(b.lines)-1]
				} else {
					b.ybase++
				}
			} else if b.ybase < min(b.maxLength, len(b.lines)+countToInsert)-newRows {
				b.ybase++
			}
		}
		b.saved.y = min(b.saved.y+linesToAdd, b.ybase+newRows-1)
	}
	if len(toInsert) == 0 {
		return
	}
	// Groups were found bottom up, so the last insertion is the topmost.
	merged := make([]*line, 0, len(b.lines)+countToInsert)
	next := len(toInsert) - 1
	for i := 0; i < len(b.lines); i++ {
		for next >= 0 && toInsert[next].at <= i {
			merged = append(merged, toInsert[next].lines...)
			next--
		}
		merged = append(merged, b.lines[i])
	}
	for ; next >= 0; next-- {
		merged = append(merged, toInsert[next].lines...)
	}
	if trim := len(merged) - b.maxLength; trim > 0 {
		merged = merged[trim:]
	}
	b.lines = merged
}
