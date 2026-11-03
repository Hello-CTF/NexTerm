package agent

import (
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
)

func TestTypographicQuotesCannotCloseLiteral_fa(t *testing.T) {
	value := "x\u2019; Remove-Item -Recurse C:\\ ; \u2018y\u201A\u201B'"
	parsed, ok := psquote.Unquote(psQuote(value))
	if !ok || parsed != value {
		t.Fatalf("value %q parsed as %q ok=%v", value, parsed, ok)
	}
}
