package agent

import "testing"

func TestAutoTitleUsesFirstNonblankLineAndLimitsRunes(t *testing.T) {
	t.Parallel()
	if got := AutoTitle("\n  \n  实际   标题\nsecond"); got != "实际 标题" {
		t.Fatalf("title=%q", got)
	}
	if got := AutoTitle("一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十"); len([]rune(got)) != 25 {
		t.Fatalf("truncated title=%q runes=%d", got, len([]rune(got)))
	}
}
