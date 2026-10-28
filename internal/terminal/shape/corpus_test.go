package shape

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/terminal"
)

// A corpus fixture is a header of "# key: value" lines, a "---" line and the
// screen content. The content is fed to a real terminal.Tab ("\n" becomes
// CRLF) so the detector sees the same Snapshot the session layer produces.
//
// Headers:
//
//	kind:      expected Kind (required)
//	options:   expected option labels separated by " | "
//	keys:      expected option keys separated by " | " (optional)
//	cursor-index: expected Prompt.CursorIndex (default -1)
//	size:      COLSxROWS (default 80x24)
//	alt:       "true" renders in the alternate screen
//	cursor:    ROW,COL zero-based final cursor (default: end of content)
//	known:     the detector is known to disagree with the label; the text
//	           explains why. Such fixtures are counted in the metrics but do
//	           not fail the test, and a fixture without it must match.
type fixture struct {
	name        string
	kind        Kind
	options     []string
	keys        []string
	cursorIndex int
	cols, rows  int
	alt         bool
	cursor      *[2]int
	known       string
	content     string
}

func splitList(v string) []string {
	parts := strings.Split(v, " | ")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func loadFixture(t *testing.T, path string) fixture {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	head, body, ok := strings.Cut(string(raw), "\n---\n")
	if !ok {
		t.Fatalf("%s: missing --- separator", path)
	}
	f := fixture{name: filepath.Base(path), cursorIndex: -1, cols: 80, rows: 24}
	f.content = strings.TrimSuffix(body, "\n")
	for _, line := range strings.Split(head, "\n") {
		key, val, ok := strings.Cut(strings.TrimPrefix(line, "# "), ":")
		if !ok || !strings.HasPrefix(line, "# ") {
			t.Fatalf("%s: bad header line %q", path, line)
		}
		val = strings.TrimSpace(val)
		switch key {
		case "kind":
			f.kind = Kind(val)
		case "options":
			f.options = splitList(val)
		case "keys":
			f.keys = splitList(val)
		case "cursor-index":
			f.cursorIndex, err = strconv.Atoi(val)
		case "size":
			_, err = fmt.Sscanf(val, "%dx%d", &f.cols, &f.rows)
		case "alt":
			f.alt = val == "true"
		case "cursor":
			var c [2]int
			_, err = fmt.Sscanf(val, "%d,%d", &c[0], &c[1])
			f.cursor = &c
		case "known":
			f.known = val
		case "note":
		default:
			t.Fatalf("%s: unknown header %q", path, key)
		}
		if err != nil {
			t.Fatalf("%s: header %q: %v", path, key, err)
		}
	}
	switch f.kind {
	case KindNone, KindYesNo, KindChoice, KindQuestion:
	default:
		t.Fatalf("%s: bad kind %q", path, f.kind)
	}
	return f
}

func render(f fixture) terminal.Snapshot {
	tab := terminal.NewTab("shape", "corpus", f.cols, f.rows, terminal.UTF8, terminal.WithScreenHistory(16))
	defer tab.Close()
	var b strings.Builder
	if f.alt {
		b.WriteString("\x1b[?1049h\x1b[H")
	}
	b.WriteString(strings.ReplaceAll(f.content, "\n", "\r\n"))
	if f.cursor != nil {
		fmt.Fprintf(&b, "\x1b[%d;%dH", f.cursor[0]+1, f.cursor[1]+1)
	}
	tab.Feed([]byte(b.String()))
	return tab.Snapshot()
}

// mismatch describes how a detection differs from the fixture label, or
// returns "" when it matches.
func (f fixture) mismatch(p Prompt) string {
	if p.Kind != f.kind {
		return fmt.Sprintf("kind %q, want %q", p.Kind, f.kind)
	}
	if f.kind == KindNone {
		return ""
	}
	labels := make([]string, len(p.Options))
	keys := make([]string, len(p.Options))
	for i, o := range p.Options {
		labels[i], keys[i] = o.Label, o.Keys
	}
	if f.kind != KindQuestion && !slices.Equal(labels, f.options) {
		return fmt.Sprintf("options %q, want %q", labels, f.options)
	}
	if f.keys != nil && !slices.Equal(keys, f.keys) {
		return fmt.Sprintf("keys %q, want %q", keys, f.keys)
	}
	if p.CursorIndex != f.cursorIndex {
		return fmt.Sprintf("cursor index %d, want %d", p.CursorIndex, f.cursorIndex)
	}
	return ""
}

type tally struct{ total, hit int }

func (t tally) String() string {
	if t.total == 0 {
		return "0/0"
	}
	return fmt.Sprintf("%d/%d (%.1f%%)", t.hit, t.total, 100*float64(t.hit)/float64(t.total))
}

// TestCorpus checks every labelled screen and reports recall over prompt
// fixtures and the false-positive rate over non-prompt fixtures. Fixtures
// that are not marked "known" must match exactly, so the measured numbers
// cannot regress without a fixture label changing in review.
func TestCorpus(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "corpus", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("empty corpus")
	}
	recall := map[Kind]*tally{KindYesNo: {}, KindChoice: {}, KindQuestion: {}}
	var all, fp tally
	for _, path := range paths {
		f := loadFixture(t, path)
		p := Detect(render(f))
		miss := f.mismatch(p)
		if f.kind == KindNone {
			fp.total++
			if miss != "" {
				fp.hit++
			}
		} else {
			all.total++
			recall[f.kind].total++
			if miss == "" {
				all.hit++
				recall[f.kind].hit++
			}
		}
		switch {
		case miss != "" && f.known == "":
			t.Errorf("%s: %s (question %q)", f.name, miss, p.Question)
		case miss == "" && f.known != "":
			t.Logf("%s: now matches its label; drop the known header (%s)", f.name, f.known)
		}
	}
	t.Logf("recall %s: yesno %s, choice %s, question %s", all, recall[KindYesNo], recall[KindChoice], recall[KindQuestion])
	t.Logf("false positives %s", fp)
}
