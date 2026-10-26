package shape

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Glyphs that may follow a prompt shape before the cursor.
func isTrailingPunct(r rune) bool {
	switch r {
	case ' ', '?', ':', '>', '.', '!', '？', '：', '»', '›':
		return true
	}
	return false
}

// isInputMarker reports whether a cursor row holds only an input marker,
// so the prompt text is on the row above.
func isInputMarker(s string) bool {
	switch strings.TrimSpace(s) {
	case "", ">", "❯", "›", "»", ":", "➜", "→":
		return true
	}
	return false
}

func closerFor(r rune) rune {
	switch r {
	case ']':
		return '['
	case ')':
		return '('
	case '）':
		return '（'
	case '】':
		return '【'
	}
	return 0
}

// trailingGroup returns the inside of the bracketed group that ends s,
// ignoring trailing prompt punctuation and keeping nested brackets inside the
// group, and the text before the group.
func trailingGroup(s string) (inner, prefix string, ok bool) {
	t := strings.TrimRightFunc(s, isTrailingPunct)
	last, size := utf8.DecodeLastRuneInString(t)
	if closerFor(last) == 0 {
		return "", "", false
	}
	end := len(t) - size
	depth := 0
	for i := end; i > 0; {
		r, n := utf8.DecodeLastRuneInString(t[:i])
		i -= n
		switch {
		case closerFor(r) != 0:
			depth++
		case r == '[' || r == '(' || r == '（' || r == '【':
			if depth == 0 {
				return t[i+n : end], t[:i], true
			}
			depth--
		}
	}
	return "", "", false
}

func isYes(t string) bool {
	switch strings.ToLower(t) {
	case "y", "yes", "是":
		return true
	}
	return false
}

func isNo(t string) bool {
	switch strings.ToLower(t) {
	case "n", "no", "否":
		return true
	}
	return false
}

func isAnswerToken(t string) bool {
	n := utf8.RuneCountInString(t)
	if n == 0 || n > 12 {
		return false
	}
	for _, r := range t {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// parseYesNo reads answer tokens from a trailing "[y/N]", "(yes/no)",
// "[y,n,q,?]" style group. Bracketed placeholders and non-word tokens are
// skipped; the group must offer both a yes and a no answer.
func parseYesNo(s string) ([]Option, bool) {
	inner, _, ok := trailingGroup(s)
	if !ok {
		return nil, false
	}
	parts := strings.FieldsFunc(inner, func(r rune) bool { return r == '/' || r == ',' || r == '|' })
	var tokens []string
	yes, no := false, false
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if !isAnswerToken(p) {
			continue
		}
		yes = yes || isYes(p)
		no = no || isNo(p)
		tokens = append(tokens, p)
	}
	if !yes || !no {
		return nil, false
	}
	upper := -1
	for i, t := range tokens {
		if strings.ToLower(t) != t {
			if upper >= 0 {
				upper = -2
				break
			}
			upper = i
		}
	}
	opts := make([]Option, len(tokens))
	for i, t := range tokens {
		opts[i] = Option{Label: t, Keys: strings.ToLower(t), Submit: true, Default: i == upper}
	}
	return opts, true
}

// yesNo finds a yes/no group that ends the text before the cursor, or ends
// the row just above when the cursor row holds nothing but an input marker.
// Nothing may follow the cursor on its row.
func (s screen) yesNo() (Prompt, bool) {
	before, after := s.split()
	if strings.TrimSpace(cleanRow(after)) != "" {
		return Prompt{}, false
	}
	cand := strings.TrimSpace(cleanRow(before))
	if isInputMarker(cand) {
		r := s.prevNonBlank(s.cr, 2)
		if r < 0 {
			return Prompt{}, false
		}
		cand = strings.TrimSpace(cleanRow(s.rows[r]))
	}
	opts, ok := parseYesNo(cand)
	if !ok {
		return Prompt{}, false
	}
	return Prompt{
		Kind:        KindYesNo,
		Question:    cand,
		Options:     opts,
		CursorIndex: -1,
		Fingerprint: fingerprint(KindYesNo, []string{cand}),
	}, true
}
