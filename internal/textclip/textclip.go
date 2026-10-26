// Package textclip shortens user-visible text without breaking characters.
package textclip

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const zwj = '\u200d'

// extendsPrevious reports code points that attach to the preceding character
// instead of starting a new user-perceived character.
func extendsPrevious(r rune) bool {
	switch {
	case r == zwj,
		r >= 0xfe00 && r <= 0xfe0f,   // variation selectors
		r >= 0xe0100 && r <= 0xe01ef, // variation selectors supplement
		r >= 0x1f3fb && r <= 0x1f3ff, // emoji skin-tone modifiers
		r >= 0xe0020 && r <= 0xe007f: // emoji tag sequences
		return true
	}
	return unicode.Is(unicode.M, r) // combining marks, including the keycap U+20E3
}

func regionalIndicator(r rune) bool {
	return r >= 0x1f1e6 && r <= 0x1f1ff
}

// Runes returns s cut to at most max runes on a rune boundary. The cut never
// separates a base character from following combining marks, variation
// selectors, skin-tone modifiers, emoji tags or ZWJ-joined parts, and never
// splits a regional-indicator flag pair; a cluster that does not fit is dropped
// whole. Each run of invalid UTF-8 bytes becomes one U+FFFD, so the result is
// always valid UTF-8.
func Runes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	s = strings.ToValidUTF8(s, "\ufffd")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	cut := max
	for cut > 0 {
		next, last := runes[cut], runes[cut-1]
		if extendsPrevious(next) || last == zwj {
			cut--
			continue
		}
		if regionalIndicator(next) && regionalIndicator(last) {
			// An odd run of indicators before the cut ends in half a flag.
			run := 0
			for i := cut - 1; i >= 0 && regionalIndicator(runes[i]); i-- {
				run++
			}
			if run%2 == 1 {
				cut--
				continue
			}
		}
		break
	}
	return string(runes[:cut])
}
