package inference

import (
	"strings"
	"unicode/utf8"
)

// Oniguruma's Unicode \s (CR_Space), which equals Unicode White_Space.
func onigSpace(r rune) bool {
	switch {
	case r >= 0x09 && r <= 0x0d, r == 0x20, r == 0x85, r == 0xa0, r == 0x1680,
		r >= 0x2000 && r <= 0x200a, r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000:
		return true
	}
	return false
}

// leadingSpacePattern is the AstrLink Guard pre-tokenizer split
// `(?<![0-9\s])\s+|^\s+`. Go's RE2 has no lookbehind, so the onig semantics
// are implemented directly: a whitespace run matches where the previous
// character is not an ASCII digit or whitespace, at the start of the text, or
// (onig's default `^` is a line anchor) right after "\n".
const leadingSpacePattern = `(?<![0-9\s])\s+|^\s+`

func leadingSpaceMatches(s string) [][2]int {
	var matches [][2]int
	for p := 0; p < len(s); {
		r, size := utf8.DecodeRuneInString(s[p:])
		if !onigSpace(r) {
			p += size
			continue
		}
		start := p == 0
		if !start {
			previous, _ := utf8.DecodeLastRuneInString(s[:p])
			start = previous == '\n' || !((previous >= '0' && previous <= '9') || onigSpace(previous))
		}
		if !start {
			p += size
			continue
		}
		end := p
		for end < len(s) {
			r, size := utf8.DecodeRuneInString(s[end:])
			if !onigSpace(r) {
				break
			}
			end += size
		}
		matches = append(matches, [2]int{p, end})
		p = end
	}
	return matches
}

func literalMatches(s, literal string) [][2]int {
	var matches [][2]int
	if literal == "" {
		return nil
	}
	for offset := 0; offset <= len(s); {
		i := strings.Index(s[offset:], literal)
		if i < 0 {
			break
		}
		matches = append(matches, [2]int{offset + i, offset + i + len(literal)})
		offset += i + len(literal)
	}
	return matches
}

type preTokenizer interface {
	preTokenize(splits []*normalizedString) ([]*normalizedString, error)
}

type sequencePreTokenizer []preTokenizer

func (s sequencePreTokenizer) preTokenize(splits []*normalizedString) ([]*normalizedString, error) {
	var err error
	for _, p := range s {
		if splits, err = p.preTokenize(splits); err != nil {
			return nil, err
		}
	}
	return splits, nil
}

type splitPreTokenizer struct {
	matches  func(string) []patternMatch
	behavior splitBehavior
	invert   bool
}

func (p *splitPreTokenizer) preTokenize(splits []*normalizedString) ([]*normalizedString, error) {
	return splitEach(splits, func(n *normalizedString) ([]*normalizedString, error) {
		matches := p.matches(n.normalized)
		if p.invert {
			matches = invertMatches(matches)
		}
		return n.split(matches, p.behavior)
	})
}

type metaspacePreTokenizer struct {
	replacement rune
	prepend     string // "always", "first" or "never"
	split       bool
}

func (p *metaspacePreTokenizer) preTokenize(splits []*normalizedString) ([]*normalizedString, error) {
	replacement := string(p.replacement)
	return splitEach(splits, func(n *normalizedString) ([]*normalizedString, error) {
		n.replaceRune(' ', replacement)
		startsWith := strings.HasPrefix(n.normalized, replacement)
		switch {
		case p.prepend == "always" && !startsWith, p.prepend == "first" && !startsWith && n.shift == 0:
			if err := n.prepend(replacement); err != nil {
				return nil, err
			}
		}
		if !p.split {
			return []*normalizedString{n}, nil
		}
		return n.split(predicateMatches(n.normalized, func(r rune) bool { return r == p.replacement }), splitMergedWithNext)
	})
}

// splitEach applies a split to every piece and drops empty results, like
// PreTokenizedString::split.
func splitEach(splits []*normalizedString, split func(*normalizedString) ([]*normalizedString, error)) ([]*normalizedString, error) {
	result := make([]*normalizedString, 0, len(splits))
	for _, s := range splits {
		parts, err := split(s)
		if err != nil {
			return nil, err
		}
		for _, part := range parts {
			if part.normalized != "" {
				result = append(result, part)
			}
		}
	}
	return result, nil
}
