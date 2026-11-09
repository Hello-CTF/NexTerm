package termtext

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func render(t *testing.T, cols int, chunks ...string) (string, Result) {
	t.Helper()
	var out bytes.Buffer
	renderer := NewRenderer(&out, Options{})
	renderer.Resize(cols)
	for _, chunk := range chunks {
		if err := renderer.Write(0, []byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	result, err := renderer.Close()
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != int64(out.Len()) {
		t.Fatalf("bytes = %d, written %d", result.Bytes, out.Len())
	}
	return out.String(), result
}

func TestRendererConvertsTerminalOutput(t *testing.T) {
	tests := []struct {
		name   string
		cols   int
		chunks []string
		want   string
	}{
		{name: "colors and styles", chunks: []string{"\x1b[1;31merror\x1b[0m: \x1b[38;2;10;20;30mbad\x1b[m\r\n"}, want: "error: bad\n"},
		{name: "line endings", chunks: []string{"a\r\nb\nc\r\n\r\nd"}, want: "a\nb\nc\n\nd\n"},
		{name: "progress bar with carriage returns", chunks: []string{"  0%\r 50%\r100%\r\n"}, want: "100%\n"},
		{name: "progress bar erasing the line", chunks: []string{"downloading 50%", "\r\x1b[Kdone\r\n"}, want: "done\n"},
		{name: "overwrite keeps the longer tail", chunks: []string{"abcdef\rXY\r\n"}, want: "XYcdef\n"},
		{name: "backspace erase", chunks: []string{"$ lss\b \b\r\n"}, want: "$ ls\n"},
		{name: "backspace overwrite", chunks: []string{"a\bb\r\n"}, want: "b\n"},
		{name: "tabs", chunks: []string{"a\tb\r\n"}, want: "a       b\n"},
		{name: "cursor forward pads", chunks: []string{"a\x1b[3Cb\r\n"}, want: "a   b\n"},
		{name: "cursor back and delete chars", chunks: []string{"$ echoo\x1b[2D\x1b[P\r\n"}, want: "$ echo\n"},
		{name: "insert chars", chunks: []string{"$ eho\x1b[2D\x1b[1@c\r\n"}, want: "$ echo\n"},
		{name: "absolute column", chunks: []string{"abcdef\x1b[3GZ\r\n"}, want: "abZdef\n"},
		{name: "erase chars", chunks: []string{"abcdef\x1b[4G\x1b[2X\r\n"}, want: "abc  f\n"},
		{name: "OSC window title with BEL", chunks: []string{"\x1b]0;user@host: ~\x07$ ls\r\n"}, want: "$ ls\n"},
		{name: "OSC with ST and hyperlink", chunks: []string{"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\ text\r\n"}, want: "link text\n"},
		{name: "OSC 133 shell integration", chunks: []string{"\x1b]133;A\x07$ \x1b]133;B\x07pwd\r\n\x1b]133;C\x07/root\r\n\x1b]133;D;0\x07"}, want: "$ pwd\n/root\n"},
		{name: "DCS and private modes", chunks: []string{"\x1bP1$r0m\x1b\\\x1b[?25l\x1b[?2004hok\x1b[?25h\r\n"}, want: "ok\n"},
		{name: "charset designation", chunks: []string{"\x1b(Bok\x1b(0\r\n"}, want: "ok\n"},
		{name: "CJK text", chunks: []string{"你好，世界\r\n"}, want: "你好，世界\n"},
		{name: "CJK split across chunks", chunks: []string{"\xe4\xbd", "\xa0\xe5", "\xa5\xbd\r\n"}, want: "你好\n"},
		{name: "CJK backspace by cell", chunks: []string{"中文\b\b字\r\n"}, want: "中字\n"},
		{name: "CJK overwrite of half a wide character", chunks: []string{"中文\rab\r\n"}, want: "ab文\n"},
		{name: "combining marks", chunks: []string{"éx\r\n"}, want: "éx\n"},
		{name: "invalid UTF-8", chunks: []string{"a\xffb\r\n"}, want: "a�b\n"},
		{name: "trailing blanks trimmed", chunks: []string{"abc   \x1b[K\r\n"}, want: "abc\n"},
		{name: "escape sequence split across chunks", chunks: []string{"a\x1b", "[3", "1mb\x1b]0;ti", "tle\x07c\r\n"}, want: "abc\n"},
		{name: "C1 CSI as a code point", chunks: []string{"a\u009b31mb\r\n"}, want: "ab\n"},
		{name: "clear screen keeps earlier lines", chunks: []string{"before\r\n\x1b[H\x1b[2J\x1b[3J$ \r\n"}, want: "before\n$\n"},
		{name: "vertical moves start new lines", chunks: []string{"layer1: 10%\r\nlayer2: 10%\r\n\x1b[2A\x1b[2Klayer1: done\r\n"}, want: "layer1: 10%\nlayer2: 10%\nlayer1: done\n"},
		{name: "carriage return on a soft-wrapped row", cols: 4, chunks: []string{"abcdefgh\rXY\r\n"}, want: "abcdXYgh\n"},
		{name: "DEC special graphics", chunks: []string{"\x1b(0lqqk\x1b(B ok\r\n\x1b)0\x0ex\x0f|\r\n"}, want: "┌──┐ ok\n│|\n"},
		{name: "save and restore cursor on the same line", chunks: []string{"abc\x1b7def\x1b8X\r\n", "123\x1b[s456\x1b[uY\r\n"}, want: "abcXef\n123Y56\n"},
		{name: "restore cursor after other lines starts a new line", chunks: []string{"top\x1b7\r\nmid\r\n\x1b8!\r\n"}, want: "top\nmid\n   !\n"},
		{name: "insert chars only shifts the cursor row", cols: 4, chunks: []string{"abcdefgh\r\x1b[2C\x1b[1@\r\n"}, want: "abcdef g\n"},
		{name: "delete chars only shifts the cursor row", cols: 4, chunks: []string{"abcdefgh\r\x1b[P\r\n"}, want: "abcdfgh\n"},
		{name: "column moves stay on the screen row", chunks: []string{"a\x1b[65535Gb\x1b[65535Cc\r\n"}, want: "a" + strings.Repeat(" ", fallbackCols-2) + "c\n"},
		{name: "column moves stay on a known row", cols: 10, chunks: []string{"\x1b[999Gx\x1b[999;999Hy\r\n"}, want: strings.Repeat(" ", 9) + "x\n" + strings.Repeat(" ", 9) + "y\n"},
		{name: "insert chars before a wide character shifts it", chunks: []string{"a中b\r\x1b[C\x1b[1@\r\n"}, want: "a 中b\n"},
		{name: "delete chars before a wide character shifts it", chunks: []string{"ab中c\r\x1b[C\x1b[P\r\n"}, want: "a中c\n"},
		{name: "vertical move from a pending wrap stays on the last column", cols: 4, chunks: []string{"abcd\x1bMx\r\n"}, want: "abcd\n   x\n"},
		{name: "unterminated last line", chunks: []string{"$ "}, want: "$\n"},
		{name: "empty", chunks: nil, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, _ := render(t, test.cols, test.chunks...)
			if got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestRendererLeavesOutFullScreenApplications(t *testing.T) {
	vim := "\x1b[?1049h\x1b[22;0;0t\x1b[H\x1b[2J~\r\n~\r\n\x1b[1;1Hhello from vim\x1b[24;1H\"notes.txt\" 1L, 15B"
	got, result := render(t, 80,
		"$ vim notes.txt\r\n",
		vim,
		"\x1b[?1049l\x1b[23;0;0t",
		"$ less log\r\n\x1b[?47hpage\r\n\x1b[?47l$ exit\r\n",
	)
	want := "$ vim notes.txt\n$ less log\n$ exit\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if result.FullScreenSegments != 2 {
		t.Fatalf("segments = %d", result.FullScreenSegments)
	}
	if result.Lines != 3 {
		t.Fatalf("lines = %d", result.Lines)
	}
}

func TestRendererTimestampsLines(t *testing.T) {
	var out bytes.Buffer
	zone := FixedOffset(8 * 60)
	renderer := NewRenderer(&out, Options{Timestamps: true, Location: zone})
	base := time.Date(2026, 11, 9, 10, 0, 0, 0, zone).UnixMilli()
	steps := []struct {
		ts   int64
		data string
	}{
		{base, "first "},
		{base + 2000, "line\r\n"},
		{base + 61_000, "\r\n"},
		{base + 3_600_000, "\x1b[31m"},
		{base + 3_601_000, "last"},
	}
	for _, step := range steps {
		if err := renderer.Write(step.ts, []byte(step.data)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := renderer.Close(); err != nil {
		t.Fatal(err)
	}
	want := "[2026-11-09 10:00:00] first line\n[2026-11-09 10:01:01]\n[2026-11-09 11:00:01] last\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestRendererBoundsLongLines(t *testing.T) {
	got, result := render(t, 0, strings.Repeat("x", maxLineCells+10))
	if result.Lines != 2 {
		t.Fatalf("lines = %d", result.Lines)
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines[0]) != maxLineCells || len(lines[1]) != 10 {
		t.Fatalf("line lengths = %d, %d", len(lines[0]), len(lines[1]))
	}
}

func TestRendererIgnoresOversizedSequences(t *testing.T) {
	got, _ := render(t, 0, "a\x1b["+strings.Repeat("1;", 1000)+"mb\x1b]0;"+strings.Repeat("t", 1<<20)+"\x07c\r\n")
	if got != "abc\n" {
		t.Fatalf("got %q", got)
	}
}

func TestFixedOffsetNames(t *testing.T) {
	for minutes, want := range map[int]string{0: "UTC+0", 480: "UTC+8", -330: "UTC-5:30"} {
		if name, _ := time.Unix(0, 0).In(FixedOffset(minutes)).Zone(); name != want {
			t.Fatalf("FixedOffset(%d) = %s, want %s", minutes, name, want)
		}
	}
}

// BenchmarkRendererRowEdits measures character inserts and deletes on a line near the cell cap. They shift only the cursor's
// screen row, so their cost follows the screen width rather than the line length.
func BenchmarkRendererRowEdits(b *testing.B) {
	edits := []byte(strings.Repeat("\x1b[1@\x1b[P", 4096))
	long := []byte(strings.Repeat("x", maxLineCells-1) + "\r")
	b.SetBytes(int64(len(edits)))
	for b.Loop() {
		renderer := NewRenderer(io.Discard, Options{})
		renderer.Resize(maxCols)
		if err := renderer.Write(0, long); err != nil {
			b.Fatal(err)
		}
		if err := renderer.Write(0, edits); err != nil {
			b.Fatal(err)
		}
	}
}
