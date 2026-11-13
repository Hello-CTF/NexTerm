package keyframe

import "github.com/charmbracelet/x/ansi"

// params are CSI parameters as xterm's IParams sees them: a missing
// parameter is 0, a missing sub-parameter -1.
type params struct {
	vals []int
	subs map[int][]int
}

func toParams(p ansi.Params) params {
	out := params{subs: map[int][]int{}}
	for i := 0; i < len(p); i++ {
		out.vals = append(out.vals, p[i].Param(0))
		top := len(out.vals) - 1
		for p[i].HasMore() && i+1 < len(p) {
			i++
			out.subs[top] = append(out.subs[top], p[i].Param(-1))
		}
	}
	if len(out.vals) == 0 {
		out.vals = []int{0}
	}
	return out
}

func (p params) at(i int) int {
	if i < len(p.vals) {
		return p.vals[i]
	}
	return 0
}

// or1 is xterm's `params.params[i] || 1`.
func (p params) or1(i int) int {
	if v := p.at(i); v != 0 {
		return v
	}
	return 1
}

func (s *Screen) csi(cmd ansi.Cmd, raw ansi.Params) {
	// xterm clears the join state after the handler: REP still sees the
	// preceding character.
	defer s.endJoin()
	p := toParams(raw)
	b := s.active
	prefix, inter, final := cmd.Prefix(), cmd.Intermediate(), cmd.Final()
	switch {
	case prefix == '?' && inter == 0:
		switch final {
		case 'h':
			s.setPrivateModes(p, true)
		case 'l':
			s.setPrivateModes(p, false)
		case 'J':
			s.eraseInDisplay(p.at(0))
		case 'K':
			s.eraseInLine(p.at(0))
		}
		return
	case prefix != 0:
		return
	case inter == '!' && final == 'p':
		s.softReset()
		return
	case inter != 0:
		return
	}
	switch final {
	case '@':
		s.restrictCursor(s.cols - 1)
		if l := s.currentLine(); l != nil {
			l.insertCells(b.x, p.or1(0), s.eraseStyle())
		}
	case 'A':
		if diff := b.y - b.scrollTop; diff >= 0 {
			s.moveCursor(0, -min(diff, p.or1(0)))
		} else {
			s.moveCursor(0, -p.or1(0))
		}
	case 'B':
		if diff := b.scrollBottom - b.y; diff >= 0 {
			s.moveCursor(0, min(diff, p.or1(0)))
		} else {
			s.moveCursor(0, p.or1(0))
		}
	case 'C', 'a':
		s.moveCursor(p.or1(0), 0)
	case 'D':
		s.moveCursor(-p.or1(0), 0)
	case 'E':
		s.csi(ansi.Cmd('B'), raw)
		b.x = 0
	case 'F':
		s.csi(ansi.Cmd('A'), raw)
		b.x = 0
	case 'G', '`':
		s.setCursor(p.or1(0)-1, b.y)
	case 'H', 'f':
		x := 0
		if len(p.vals) >= 2 {
			x = p.or1(1) - 1
		}
		s.setCursor(x, p.or1(0)-1)
	case 'I':
		if b.x < s.cols {
			for n := p.or1(0); n > 0; n-- {
				b.x = b.nextStop(s.cols, b.x)
			}
		}
	case 'J':
		s.eraseInDisplay(p.at(0))
	case 'K':
		s.eraseInLine(p.at(0))
	case 'L':
		s.insertLines(p.or1(0))
	case 'M':
		s.deleteLines(p.or1(0))
	case 'P':
		s.restrictCursor(s.cols - 1)
		if l := s.currentLine(); l != nil {
			l.deleteCells(b.x, p.or1(0), s.eraseStyle())
		}
	case 'S':
		for n := p.or1(0); n > 0; n-- {
			b.removeLine(b.ybase + b.scrollTop)
			b.insertLineAt(b.ybase+b.scrollBottom, newLine(s.cols, s.eraseStyle(), false))
		}
	case 'T', '^':
		for n := p.or1(0); n > 0; n-- {
			b.removeLine(b.ybase + b.scrollBottom)
			b.insertLineAt(b.ybase+b.scrollTop, newLine(s.cols, 0, false))
		}
	case 'X':
		s.restrictCursor(s.cols - 1)
		if l := s.currentLine(); l != nil {
			l.replaceCells(b.x, b.x+p.or1(0), s.eraseStyle())
		}
	case 'Z':
		if b.x < s.cols {
			for n := p.or1(0); n > 0; n-- {
				b.x = b.prevStop(s.cols, b.x)
			}
		}
	case 'b':
		s.repeatPreceding(p.or1(0))
	case 'd':
		s.setCursor(b.x, p.or1(0)-1)
	case 'e':
		s.moveCursor(0, p.or1(0))
	case 'g':
		switch p.at(0) {
		case 0:
			delete(b.tabs, b.x)
		case 3:
			b.tabs = map[int]bool{}
		}
	case 'h', 'l':
		for _, mode := range p.vals {
			if mode == 4 {
				s.insert = final == 'h'
			}
		}
	case 'm':
		s.charAttributes(p)
	case 'r':
		top := p.or1(0)
		bottom := s.rows
		if len(p.vals) >= 2 {
			if v := p.vals[1]; v != 0 && v <= s.rows {
				bottom = v
			}
		}
		if bottom > top {
			b.scrollTop, b.scrollBottom = top-1, bottom-1
			s.setCursor(0, 0)
		}
	case 's':
		s.saveCursor()
	case 'u':
		s.restoreCursor()
	}
}

// insertLineAt inserts l at index i without trimming: the callers remove a
// line first.
func (b *buffer) insertLineAt(i int, l *line) {
	b.lines = append(b.lines, nil)
	copy(b.lines[i+1:], b.lines[i:])
	b.lines[i] = l
}

func (s *Screen) eraseInDisplay(mode int) {
	s.restrictCursor(s.cols)
	b := s.active
	erase := s.eraseStyle()
	resetLine := func(row int) {
		if l := b.line(row); l != nil {
			l.fill(erase)
			l.wrapped = false
		}
	}
	switch mode {
	case 0:
		if l := b.line(b.y); l != nil {
			l.replaceCells(b.x, s.cols, erase)
			if b.x == 0 {
				l.wrapped = false
			}
		}
		for row := b.y + 1; row < s.rows; row++ {
			resetLine(row)
		}
	case 1:
		if l := b.line(b.y); l != nil {
			l.replaceCells(0, b.x+1, erase)
			l.wrapped = false
		}
		if b.x+1 >= s.cols {
			// xterm passes the viewport row where an absolute one is
			// expected; kept so the flags match.
			b.setWrapped(b.y+1, false)
		}
		for row := b.y - 1; row >= 0; row-- {
			resetLine(row)
		}
	case 2:
		for row := s.rows - 1; row >= 0; row-- {
			resetLine(row)
		}
	case 3:
		if size := len(b.lines) - s.rows; size > 0 {
			b.lines = b.lines[size:]
			b.ybase = max(b.ybase-size, 0)
		}
	}
}

func (s *Screen) eraseInLine(mode int) {
	s.restrictCursor(s.cols)
	b := s.active
	l := b.line(b.y)
	if l == nil {
		return
	}
	erase := s.eraseStyle()
	switch mode {
	case 0:
		l.replaceCells(b.x, s.cols, erase)
		if b.x == 0 {
			l.wrapped = false
		}
	case 1:
		l.replaceCells(0, b.x+1, erase)
	case 2:
		l.replaceCells(0, s.cols, erase)
		l.wrapped = false
	}
}

func (s *Screen) insertLines(n int) {
	s.restrictCursor(s.cols - 1)
	b := s.active
	if b.y > b.scrollBottom || b.y < b.scrollTop {
		return
	}
	row := b.ybase + b.y
	bottom := b.ybase + b.scrollBottom + 1
	for ; n > 0; n-- {
		b.removeLine(bottom - 1)
		b.insertLineAt(row, newLine(s.cols, s.eraseStyle(), false))
	}
	b.x = 0
}

func (s *Screen) deleteLines(n int) {
	s.restrictCursor(s.cols - 1)
	b := s.active
	if b.y > b.scrollBottom || b.y < b.scrollTop {
		return
	}
	row := b.ybase + b.y
	bottom := b.ybase + b.scrollBottom
	for ; n > 0; n-- {
		b.removeLine(row)
		b.insertLineAt(bottom, newLine(s.cols, s.eraseStyle(), false))
	}
	b.x = 0
}

// repeatPreceding is xterm's REP: it prints the preceding character's cell
// content again.
func (s *Screen) repeatPreceding(n int) {
	if s.joinWidth == 0 {
		return
	}
	b := s.active
	l := s.currentLine()
	x := b.x - s.joinWidth
	if l == nil || x < 0 || x >= l.length() {
		return
	}
	c, comb := l.load(x)
	if c.ch == 0 && comb == nil {
		return
	}
	text := append([]rune{c.ch}, comb...)
	for ; n > 0; n-- {
		for _, r := range text {
			s.print(r)
		}
	}
}

func (s *Screen) setPrivateModes(p params, set bool) {
	for _, mode := range p.vals {
		switch mode {
		case 2:
			if set {
				s.charsets = [4]charset{'B', 'B', 'B', 'B'}
			}
		case 6:
			s.origin = set
			s.setCursor(0, 0)
		case 7:
			s.wraparound = set
		case 25:
			s.cursorHidden = !set
		case 45:
			s.reverseWrap = set
		case 1048:
			if set {
				s.saveCursor()
			} else {
				s.restoreCursor()
			}
		case 1049, 47, 1047:
			if set {
				if mode == 1049 {
					s.saveCursor()
				}
				s.activateAlt()
			} else {
				s.activateNormal()
				if mode == 1049 {
					s.restoreCursor()
				}
			}
		}
	}
}

func updateColor(c color, mode, c1, c2, c3 int) color {
	switch mode {
	case 2:
		return colorRGB | color(c1&0xff)<<16 | color(c2&0xff)<<8 | color(c3&0xff)
	case 5:
		return colorP256 | color(c1&0xff)
	}
	return c
}

// extractColor is xterm's _extractColor; it returns how many parameters it
// consumed after pos.
func (s *Screen) extractColor(p params, pos int) int {
	accu := [6]int{0, 0, -1, 0, 0, 0}
	cSpace := 0
	advance := 0
	for {
		accu[advance+cSpace] = p.at(pos + advance)
		if subs, ok := p.subs[pos+advance]; ok && len(subs) > 0 {
			i := 0
			for {
				if accu[1] == 5 {
					cSpace = 1
				}
				accu[advance+i+1+cSpace] = subs[i]
				i++
				if !(i < len(subs) && i+advance+1+cSpace < len(accu)) {
					break
				}
			}
			break
		}
		if (accu[1] == 5 && advance+cSpace >= 2) || (accu[1] == 2 && advance+cSpace >= 5) {
			break
		}
		if accu[1] != 0 {
			cSpace = 1
		}
		advance++
		if !(advance+pos < len(p.vals) && advance+cSpace < len(accu)) {
			break
		}
	}
	for i := 2; i < len(accu); i++ {
		if accu[i] == -1 {
			accu[i] = 0
		}
	}
	switch accu[0] {
	case 38:
		s.pen.fg = updateColor(s.pen.fg, accu[1], accu[3], accu[4], accu[5])
	case 48:
		s.pen.bg = updateColor(s.pen.bg, accu[1], accu[3], accu[4], accu[5])
	case 58:
		s.pen.ul = updateColor(s.pen.ul, accu[1], accu[3], accu[4], accu[5])
	}
	return advance
}

// underline is xterm's _processUnderline.
func (s *Screen) underline(style int) {
	if style < 0 || style > 5 {
		style = 1
	}
	s.pen.ulStyle = uint8(style)
	if style == 0 {
		s.pen.flags &^= flagUnderline
	} else {
		s.pen.flags |= flagUnderline
	}
}

// charAttributes is xterm's SGR handler.
func (s *Screen) charAttributes(p params) {
	if len(p.vals) == 1 && p.vals[0] == 0 {
		s.pen = attr{}
		return
	}
	a := &s.pen
	for i := 0; i < len(p.vals); i++ {
		v := p.vals[i]
		switch {
		case v >= 30 && v <= 37:
			a.fg = colorP16 | color(v-30)
		case v >= 40 && v <= 47:
			a.bg = colorP16 | color(v-40)
		case v >= 90 && v <= 97:
			a.fg = colorP16 | color(v-90+8)
		case v >= 100 && v <= 107:
			a.bg = colorP16 | color(v-100+8)
		case v == 0:
			s.pen = attr{}
		case v == 1:
			a.flags |= flagBold
		case v == 2:
			a.flags |= flagDim
		case v == 3:
			a.flags |= flagItalic
		case v == 4:
			style := 1
			if subs := p.subs[i]; len(subs) > 0 {
				style = subs[0]
			}
			s.underline(style)
		case v == 5:
			a.flags |= flagBlink
		case v == 7:
			a.flags |= flagInverse
		case v == 8:
			a.flags |= flagInvisible
		case v == 9:
			a.flags |= flagStrike
		case v == 21:
			s.underline(2)
		case v == 22:
			a.flags &^= flagBold | flagDim
		case v == 23:
			a.flags &^= flagItalic
		case v == 24:
			s.underline(0)
		case v == 25:
			a.flags &^= flagBlink
		case v == 27:
			a.flags &^= flagInverse
		case v == 28:
			a.flags &^= flagInvisible
		case v == 29:
			a.flags &^= flagStrike
		case v == 39:
			a.fg = colorDefault
		case v == 49:
			a.bg = colorDefault
		case v == 38 || v == 48 || v == 58:
			i += s.extractColor(p, i)
		case v == 53:
			a.flags |= flagOverline
		case v == 55:
			a.flags &^= flagOverline
		case v == 221:
			a.flags &^= flagBold
		case v == 222:
			a.flags &^= flagDim
		case v == 59:
			a.ul = colorDefault
		}
	}
}
