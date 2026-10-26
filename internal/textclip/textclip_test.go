package textclip

import (
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	family   = "👨‍👩‍👧‍👦"
	flagJP   = "🇯🇵"
	flagUS   = "🇺🇸"
	thumbs   = "👍🏽"
	keycap   = "1️⃣"
	heart    = "❤️"
	scotland = "🏴\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f"
	accented = "é"
)

func TestRunesKeepsShortText(t *testing.T) {
	for _, s := range []string{"", "build", family, "中文" + heart} {
		if got := Runes(s, 80); got != s {
			t.Fatalf("Runes(%q, 80) = %q, want unchanged", s, got)
		}
	}
	if got := Runes("abc", 0); got != "" {
		t.Fatalf("Runes with zero limit = %q, want empty", got)
	}
}

// Every emoji cluster at the boundary is either kept whole or dropped whole.
func TestRunesNeverSplitsClusters(t *testing.T) {
	clusters := []string{family, flagJP, thumbs, keycap, heart, scotland, accented}
	for _, cluster := range clusters {
		prefix := "中文"
		text := prefix + cluster + "tail"
		size := utf8.RuneCountInString(cluster)
		base := utf8.RuneCountInString(prefix)
		for limit := base; limit < base+size; limit++ {
			got := Runes(text, limit)
			if got != prefix {
				t.Fatalf("Runes(%q, %d) = %q, want the cluster dropped whole (%q)", text, limit, got, prefix)
			}
		}
		if got := Runes(text, base+size); got != prefix+cluster {
			t.Fatalf("Runes(%q, %d) = %q, want %q", text, base+size, got, prefix+cluster)
		}
	}
}

func TestRunesKeepsFlagPairs(t *testing.T) {
	text := flagJP + flagUS + flagJP
	for limit, want := range map[int]string{1: "", 2: flagJP, 3: flagJP, 4: flagJP + flagUS, 5: flagJP + flagUS} {
		if got := Runes(text, limit); got != want {
			t.Fatalf("Runes(flags, %d) = %q, want %q", limit, got, want)
		}
	}
}

func TestRunesOutputIsValidAndBounded(t *testing.T) {
	text := strings.Repeat("中"+family+thumbs+keycap+heart+flagJP, 8)
	for limit := 0; limit <= utf8.RuneCountInString(text); limit++ {
		got := Runes(text, limit)
		if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
			t.Fatalf("Runes(_, %d) = %q, not clean UTF-8", limit, got)
		}
		if utf8.RuneCountInString(got) > limit {
			t.Fatalf("Runes(_, %d) has %d runes", limit, utf8.RuneCountInString(got))
		}
		if !strings.HasPrefix(text, got) {
			t.Fatalf("Runes(_, %d) = %q, not a prefix", limit, got)
		}
		if strings.HasSuffix(got, "‍") {
			t.Fatalf("Runes(_, %d) = %q ends in a dangling ZWJ", limit, got)
		}
	}
}
