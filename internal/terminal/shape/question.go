package shape

import (
	"strings"
	"unicode"
)

// questionGap is the most blank columns allowed between the question text
// and the cursor.
const questionGap = 2

func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// asksQuestion accepts text ending in "?" or ":", optionally followed by a
// bracketed default such as "name: (demo)".
func asksQuestion(s string) bool {
	if _, prefix, ok := trailingGroup(s); ok && strings.TrimSpace(prefix) != "" {
		s = strings.TrimSpace(prefix)
	}
	return endsWithAsk(s) && hasLetter(s) && !strings.HasSuffix(s, "::")
}

// question is the lowest-confidence shape: the text right before the cursor
// ends with "?" or ":" (or the row above does, when the cursor row holds an
// input marker), nothing follows the cursor and every row below is blank.
func (s screen) question() (Prompt, bool) {
	before, after := s.split()
	if strings.TrimSpace(cleanRow(after)) != "" || s.nonBlankBelow(s.cr) > 0 {
		return Prompt{}, false
	}
	text := cleanRow(before)
	q := strings.TrimSpace(text)
	if s.cc-displayWidth(strings.TrimRight(before, " ")) > questionGap {
		return Prompt{}, false
	}
	switch {
	case asksQuestion(q):
	case q != "" && isInputMarker(q):
		r := s.prevNonBlank(s.cr, 1)
		if r < 0 {
			return Prompt{}, false
		}
		q = strings.TrimSpace(cleanRow(s.rows[r]))
		if !asksQuestion(q) {
			return Prompt{}, false
		}
	default:
		return Prompt{}, false
	}
	return Prompt{
		Kind:        KindQuestion,
		Question:    q,
		CursorIndex: -1,
		Fingerprint: fingerprint(KindQuestion, []string{q}),
	}, true
}
