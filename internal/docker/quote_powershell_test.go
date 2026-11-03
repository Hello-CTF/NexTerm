package docker

import (
	"strings"
	"testing"
)

func isPowerShellQuote(r rune) bool {
	switch r {
	case '\'', '\u2018', '\u2019', '\u201A', '\u201B':
		return true
	}
	return false
}

// parsePowerShellSingleQuoted parses text as exactly one single-quoted
// PowerShell string and returns its value.
func parsePowerShellSingleQuoted(text string) (string, bool) {
	runes := []rune(text)
	if len(runes) < 2 || !isPowerShellQuote(runes[0]) {
		return "", false
	}
	var out []rune
	for i := 1; i < len(runes); i++ {
		if !isPowerShellQuote(runes[i]) {
			out = append(out, runes[i])
			continue
		}
		if i+1 < len(runes) && isPowerShellQuote(runes[i+1]) {
			out = append(out, runes[i])
			i++
			continue
		}
		return string(out), i == len(runes)-1
	}
	return "", false
}

func TestQuoteShellPowerShellDoublesEverySingleQuoteLike(t *testing.T) {
	for _, quote := range []string{"'", "\u2018", "\u2019", "\u201A", "\u201B"} {
		got := quoteShell("a"+quote+"b", ShellPowerShell)
		want := "'a" + quote + quote + "b'"
		if got != want {
			t.Errorf("quoteShell(%q) = %q, want %q", quote, got, want)
		}
	}
}

func TestQuoteShellPowerShellRoundTrips(t *testing.T) {
	values := []string{
		"plain",
		"it's",
		"x\u2019; Remove-Item -Recurse C:\\ ; \u2018y",
		"\u201A\u201B\u2018\u2019'",
		"\u2019",
	}
	for _, value := range values {
		parsed, ok := parsePowerShellSingleQuoted(quoteShell(value, ShellPowerShell))
		if !ok || parsed != value {
			t.Errorf("value %q parsed as %q ok=%v", value, parsed, ok)
		}
		if strings.Count(quoteShell(value, ShellPowerShell), "\n") != 0 {
			t.Errorf("unexpected newline")
		}
	}
}
