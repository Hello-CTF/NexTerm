package docker

import (
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
)

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
		parsed, ok := psquote.Unquote(quoteShell(value, ShellPowerShell))
		if !ok || parsed != value {
			t.Errorf("value %q parsed as %q ok=%v", value, parsed, ok)
		}
	}
}
