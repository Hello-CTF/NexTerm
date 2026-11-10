package terminal

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// unicodeWidthFixture is shared with src/test/features/unicodeWidth.test.ts, which checks xterm.js against the
// same widths, so the screen model and the frontend renderer cannot drift apart.
type unicodeWidthFixture struct {
	Samples []struct {
		Name  string `json:"name"`
		Text  string `json:"text"`
		Cells int    `json:"cells"`
	} `json:"samples"`
	Screen struct {
		Cols      int      `json:"cols"`
		Rows      int      `json:"rows"`
		Input     string   `json:"input"`
		Lines     []string `json:"lines"`
		CursorRow int      `json:"cursorRow"`
		CursorCol int      `json:"cursorCol"`
	} `json:"screen"`
	// Ranges lists every printable code point whose width is not 1 as [first, last, width].
	Ranges [][3]int `json:"ranges"`
}

var unicodeWidthFixturePath = filepath.Join("testdata", "unicode_widths.json")

func readUnicodeWidthFixture(t *testing.T) unicodeWidthFixture {
	t.Helper()
	raw, err := os.ReadFile(unicodeWidthFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var fixture unicodeWidthFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// writeUnicodeWidthFixture keeps one sample or range per line so a table change reads as a small diff.
func writeUnicodeWidthFixture(t *testing.T, fixture unicodeWidthFixture) {
	t.Helper()
	marshal := func(v any) string {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSuffix(buf.String(), "\n")
	}
	list := func(b *strings.Builder, n int, item func(int) string) {
		for i := range n {
			b.WriteString("    " + item(i))
			if i < n-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
	}
	var b strings.Builder
	b.WriteString("{\n  \"samples\": [\n")
	list(&b, len(fixture.Samples), func(i int) string { return marshal(fixture.Samples[i]) })
	b.WriteString("  ],\n  \"screen\": " + marshal(fixture.Screen) + ",\n  \"ranges\": [\n")
	list(&b, len(fixture.Ranges), func(i int) string { return marshal(fixture.Ranges[i]) })
	b.WriteString("  ]\n}\n")
	if err := os.WriteFile(unicodeWidthFixturePath, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func nonDefaultWidthRanges() [][3]int {
	var ranges [][3]int
	for r := rune(0xA0); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		w := RuneWidth(r)
		if w == 1 {
			continue
		}
		if n := len(ranges); n > 0 && ranges[n-1][1] == int(r)-1 && ranges[n-1][2] == w {
			ranges[n-1][1] = int(r)
			continue
		}
		ranges = append(ranges, [3]int{int(r), int(r), w})
	}
	return ranges
}

func TestUnicodeWidthTableMatchesFixture(t *testing.T) {
	fixture := readUnicodeWidthFixture(t)
	got := nonDefaultWidthRanges()
	if *updateGolden {
		fixture.Ranges = got
		writeUnicodeWidthFixture(t, fixture)
	}
	if !reflect.DeepEqual(got, fixture.Ranges) {
		t.Fatalf("width table differs from %s (review and rerun with -update if intentional)", unicodeWidthFixturePath)
	}
	for r := rune(0x20); r < 0x7F; r++ {
		if RuneWidth(r) != 1 {
			t.Fatalf("RuneWidth(%q) = %d", r, RuneWidth(r))
		}
	}
}

func TestUnicodeWidthSamplesAdvanceCursor(t *testing.T) {
	for _, sample := range readUnicodeWidthFixture(t).Samples {
		t.Run(sample.Name, func(t *testing.T) {
			if got := StringWidth(sample.Text); got != sample.Cells {
				t.Fatalf("StringWidth(%q) = %d, want %d", sample.Text, got, sample.Cells)
			}
			tab := NewTab("t", "s", 80, 4, UTF8)
			t.Cleanup(tab.Close)
			tab.Feed([]byte(sample.Text))
			snap := tab.Snapshot()
			if snap.CursorRow != 0 || snap.CursorCol != sample.Cells {
				t.Fatalf("cursor after %q = (%d,%d), want (0,%d)", sample.Text, snap.CursorRow, snap.CursorCol, sample.Cells)
			}
		})
	}
}

func TestUnicodeWidthScreenSnapshot(t *testing.T) {
	fixture := readUnicodeWidthFixture(t)
	sc := fixture.Screen
	tab := NewTab("t", "s", sc.Cols, sc.Rows, UTF8)
	t.Cleanup(tab.Close)
	tab.Feed([]byte(sc.Input))
	snap := tab.Snapshot()
	if !reflect.DeepEqual(snap.Lines, sc.Lines) || snap.CursorRow != sc.CursorRow || snap.CursorCol != sc.CursorCol {
		t.Fatalf("snapshot lines=%q cursor=(%d,%d), fixture lines=%q cursor=(%d,%d)",
			snap.Lines, snap.CursorRow, snap.CursorCol, sc.Lines, sc.CursorRow, sc.CursorCol)
	}
}
