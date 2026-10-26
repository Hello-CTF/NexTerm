package shape

import (
	"regexp"
	"strconv"
	"strings"
)

// Selection pointers drawn in front of the current menu item.
func isPointer(s string) bool {
	switch s {
	case "❯", "›", ">", "▶", "►", "▸", "➜", "→", "➤", "*":
		return true
	}
	return false
}

// Radio and checkbox bullets: a filled bullet among hollow ones marks the
// current item of a menu without a pointer.
func bulletKind(s string) (filled, hollow bool) {
	switch s {
	case "●", "◉", "◆", "■", "▣", "☑", "☒":
		return true, false
	case "○", "◯", "◇", "□", "☐":
		return false, true
	}
	return false, false
}

// isRule reports whether a row contains a horizontal rule, which separates
// sections rather than listing options.
func isRule(s string) bool {
	return strings.Contains(s, "───") || strings.Contains(s, "━━━") || strings.Contains(s, "═══")
}

func pointerGap(next string) bool {
	if next == " " {
		return true
	}
	f, h := bulletKind(next)
	return f || h
}

var numberedItem = []*regexp.Regexp{
	regexp.MustCompile(`^(\d{1,2})[.)]\s+(\S.*)$`),
	regexp.MustCompile(`^\[(\d{1,2})\]\s+(\S.*)$`),
	regexp.MustCompile(`^\((\d{1,2})\)\s+(\S.*)$`),
}

type item struct {
	blank  bool
	indent int // display column of the first glyph
	col    int // display column after pointer and bullet
	marked bool
	weak   bool // pointer glyph that also appears in ordinary output
	filled bool
	hollow bool
	num    int // -1 when unnumbered
	label  string
	text   string
	rule   bool // contains a horizontal rule, so it separates sections
}

func parseItem(row string) item {
	text := cleanRow(row)
	l := parseRow(text)
	g := l.g
	at := l.text
	i := 0
	for i < len(g) && at(i) == " " {
		i++
	}
	if i == len(g) {
		return item{blank: true, num: -1}
	}
	it := item{indent: g[i].col, num: -1, text: strings.TrimSpace(text)}
	it.rule = isRule(text)
	skip := func() {
		for i < len(g) && at(i) == " " {
			i++
		}
	}
	// A pointer stands alone: a space or a bullet follows it.
	if isPointer(at(i)) && i+1 < len(g) && pointerGap(at(i+1)) {
		it.marked = true
		it.weak = at(i) == "*" || at(i) == ">"
		i++
		skip()
	}
	if i < len(g) {
		if f, h := bulletKind(at(i)); f || h {
			it.filled, it.hollow = f, h
			i++
			skip()
		}
	}
	if i == len(g) {
		it.col = it.indent
		it.marked, it.filled, it.hollow = false, false, false
		it.label = it.text
		return it
	}
	it.col = g[i].col
	rest := text[g[i].start:]
	it.label = strings.TrimSpace(rest)
	if c := rest[0]; c != '[' && c != '(' && (c < '0' || c > '9') {
		return it
	}
	for _, re := range numberedItem {
		if m := re.FindStringSubmatch(rest); m != nil {
			it.num, _ = strconv.Atoi(m[1])
			it.label = strings.TrimSpace(m[2])
			break
		}
	}
	return it
}

type block struct {
	items      []item
	top        int
	bottom     int // last row of the block, including continuation rows
	cursor     int
	weak       bool
	numbered   bool
	cursorDist int
}

// collect gathers the menu block starting at row start: rows whose content
// begins in the same column, separated at most by single blank rows and by
// deeper indented continuation rows.
func collect(items []item, start int) block {
	b := block{top: start, bottom: start, cursor: -1}
	col := items[start].col
	blanks := 0
	for i := start; i < len(items); i++ {
		it := items[i]
		switch {
		case it.rule:
			return b
		case it.blank:
			blanks++
			if blanks > 1 {
				return b
			}
			continue
		case it.col == col && len(b.items) > 0 && (it.num >= 0) != (b.items[0].num >= 0):
			// A numbered run ends at the first unnumbered row in its
			// column, such as the reply line below a list.
			return b
		case it.col == col:
			b.items = append(b.items, it)
		case it.indent > col && len(b.items) > 0:
		default:
			return b
		}
		blanks = 0
		b.bottom = i
	}
	return b
}

// valid applies the structural menu rules: two or more items, numbering
// that is consecutive from 0 or 1 when present, at most one pointer, and an
// unnumbered menu must show exactly one selected item.
func (b *block) valid() bool {
	if len(b.items) < 2 {
		return false
	}
	numbered, marked, filled, hollow := 0, 0, 0, 0
	for i, it := range b.items {
		if it.num >= 0 {
			numbered++
		}
		if it.marked {
			marked++
			b.cursor = i
			b.weak = it.weak
		}
		if it.filled {
			filled++
		}
		if it.hollow {
			hollow++
		}
	}
	if marked > 1 {
		return false
	}
	if marked == 0 && filled == 1 && hollow == len(b.items)-1 {
		for i, it := range b.items {
			if it.filled {
				b.cursor = i
			}
		}
	}
	if numbered > 0 {
		if numbered != len(b.items) {
			return false
		}
		first := b.items[0].num
		if first > 1 {
			return false
		}
		for i, it := range b.items {
			if it.num != first+i {
				return false
			}
		}
		b.numbered = true
		return true
	}
	return b.cursor >= 0
}

func endsWithShellPrompt(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasSuffix(s, "$") || strings.HasSuffix(s, "#") || strings.HasSuffix(s, "%")
}

func endsWithAsk(s string) bool {
	s = strings.TrimSpace(s)
	for _, suf := range []string{"?", ":", "？", "："} {
		if strings.HasSuffix(s, suf) {
			return true
		}
	}
	return false
}

// Proximity limits, in rows, between a menu and the cursor.
const (
	choiceAbove      = 4 // cursor on a question line above a marked menu
	choiceBelow      = 6 // cursor in an input or hint area below a marked menu
	choiceFooterRows = 4 // non-blank rows allowed below a marked menu
	replyGap         = 2 // rows between an unmarked numbered list and its reply line
	choiceScan       = 24
)

// near reports whether the cursor sits on or just around the block so the
// menu is the live prompt rather than output that scrolled past.
func (s screen) near(b block) bool {
	before, after := s.split()
	if b.cursor >= 0 {
		if s.cr < b.top-choiceAbove || s.cr > b.bottom+choiceBelow {
			return false
		}
		if s.nonBlankBelow(max(b.bottom, s.cr)) > choiceFooterRows {
			return false
		}
		if s.cr > b.bottom && endsWithShellPrompt(cleanRow(before)) {
			return false
		}
		// "*" and ">" also start ordinary output such as `git branch` or
		// quoted text; without numbering they only count while the cursor
		// is on or above the list, or parked at the start of a row below.
		if b.weak && !b.numbered && s.cr > b.bottom && strings.TrimSpace(cleanRow(before)) != "" {
			return false
		}
		return true
	}
	// A numbered list without a pointer is read line by line: the cursor
	// must sit on a reply line right below it that asks for input.
	if s.cr <= b.bottom || s.cr > b.bottom+replyGap {
		return false
	}
	for i := b.bottom + 1; i < s.cr; i++ {
		if !blank(s.rows[i]) {
			return false
		}
	}
	reply := strings.TrimSpace(cleanRow(before))
	if !endsWithAsk(reply) || len([]rune(reply)) < 2 {
		return false
	}
	if strings.TrimSpace(cleanRow(after)) != "" {
		return false
	}
	return s.nonBlankBelow(s.cr) == 0
}

func (s screen) choice() (Prompt, bool) {
	lo := max(0, s.cr-choiceScan)
	hi := min(len(s.rows), s.cr+choiceScan)
	items := make([]item, hi-lo)
	for i := range items {
		items[i] = parseItem(s.rows[lo+i])
	}
	var best *block
	for start := 0; start < len(items); start++ {
		if items[start].blank {
			continue
		}
		b := collect(items, start)
		if !b.valid() {
			continue
		}
		b.top += lo
		b.bottom += lo
		if !s.near(b) {
			continue
		}
		b.cursorDist = max(b.top-s.cr, s.cr-b.bottom, 0)
		if best == nil || b.cursorDist < best.cursorDist {
			best = &b
		}
		start = b.bottom - lo
	}
	if best == nil {
		return Prompt{}, false
	}
	region := make([]string, 0, best.bottom-best.top+2)
	question := ""
	if r := s.prevNonBlank(best.top, 2); r >= 0 && !isRule(s.rows[r]) {
		question = strings.TrimSpace(cleanRow(s.rows[r]))
		region = append(region, question)
	}
	for i := best.top; i <= best.bottom; i++ {
		region = append(region, cleanRow(s.rows[i]))
	}
	opts := make([]Option, len(best.items))
	for i, it := range best.items {
		opts[i] = Option{Label: it.label}
		if best.numbered {
			opts[i].Keys = strconv.Itoa(it.num)
			opts[i].Submit = best.cursor < 0
		}
	}
	return Prompt{
		Kind:        KindChoice,
		Question:    question,
		Options:     opts,
		CursorIndex: best.cursor,
		Fingerprint: fingerprint(KindChoice, region),
	}, true
}
