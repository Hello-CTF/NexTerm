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

func TestAutoTitleSkipsReferencePrefix(t *testing.T) {
	if got := AutoTitle("[引用]\n- web-01（工作区 的终端）\n\n为什么报错"); got != "为什么报错" {
		t.Fatalf("AutoTitle with references = %q", got)
	}
	if got := AutoTitle("[引用] 是什么"); got != "[引用] 是什么" {
		t.Fatalf("AutoTitle without a prefix list = %q", got)
	}
}
