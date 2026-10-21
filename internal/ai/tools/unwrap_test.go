package tools

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

// Unwrap decides soft-wrapped continuation by terminal cell width, not rune
// count: CJK wide characters fill a row with far fewer runes.

func TestUnwrapLinesUsesCellWidth(t *testing.T) {
	// 41 CJK runes at 80 cols render as 40+1 cells over two screen lines;
	// rune counting would see 40 < 80 and never join them.
	rendered := renderOutputDiff([]byte(strings.Repeat("界", 41)+"\r\n"), 80, 24)
	if len(rendered) != 2 {
		t.Fatalf("rendered lines = %d, want 2: %q", len(rendered), rendered)
	}
	if runewidth.StringWidth(rendered[0]) != 80 || runewidth.StringWidth(rendered[1]) != 2 {
		t.Fatalf("cell widths = %d, %d", runewidth.StringWidth(rendered[0]), runewidth.StringWidth(rendered[1]))
	}
	joined := unwrapLines(rendered, 80)
	if len(joined) != 1 || joined[0] != strings.Repeat("界", 41) {
		t.Fatalf("unwrapped = %q", joined)
	}
}

func TestUnwrapLinesMixedWidth(t *testing.T) {
	// 60 ASCII + 10 CJK = 80 cells exactly: the row is full and continues.
	full := strings.Repeat("a", 60) + strings.Repeat("界", 10)
	joined := unwrapLines([]string{full, "tail"}, 80)
	if len(joined) != 1 || joined[0] != full+"tail" {
		t.Fatalf("joined = %q", joined)
	}
	// 79 cells: not full, stays separate.
	notFull := strings.Repeat("a", 59) + "界"
	joined = unwrapLines([]string{notFull, "tail"}, 80)
	if len(joined) != 2 {
		t.Fatalf("unwrapped = %q", joined)
	}
}
