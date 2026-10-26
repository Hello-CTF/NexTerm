package shape

import (
	"slices"
	"strings"
	"testing"
)

func endCursor(lines []string) (int, int) {
	r := len(lines) - 1
	return r, displayWidth(lines[r]) + 1
}

func detect(lines ...string) Prompt {
	r, c := endCursor(lines)
	return DetectLines(lines, r, c)
}

func TestYesNoKeysAndDefault(t *testing.T) {
	p := detect("Do you want to continue? [Y/n]")
	if p.Kind != KindYesNo {
		t.Fatalf("kind = %q", p.Kind)
	}
	want := []Option{
		{Label: "Y", Keys: "y", Submit: true, Default: true},
		{Label: "n", Keys: "n", Submit: true},
	}
	if !slices.Equal(p.Options, want) {
		t.Fatalf("options = %+v, want %+v", p.Options, want)
	}
	if p.CursorIndex != -1 {
		t.Fatalf("cursor index = %d", p.CursorIndex)
	}
}

func TestYesNoNeedsCursorAfterShape(t *testing.T) {
	lines := []string{"是否覆盖现有配置？[y/N] 正在等待"}
	// The cursor sits before the trailing text; wide characters count two
	// columns each, so column 24 is right after "[y/N] ".
	if p := DetectLines(lines, 0, 24); p.Kind != KindNone {
		t.Fatalf("cursor before trailing text: kind = %q", p.Kind)
	}
	lines = []string{"是否覆盖现有配置？[y/N]"}
	if p := DetectLines(lines, 0, 24); p.Kind != KindYesNo {
		t.Fatalf("cursor after wide prompt: kind = %q", p.Kind)
	}
}

func TestChoiceSubmitFollowsMarker(t *testing.T) {
	marked := detect(
		"Pick one",
		"❯ 1. Alpha",
		"  2. Beta",
		"",
	)
	if marked.Kind != KindChoice || marked.CursorIndex != 0 {
		t.Fatalf("marked = %+v", marked)
	}
	for _, o := range marked.Options {
		if o.Submit {
			t.Fatalf("marked menu option %q wants Enter after its digit", o.Label)
		}
	}
	plain := detect(
		"1) Alpha",
		"2) Beta",
		"Select:",
	)
	if plain.Kind != KindChoice || plain.CursorIndex != -1 {
		t.Fatalf("plain = %+v", plain)
	}
	for _, o := range plain.Options {
		if !o.Submit || o.Keys == "" {
			t.Fatalf("line-read option %+v", o)
		}
	}
}

func TestChoiceRejectsBrokenNumbering(t *testing.T) {
	for name, lines := range map[string][]string{
		"gap":       {"1) Alpha", "3) Beta", "Select:"},
		"late":      {"4) Alpha", "5) Beta", "Select:"},
		"single":    {"1) Alpha", "Select:"},
		"two marks": {"❯ Alpha", "❯ Beta", ""},
	} {
		if p := detect(lines...); p.Kind == KindChoice {
			t.Errorf("%s: detected %+v", name, p)
		}
	}
}

func TestFingerprint(t *testing.T) {
	menu := []string{
		"⠋ working",
		"",
		"Pick one",
		"❯ 1. Alpha",
		"  2. Beta",
		"",
	}
	base := DetectLines(menu, 5, 0)
	if base.Kind != KindChoice || base.Fingerprint == 0 {
		t.Fatalf("base = %+v", base)
	}

	spinner := slices.Clone(menu)
	spinner[0] = "⠙ working"
	if got := DetectLines(spinner, 5, 0).Fingerprint; got != base.Fingerprint {
		t.Fatal("redrawing a row outside the prompt changed the fingerprint")
	}

	scrolled := append([]string{"earlier output"}, menu...)
	if got := DetectLines(scrolled, 6, 0).Fingerprint; got != base.Fingerprint {
		t.Fatal("moving the prompt down a row changed the fingerprint")
	}

	moved := slices.Clone(menu)
	moved[3], moved[4] = "  1. Alpha", "❯ 2. Beta"
	if got := DetectLines(moved, 5, 0); got.CursorIndex != 1 || got.Fingerprint == base.Fingerprint {
		t.Fatalf("moving the selection: %+v", got)
	}

	relabeled := slices.Clone(menu)
	relabeled[4] = "  2. Gamma"
	if got := DetectLines(relabeled, 5, 0).Fingerprint; got == base.Fingerprint {
		t.Fatal("changing an option kept the fingerprint")
	}
}

func TestDetectDoesNotModifyInput(t *testing.T) {
	lines := []string{"│ Pick │", "│ ❯ 1. A │", "│   2. B │"}
	orig := slices.Clone(lines)
	DetectLines(lines, 1, 2)
	if !slices.Equal(lines, orig) {
		t.Fatalf("lines changed: %q", lines)
	}
}

func BenchmarkDetectLines(b *testing.B) {
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = strings.Repeat("output 12345 ", 15)
	}
	lines[45], lines[46], lines[47], lines[48] = "Pick one", "❯ 1. Alpha", "  2. Beta", "  3. Gamma"
	b.ReportAllocs()
	for b.Loop() {
		DetectLines(lines, 49, 0)
	}
}

func FuzzDetectLines(f *testing.F) {
	f.Add("Continue? [Y/n]\n", 0, 16)
	f.Add("1) a\n2) b\n#? ", 2, 3)
	f.Add("│ ❯ 1. 是\n│   2. 否\n", 1, 4)
	f.Add("é́ (y/n)", 0, 99)
	f.Fuzz(func(t *testing.T, screen string, row, col int) {
		lines := strings.Split(screen, "\n")
		if row > 200 || col > 1000 {
			return
		}
		p := DetectLines(lines, row, col)
		if p.CursorIndex < -1 || p.CursorIndex >= max(len(p.Options), 1) {
			t.Fatalf("cursor index %d with %d options", p.CursorIndex, len(p.Options))
		}
		if p.Kind == KindNone && (p.Fingerprint != 0 || len(p.Options) > 0) {
			t.Fatalf("none result carries data: %+v", p)
		}
	})
}
