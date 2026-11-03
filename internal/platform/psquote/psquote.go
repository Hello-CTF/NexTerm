// Package psquote builds PowerShell single-quoted string literals.
package psquote

import "strings"

// PowerShell treats U+2018-U+201B like the ASCII apostrophe, so each of them
// must be doubled or a value could close the literal early.
var doubler = strings.NewReplacer(
	"'", "''", "\u2018", "\u2018\u2018", "\u2019", "\u2019\u2019",
	"\u201A", "\u201A\u201A", "\u201B", "\u201B\u201B",
)

// Quote returns value as a single-quoted PowerShell literal.
func Quote(value string) string {
	return "'" + doubler.Replace(value) + "'"
}

func isQuote(r rune) bool {
	switch r {
	case '\'', '\u2018', '\u2019', '\u201A', '\u201B':
		return true
	}
	return false
}

// Unquote parses text as exactly one single-quoted PowerShell literal and
// returns its value. ok is false when text is not a single literal.
func Unquote(text string) (value string, ok bool) {
	runes := []rune(text)
	if len(runes) < 2 || !isQuote(runes[0]) {
		return "", false
	}
	var out []rune
	for i := 1; i < len(runes); i++ {
		if !isQuote(runes[i]) {
			out = append(out, runes[i])
			continue
		}
		if i+1 < len(runes) && isQuote(runes[i+1]) {
			out = append(out, runes[i+1])
			i++
			continue
		}
		return string(out), i == len(runes)-1
	}
	return "", false
}
