// Ported from Hugging Face tokenizers 0.23.1 (Apache-2.0) tokenizer/normalizer.rs
// and tokenizer/pattern.rs.
// Modified for NexTerm; see ../third_party/ASTRLINK_NOTICE.md.

package inference

import (
	"errors"
	"unicode/utf8"
)

// errTokenizer reports a tokenizer state that the reference implementation
// would reject or panic on. It never carries user text.
var errTokenizer = errors.New("invalid_tokenizer_output")

// normalizedString mirrors HF tokenizers' NormalizedString: every normalized
// byte keeps the original byte range it came from, so token offsets map back
// to exact UTF-8 byte spans of the input.
type normalizedString struct {
	original   string
	normalized string
	align      [][2]int
	shift      int
}

type charChange struct {
	r      rune
	change int
}

func newNormalizedString(s string) *normalizedString {
	align := make([][2]int, len(s))
	for b := 0; b < len(s); {
		_, size := utf8.DecodeRuneInString(s[b:])
		for i := 0; i < size; i++ {
			align[b+i] = [2]int{b, b + size}
		}
		b += size
	}
	return &normalizedString{original: s, normalized: s, align: align}
}

func (n *normalizedString) originalOffsets() (int, int) {
	return n.shift, n.shift + len(n.original)
}

// convertNormalized maps a normalized byte range to the original string.
func (n *normalizedString) convertNormalized(start, end int) (int, int, bool) {
	if start == end {
		return start, end, true
	}
	if start > end || start < 0 || end > len(n.align) {
		return 0, 0, false
	}
	return n.align[start][0], n.align[end-1][1], true
}

// convertOriginal maps an original byte range to the normalized string.
func (n *normalizedString) convertOriginal(start, end int) (int, int, bool) {
	if start == end {
		return start, end, true
	}
	if start > end {
		return 0, 0, false
	}
	first, last := -1, -1
	for i, a := range n.align {
		if end < a[1] {
			break
		}
		if first < 0 && start <= a[0] && a[0] != a[1] {
			first = i
		}
		last = i + 1
	}
	switch {
	case first >= 0 && last >= 0:
		return first, last, true
	case first >= 0:
		return first, first, true
	case last >= 0:
		return last, last, true
	default:
		return 0, 0, false
	}
}

func charBoundary(s string, i int) bool {
	return i == len(s) || (i >= 0 && i < len(s) && utf8.RuneStart(s[i]))
}

// slice returns the part covering a normalized byte range.
func (n *normalizedString) slice(start, end int) (*normalizedString, bool) {
	if start < 0 || end > len(n.normalized) || start > end || !charBoundary(n.normalized, start) || !charBoundary(n.normalized, end) {
		return nil, false
	}
	originalStart, originalEnd, ok := n.convertNormalized(start, end)
	if !ok {
		return nil, false
	}
	original := ""
	if originalStart >= 0 && originalStart <= originalEnd && originalEnd <= len(n.original) && charBoundary(n.original, originalStart) && charBoundary(n.original, originalEnd) {
		original = n.original[originalStart:originalEnd]
	}
	align := make([][2]int, end-start)
	for i, a := range n.align[start:end] {
		align[i] = [2]int{a[0] - originalStart, a[1] - originalStart}
	}
	return &normalizedString{
		original:   original,
		normalized: n.normalized[start:end],
		align:      align,
		shift:      n.shift + originalStart,
	}, true
}

// transformRange replaces a normalized range. Each change is 1 for an added
// character, -N for a character followed by N removed characters, or 0 for
// a replacement; initialOffset counts characters removed at the start.
func (n *normalizedString) transformRange(start, end int, dest []charChange, initialOffset int) error {
	if start < 0 || end > len(n.normalized) || start > end || !charBoundary(n.normalized, start) || !charBoundary(n.normalized, end) {
		return errTokenizer
	}
	replaced := []rune(n.normalized[start:end])
	next := 0
	take := func(count int) int {
		size := 0
		for ; count > 0 && next < len(replaced); count-- {
			size += utf8.RuneLen(replaced[next])
			next++
		}
		return size
	}
	offset := take(initialOffset) + start
	align := make([][2]int, 0, end-start)
	normalized := make([]byte, 0, end-start)
	for _, item := range dest {
		var a [2]int
		if item.change > 0 {
			if offset >= 1 {
				if offset-1 >= len(n.align) {
					return errTokenizer
				}
				a = n.align[offset-1]
			}
		} else {
			if offset < 0 || offset >= len(n.align) {
				return errTokenizer
			}
			a = n.align[offset]
		}
		if item.change <= 0 {
			offset += take(1)
		}
		if item.change < 0 {
			offset += take(-item.change)
		}
		if !utf8.ValidRune(item.r) {
			return errTokenizer
		}
		size := utf8.RuneLen(item.r)
		for i := 0; i < size; i++ {
			align = append(align, a)
		}
		normalized = utf8.AppendRune(normalized, item.r)
	}
	n.align = append(n.align[:start:start], append(align, n.align[end:]...)...)
	n.normalized = n.normalized[:start] + string(normalized) + n.normalized[end:]
	return nil
}

// transform applies changes over the whole string.
func (n *normalizedString) transform(dest []charChange, initialOffset int) error {
	start, end, ok := n.convertOriginal(0, len(n.original))
	if !ok {
		return nil
	}
	return n.transformRange(start, end, dest, initialOffset)
}

// replaceRune replaces each occurrence of old; inserted characters align
// with the replaced one.
func (n *normalizedString) replaceRune(old rune, content string) {
	var normalized []byte
	var align [][2]int
	last := 0
	for b, r := range n.normalized {
		if r != old {
			continue
		}
		end := b + utf8.RuneLen(r)
		normalized = append(normalized, n.normalized[last:b]...)
		align = append(align, n.align[last:b]...)
		a := n.align[end-1]
		for _, c := range content {
			for i := utf8.RuneLen(c); i > 0; i-- {
				align = append(align, a)
			}
			normalized = utf8.AppendRune(normalized, c)
		}
		last = end
	}
	if last == 0 && normalized == nil {
		return
	}
	normalized = append(normalized, n.normalized[last:]...)
	align = append(align, n.align[last:]...)
	n.normalized = string(normalized)
	n.align = align
}

func (n *normalizedString) prepend(s string) error {
	if n.normalized == "" {
		return nil
	}
	next, size := utf8.DecodeRuneInString(n.normalized)
	changes := make([]charChange, 0, utf8.RuneCountInString(s)+1)
	for i, r := range []rune(s) {
		change := 0
		if i != 0 {
			change = 1
		}
		changes = append(changes, charChange{r, change})
	}
	changes = append(changes, charChange{next, 1})
	return n.transformRange(0, size, changes, 0)
}

type splitBehavior int

const (
	splitRemoved splitBehavior = iota
	splitIsolated
	splitMergedWithPrevious
	splitMergedWithNext
	splitContiguous
)

type patternMatch struct {
	start, end int
	match      bool
}

// regexMatches turns match ranges into the alternating split list produced
// by HF tokenizers' regex Pattern implementation.
func regexMatches(inside string, ranges [][2]int) []patternMatch {
	if inside == "" {
		return []patternMatch{{0, 0, false}}
	}
	matches := make([]patternMatch, 0, 2*len(ranges)+1)
	previous := 0
	for _, r := range ranges {
		if previous != r[0] {
			matches = append(matches, patternMatch{previous, r[0], false})
		}
		matches = append(matches, patternMatch{r[0], r[1], true})
		previous = r[1]
	}
	if previous != len(inside) {
		matches = append(matches, patternMatch{previous, len(inside), false})
	}
	return matches
}

// predicateMatches mirrors the Fn(char) -> bool Pattern implementation.
func predicateMatches(inside string, predicate func(rune) bool) []patternMatch {
	if inside == "" {
		return []patternMatch{{0, 0, false}}
	}
	var matches []patternMatch
	lastOffset, lastSeen := 0, 0
	for b, r := range inside {
		size := utf8.RuneLen(r)
		lastSeen = b + size
		if predicate(r) {
			if lastOffset < b {
				matches = append(matches, patternMatch{lastOffset, b, false})
			}
			matches = append(matches, patternMatch{b, b + size, true})
			lastOffset = b + size
		}
	}
	if lastSeen > lastOffset {
		matches = append(matches, patternMatch{lastOffset, lastSeen, false})
	}
	return matches
}

func invertMatches(matches []patternMatch) []patternMatch {
	for i := range matches {
		matches[i].match = !matches[i].match
	}
	return matches
}

func (n *normalizedString) split(matches []patternMatch, behavior splitBehavior) ([]*normalizedString, error) {
	type piece struct {
		start, end int
		remove     bool
	}
	var pieces []piece
	switch behavior {
	case splitIsolated:
		for _, m := range matches {
			pieces = append(pieces, piece{m.start, m.end, false})
		}
	case splitRemoved:
		for _, m := range matches {
			pieces = append(pieces, piece{m.start, m.end, m.match})
		}
	case splitContiguous:
		previous := false
		for _, m := range matches {
			if m.match == previous && len(pieces) > 0 {
				pieces[len(pieces)-1].end = m.end
			} else {
				pieces = append(pieces, piece{m.start, m.end, false})
			}
			previous = m.match
		}
	case splitMergedWithPrevious:
		previous := false
		for _, m := range matches {
			if m.match && !previous && len(pieces) > 0 {
				pieces[len(pieces)-1].end = m.end
			} else {
				pieces = append(pieces, piece{m.start, m.end, false})
			}
			previous = m.match
		}
	case splitMergedWithNext:
		previous := false
		for i := len(matches) - 1; i >= 0; i-- {
			m := matches[i]
			if m.match && !previous && len(pieces) > 0 {
				pieces[len(pieces)-1].start = m.start
			} else {
				pieces = append(pieces, piece{m.start, m.end, false})
			}
			previous = m.match
		}
		for i, j := 0, len(pieces)-1; i < j; i, j = i+1, j-1 {
			pieces[i], pieces[j] = pieces[j], pieces[i]
		}
	default:
		return nil, errTokenizer
	}
	result := make([]*normalizedString, 0, len(pieces))
	for _, p := range pieces {
		if p.remove {
			continue
		}
		part, ok := n.slice(p.start, p.end)
		if !ok {
			return nil, errTokenizer
		}
		result = append(result, part)
	}
	return result, nil
}
