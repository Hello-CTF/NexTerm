package terminal

import (
	"bytes"
	"strings"
	"testing"
)

func screenRow(t *testing.T, tab *Tab, row int) string {
	t.Helper()
	lines := strings.Split(tab.ScreenText(), "\n")
	if row >= len(lines) {
		t.Fatalf("row %d out of range: %q", row, lines)
	}
	return lines[row]
}

func TestWideBaseOverwriteReviewExample(t *testing.T) {
	tab := NewTab("t", "s", 20, 3, UTF8)
	t.Cleanup(tab.Close)
	tab.Feed([]byte("中"))
	tab.Feed([]byte("\r"))
	tab.Feed([]byte("x"))
	tab.Feed([]byte("\x1b[3C"))
	tab.Feed([]byte("Y"))
	if got := screenRow(t, tab, 0); got != "x   Y" {
		t.Fatalf("row = %q, want %q", got, "x   Y")
	}
	if c := tab.Snapshot().CursorCol; c != 5 {
		t.Fatalf("cursor col = %d, want 5", c)
	}
}

func TestWideContinuationOverwrite(t *testing.T) {
	tab := NewTab("t", "s", 20, 3, UTF8)
	t.Cleanup(tab.Close)
	tab.Feed([]byte("中"))
	tab.Feed([]byte("\x1b[2G"))
	tab.Feed([]byte("x"))
	if got := screenRow(t, tab, 0); got != " x" {
		t.Fatalf("row = %q, want %q (no phantom base)", got, " x")
	}
}

func TestWidePairCellEditing(t *testing.T) {
	cases := []struct {
		name  string
		feeds []string
		want  string
	}{
		{"DCH keeps pair intact", []string{"a中b", "\x1b[1G\x1b[P"}, "中b"},
		{"DCH through wide base", []string{"x中y", "\x1b[2G\x1b[P"}, "x y"},
		{"DCH through continuation", []string{"x中y", "\x1b[3G\x1b[P"}, "x y"},
		{"ICH inserts before pair", []string{"x中y", "\x1b[2G\x1b[2@"}, "x  中y"},
		{"ICH splits pair normalized", []string{"x中y", "\x1b[3G\x1b[@"}, "x   y"},
		{"ECH on wide base", []string{"x中y", "\x1b[2G\x1b[X"}, "x  y"},
		{"EL0 on wide base", []string{"a中b", "\x1b[3G\x1b[K"}, "a"},
		{"EL1 through pair", []string{"a中b", "\x1b[2G\x1b[1K"}, "   b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tab := NewTab("t", "s", 20, 3, UTF8)
			t.Cleanup(tab.Close)
			for _, f := range tc.feeds {
				tab.Feed([]byte(f))
			}
			if got := screenRow(t, tab, 0); got != tc.want {
				t.Fatalf("row = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRowStringOrphanContStaysAligned(t *testing.T) {
	r := newRow(4)
	r.cells[0].r = 'x'
	r.cells[1].cont = true
	r.cells[2].r = 'Y'
	if got := r.String(); got != "x Y" {
		t.Fatalf("row = %q, want %q", got, "x Y")
	}
}

func TestScrollHugeCountBounded(t *testing.T) {
	tab := NewTab("t", "s", 20, 24, UTF8)
	t.Cleanup(tab.Close)
	tab.Feed([]byte("a\r\nb\r\nc\r\n"))
	tab.Feed([]byte("\x1b[1000000000S"))
	if got := tab.ScrollbackLen(); got != 24 {
		t.Fatalf("scrollback = %d lines, want 24 (region height, not 1e9)", got)
	}
	hist := tab.ScreenHistory(24)
	if len(hist) != 24 || hist[0] != "a" || hist[1] != "b" || hist[2] != "c" {
		t.Fatalf("history head = %q", hist[:3])
	}
	if got := tab.ScreenHistory(100000); len(got) != 24 {
		t.Fatalf("history = %d lines", len(got))
	}

	tab.Feed([]byte("\x1b[1000000000T"))
	if got := tab.ScrollbackLen(); got != 24 {
		t.Fatalf("scrollback after SD = %d", got)
	}
	if text := tab.ScreenText(); strings.TrimSpace(text) != "" {
		t.Fatalf("screen not blank after huge SD: %q", text)
	}
}

func TestScrollHugeCountInsideRegion(t *testing.T) {
	tab := NewTab("t", "s", 20, 8, UTF8)
	t.Cleanup(tab.Close)
	tab.Feed([]byte("top\r\n"))
	tab.Feed([]byte("\x1b[2;5r"))
	tab.Feed([]byte("\x1b[1000000000S"))
	if got := screenRow(t, tab, 0); got != "top" {
		t.Fatalf("row0 = %q", got)
	}
	if got := tab.ScrollbackLen(); got != 0 {
		t.Fatalf("region scroll must not create history: %d", got)
	}
	tab.Feed([]byte("\x1b[r"))
}

func TestCombiningCapBoundedMemory(t *testing.T) {
	tab := NewTab("t", "s", 20, 3, UTF8)
	t.Cleanup(tab.Close)
	const marks = 10000
	var input bytes.Buffer
	input.WriteString("e")
	for i := 0; i < marks; i++ {
		input.WriteString("́")
	}
	raw := input.Bytes()
	for off := 0; off < len(raw); off += 97 {
		end := min(off+97, len(raw))
		tab.Feed(raw[off:end])
	}
	cell := tab.scr.grid.rows[0].cells[0]
	if len(cell.comb) != maxCombiningRunes {
		t.Fatalf("comb len = %d, want cap %d", len(cell.comb), maxCombiningRunes)
	}
	extra := []byte("́́́")
	tab.Feed(extra)
	if len(cell.comb) != maxCombiningRunes {
		t.Fatalf("comb grew past cap: %d", len(cell.comb))
	}
	wantRaw := append(bytes.Clone(raw), extra...)
	if got := tab.Dump(0); !bytes.Equal(got, wantRaw) {
		t.Fatalf("raw dump mismatch: got %d bytes want %d", len(got), len(wantRaw))
	}
	replay, start := tab.ReplayFrom(0, 0)
	if start != 0 || !bytes.Equal(replay, wantRaw) {
		t.Fatal("replay not lossless")
	}
	want := "e" + strings.Repeat("́", maxCombiningRunes)
	if got := screenRow(t, tab, 0); got != want {
		t.Fatalf("row = %q, want %q", got, want)
	}
}
