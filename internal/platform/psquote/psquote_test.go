package psquote

import "testing"

func TestQuoteDoublesEverySingleQuoteLike(t *testing.T) {
	for _, quote := range []string{"'", "\u2018", "\u2019", "\u201A", "\u201B"} {
		got := Quote("a" + quote + "b")
		want := "'a" + quote + quote + "b'"
		if got != want {
			t.Errorf("Quote(%q) = %q, want %q", quote, got, want)
		}
	}
}

func TestQuoteRoundTripsAsOneToken(t *testing.T) {
	values := []string{
		"",
		"plain",
		"it's",
		"x\u2019; Remove-Item -Recurse C:\\ ; \u2018y",
		"\u201A\u201B\u2018\u2019'",
		"\u2019",
	}
	for _, value := range values {
		parsed, ok := Unquote(Quote(value))
		if !ok || parsed != value {
			t.Errorf("value %q parsed as %q ok=%v", value, parsed, ok)
		}
	}
}

func TestUnquoteRejectsEarlyClose(t *testing.T) {
	if _, ok := Unquote("'a\u2019; evil '"); ok {
		t.Fatal("a lone typographic quote must close the string and leave trailing text")
	}
}

func TestUnquoteMixedPairKeepsSecondRune(t *testing.T) {
	got, ok := Unquote("'a\u2019'b'")
	if !ok || got != "a'b" {
		t.Fatalf("got %q ok=%v, want a'b", got, ok)
	}
}
