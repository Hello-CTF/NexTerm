package supervisor

import (
	"bytes"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func preludeOf(data string, rows int) string {
	modes := terminalModes{Rows: rows}
	modes.feed([]byte(data))
	return string(modes.prelude())
}

func TestModesScrollRegionRules(t *testing.T) {
	if got := preludeOf("\x1b[?1049h\x1b[1;24r", 24); strings.Contains(got, "r") {
		t.Fatalf("a region covering all rows is the full screen, prelude %q", got)
	}
	if got := preludeOf("\x1b[?1049h\x1b[2;20r", 24); !strings.Contains(got, "\x1b[2;20r") {
		t.Fatalf("alternate-screen region lost: %q", got)
	}
	// A program that leaves the alternate screen without resetting its
	// region must not confine later main-screen output.
	if got := preludeOf("\x1b[?1049h\x1b[2;20r\x1b[?1049l", 24); got != "" {
		t.Fatalf("main-screen prelude after a program exited = %q", got)
	}
	modes := terminalModes{Rows: 24}
	modes.feed([]byte("\x1b[?1049h\x1b[2;20r"))
	modes.resize(40)
	if got := string(modes.prelude()); got != "\x1b[?1049h" {
		t.Fatalf("prelude after a resize = %q, want the region reset", got)
	}
}

func TestModesMouseFamiliesAreExclusive(t *testing.T) {
	if got := preludeOf("\x1b[?1003h\x1b[?1000h", 24); !strings.Contains(got, "\x1b[?1000h") || strings.Contains(got, "1003") {
		t.Fatalf("1003 then 1000 = %q, want 1000 only", got)
	}
	if got := preludeOf("\x1b[?1002h\x1b[?1000l", 24); got != "" {
		t.Fatalf("resetting any tracking mode turns tracking off, prelude %q", got)
	}
	if got := preludeOf("\x1b[?1000h\x1b[?1006h\x1b[?1015h\x1b[?1004h", 24); !strings.Contains(got, "\x1b[?1015h") || strings.Contains(got, "1006") || !strings.Contains(got, "\x1b[?1004h") {
		t.Fatalf("encoding and focus = %q", got)
	}
}

func TestModesSoftResetMatchesXterm(t *testing.T) {
	if got := preludeOf("\x1b[?2004h\x1b[?7l\x1b[?1h\x1b[!p", 24); got != "" {
		t.Fatalf("prelude after DECSTR = %q", got)
	}
}

// A resize recorded inside a segment resets the region for every offset
// after it, also after a reload.
func TestHistoryResizeResetsScrollRegion(t *testing.T) {
	dir := t.TempDir()
	recorded, err := createHistoryRows(dir, MinHistoryLimit, 24)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorded.append([]byte("\x1b[?1049h\x1b[2;20rframe")); err != nil {
		t.Fatal(err)
	}
	_, before := recorded.bounds()
	recorded.noteResize(40)
	if err := recorded.append([]byte("redraw")); err != nil {
		t.Fatal(err)
	}
	_, after := recorded.bounds()
	check := func(t *testing.T, recorded *history) {
		t.Helper()
		if prelude, err := recorded.modesPreludeAt(before - 1); err != nil || !bytes.Contains(prelude, []byte("\x1b[2;20r")) {
			t.Fatalf("prelude before the resize = %q, %v", prelude, err)
		}
		if prelude, err := recorded.modesPreludeAt(after); err != nil || string(prelude) != "\x1b[?1049h" {
			t.Fatalf("prelude after the resize = %q, %v", prelude, err)
		}
	}
	check(t, recorded)
	if err := recorded.closeWriter(); err != nil {
		t.Fatal(err)
	}
	check(t, loadHistory(dir, MinHistoryLimit))
}

func TestHistoryLoadSweepsOrphanSnapshots(t *testing.T) {
	recorded := newTestHistory(t, MinHistoryLimit)
	if err := recorded.append([]byte("output")); err != nil {
		t.Fatal(err)
	}
	if err := recorded.closeWriter(); err != nil {
		t.Fatal(err)
	}
	orphans := []string{historyModesPath(recorded.dir, 999), historyModesPath(recorded.dir, 6) + ".tmp", historyResizesPath(recorded.dir, 999)}
	for _, path := range orphans {
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := loadHistory(recorded.dir, MinHistoryLimit).usable(); err != nil {
		t.Fatal(err)
	}
	for _, path := range orphans {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("orphan %s survived the load: %v", filepath.Base(path), err)
		}
	}
}

func TestHistoryRollFailureRemovesSnapshot(t *testing.T) {
	recorded := newTestHistory(t, MinHistoryLimit)
	if err := recorded.append(make([]byte, recorded.segSize)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(historySegmentPath(recorded.dir, recorded.segSize), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := recorded.append([]byte("x")); err == nil {
		t.Fatal("roll into an occupied segment name succeeded")
	}
	if _, err := os.Lstat(historyModesPath(recorded.dir, recorded.segSize)); !os.IsNotExist(err) {
		t.Fatalf("snapshot of the failed segment remains: %v", err)
	}
}

var modeTokens = []string{
	"\x1b[?1049h", "\x1b[?1049l", "\x1b[?1h", "\x1b[?1l", "\x1b=", "\x1b>", "\x1b[?2004h", "\x1b[?2004l",
	"\x1b[?25l", "\x1b[?25h", "\x1b[?7l", "\x1b[?7h", "\x1b[?6h", "\x1b[?6l", "\x1b[?1000;1006h", "\x1b[?1003h",
	"\x1b[?1002l", "\x1b[?1015h", "\x1b[?1004h", "\x1b[3;20r", "\x1b[1;24r", "\x1b[r", "\x1b(0", "\x1b(B",
	"\x1b)0", "\x0e", "\x0f", "\x1b[!p", "\x1bc", "\x1b]0;title\x07", "\x1bP1$r\x1b\\", "\x1b[31m",
	"plain text ", "文字", "\r\n", "\x1b[", "\x1b", "\x1b[?",
}

// Mode snapshots at segment starts plus a bounded parse reproduce exactly the
// modes of parsing every byte from the start, wherever segments split the
// stream (also inside a sequence) and whatever resizes happened.
func TestHistoryModesMatchFullParse(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		random := rand.New(rand.NewPCG(seed, seed*7919))
		dir := t.TempDir()
		recorded, err := createHistoryRows(dir, MaxHistoryLimit, 24)
		if err != nil {
			t.Fatal(err)
		}
		recorded.segSize = uint64(16 + random.IntN(200))
		var stream []byte
		type resizeAt struct {
			offset uint64
			rows   int
		}
		var resizes []resizeAt
		for len(stream) < 20000 {
			if random.IntN(40) == 0 {
				rows := 10 + random.IntN(60)
				recorded.noteResize(uint32(rows))
				resizes = append(resizes, resizeAt{uint64(len(stream)), rows})
				continue
			}
			var chunk []byte
			for range 1 + random.IntN(6) {
				chunk = append(chunk, modeTokens[random.IntN(len(modeTokens))]...)
			}
			if err := recorded.append(chunk); err != nil {
				t.Fatal(err)
			}
			stream = append(stream, chunk...)
		}
		reference := func(offset uint64) terminalModes {
			modes := terminalModes{Rows: 24}
			pending := resizes
			for position := uint64(0); ; position++ {
				for len(pending) > 0 && pending[0].offset == position {
					modes.resize(pending[0].rows)
					pending = pending[1:]
				}
				if position >= offset {
					return modes
				}
				modes.advance(stream[position])
			}
		}
		check := func(recorded *history) {
			for range 200 {
				offset := uint64(random.IntN(len(stream) + 1))
				got, scanned, err := recorded.modesAt(offset)
				if err != nil {
					t.Fatalf("seed %d: modesAt(%d): %v", seed, offset, err)
				}
				// A segment holds at most segSize bytes plus one appended chunk.
				if scanned > recorded.segSize+256 {
					t.Fatalf("seed %d: parsed %d bytes, segment size %d", seed, scanned, recorded.segSize)
				}
				if want := reference(offset); got != want {
					t.Fatalf("seed %d: modes at %d = %+v, want %+v", seed, offset, got, want)
				}
			}
		}
		check(recorded)
		if err := recorded.closeWriter(); err != nil {
			t.Fatal(err)
		}
		reloaded := loadHistory(dir, MaxHistoryLimit)
		reloaded.segSize = recorded.segSize
		check(reloaded)
	}
}
