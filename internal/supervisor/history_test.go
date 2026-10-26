package supervisor

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// patterned returns n bytes whose value encodes their absolute offset, so a
// read at any offset can be checked without keeping the whole stream.
func patterned(offset uint64, n int) []byte {
	data := make([]byte, n)
	for index := range data {
		data[index] = byte((offset + uint64(index)) % 251)
	}
	return data
}

func newTestHistory(t *testing.T, limit uint64) *history {
	t.Helper()
	dir := t.TempDir()
	recorded, err := createHistory(dir, limit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorded.closeWriter() })
	return recorded
}

func segmentBytesOnDisk(t *testing.T, dir string) uint64 {
	t.Helper()
	dirents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var total uint64
	for _, dirent := range dirents {
		if !strings.HasPrefix(dirent.Name(), historySegmentPrefix) {
			continue
		}
		info, err := dirent.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += uint64(info.Size())
	}
	return total
}

func readAll(t *testing.T, recorded *history, offset uint64) []byte {
	t.Helper()
	_, next := recorded.bounds()
	data := make([]byte, next-offset)
	count, ok, err := recorded.readAt(offset, data)
	if err != nil || !ok {
		t.Fatalf("readAt(%d) = %d, %v, %v", offset, count, ok, err)
	}
	return data[:count]
}

func TestHistoryEvictsOldestWithinLimit(t *testing.T) {
	recorded := newTestHistory(t, MinHistoryLimit)
	var offset, lastBase uint64
	for offset < 5*MinHistoryLimit {
		chunk := patterned(offset, 4096+int(offset%3000))
		if err := recorded.append(chunk); err != nil {
			t.Fatal(err)
		}
		offset += uint64(len(chunk))
		base, next := recorded.bounds()
		if next != offset {
			t.Fatalf("next = %d after recording %d bytes", next, offset)
		}
		if base < lastBase {
			t.Fatalf("history base moved backwards from %d to %d", lastBase, base)
		}
		lastBase = base
		if next-base > MinHistoryLimit {
			t.Fatalf("retained %d bytes, limit %d", next-base, MinHistoryLimit)
		}
		if disk := segmentBytesOnDisk(t, recorded.dir); disk != next-base {
			t.Fatalf("segments on disk hold %d bytes, history reports %d", disk, next-base)
		}
	}
	base, next := recorded.bounds()
	if base == 0 {
		t.Fatal("history never dropped its oldest output")
	}
	if next-base < MinHistoryLimit-recorded.segSize {
		t.Fatalf("retained %d bytes, want at least the limit minus one segment", next-base)
	}
	if got := readAll(t, recorded, base); !bytes.Equal(got, patterned(base, int(next-base))) {
		t.Fatal("retained output does not keep its absolute offsets")
	}
	if _, ok, err := recorded.readAt(base-1, make([]byte, 1)); ok || err != nil {
		t.Fatalf("read below the base = %v, %v; want not retained", ok, err)
	}
}

func TestHistoryReloadKeepsBaseAndNext(t *testing.T) {
	recorded := newTestHistory(t, MinHistoryLimit)
	var offset uint64
	for offset < 3*MinHistoryLimit+12345 {
		chunk := patterned(offset, 7777)
		if err := recorded.append(chunk); err != nil {
			t.Fatal(err)
		}
		offset += uint64(len(chunk))
	}
	if err := recorded.closeWriter(); err != nil {
		t.Fatal(err)
	}
	base, next := recorded.bounds()
	reloaded := loadHistory(recorded.dir, MinHistoryLimit)
	if err := reloaded.usable(); err != nil {
		t.Fatal(err)
	}
	if gotBase, gotNext := reloaded.bounds(); gotBase != base || gotNext != next {
		t.Fatalf("reloaded bounds = %d..%d, want %d..%d", gotBase, gotNext, base, next)
	}
	if got := readAll(t, reloaded, base); !bytes.Equal(got, patterned(base, int(next-base))) {
		t.Fatal("reloaded output differs")
	}
}

// A crash can cut the last write short; the bytes that reached the segment
// are the history and the next offset follows them.
func TestHistoryReloadAcceptsPartialLastSegment(t *testing.T) {
	recorded := newTestHistory(t, MinHistoryLimit)
	if err := recorded.append(patterned(0, 1000)); err != nil {
		t.Fatal(err)
	}
	if err := recorded.closeWriter(); err != nil {
		t.Fatal(err)
	}
	last := recorded.segments[len(recorded.segments)-1].path
	file, err := os.OpenFile(last, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(patterned(1000, 10)); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	reloaded := loadHistory(recorded.dir, MinHistoryLimit)
	if _, next := reloaded.bounds(); next != 1010 {
		t.Fatalf("next = %d, want 1010", next)
	}
}

func TestHistoryReloadQuarantinesInconsistentSegments(t *testing.T) {
	build := func(t *testing.T) *history {
		recorded := newTestHistory(t, MinHistoryLimit)
		var offset uint64
		for offset < 3*recorded.segSize {
			chunk := patterned(offset, 4096)
			if err := recorded.append(chunk); err != nil {
				t.Fatal(err)
			}
			offset += uint64(len(chunk))
		}
		if err := recorded.closeWriter(); err != nil {
			t.Fatal(err)
		}
		if len(recorded.segments) < 3 {
			t.Fatalf("want at least three segments, got %d", len(recorded.segments))
		}
		return recorded
	}
	cases := map[string]func(t *testing.T, recorded *history){
		"missing middle segment": func(t *testing.T, recorded *history) {
			if err := os.Remove(recorded.segments[1].path); err != nil {
				t.Fatal(err)
			}
		},
		"short middle segment": func(t *testing.T, recorded *history) {
			if err := os.Truncate(recorded.segments[1].path, 10); err != nil {
				t.Fatal(err)
			}
		},
		"legacy recording beside segments": func(t *testing.T, recorded *history) {
			if err := os.WriteFile(filepath.Join(recorded.dir, recordingName), []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"malformed segment name": func(t *testing.T, recorded *history) {
			if err := os.WriteFile(filepath.Join(recorded.dir, historySegmentPrefix+"12.raw"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"no segments": func(t *testing.T, recorded *history) {
			for _, segment := range recorded.segments {
				if err := os.Remove(segment.path); err != nil {
					t.Fatal(err)
				}
			}
		},
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			recorded := build(t)
			corrupt(t, recorded)
			reloaded := loadHistory(recorded.dir, MinHistoryLimit)
			if err := reloaded.usable(); !errors.Is(err, ErrHistoryQuarantined) {
				t.Fatalf("usable = %v, want ErrHistoryQuarantined", err)
			}
			if _, err := reloaded.newReader(0).read(make([]byte, 16)); !errors.Is(err, ErrHistoryQuarantined) {
				t.Fatalf("read of a quarantined history = %v", err)
			}
			dirents, err := os.ReadDir(recorded.dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, dirent := range dirents {
				if strings.HasPrefix(dirent.Name(), historySegmentPrefix) || dirent.Name() == recordingName {
					t.Fatalf("%s was left in place instead of quarantined", dirent.Name())
				}
			}
		})
	}
}

func TestHistoryReloadReadsLegacyRecording(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, recordingName), []byte("legacy output"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorded := loadHistory(dir, DefaultHistoryLimit)
	if err := recorded.usable(); err != nil {
		t.Fatal(err)
	}
	if base, next := recorded.bounds(); base != 0 || next != uint64(len("legacy output")) {
		t.Fatalf("legacy bounds = %d..%d", base, next)
	}
	if got := readAll(t, recorded, 0); string(got) != "legacy output" {
		t.Fatalf("legacy read = %q", got)
	}
}

// A failed write (a full disk) stops recording at the bytes that reached the
// disk, so offsets keep matching the data, and the failure stays visible.
func TestHistoryWriteFailureStopsRecording(t *testing.T) {
	recorded := newTestHistory(t, MinHistoryLimit)
	if err := recorded.append([]byte("kept")); err != nil {
		t.Fatal(err)
	}
	recorded.mu.Lock()
	recorded.writeHook = func(chunk []byte) (int, error) {
		written, err := recorded.active.Write(chunk[:2])
		if err != nil {
			return written, err
		}
		return written, syscall.ENOSPC
	}
	recorded.mu.Unlock()
	err := recorded.append([]byte("lost"))
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, syscall.ENOSPC) && !strings.Contains(err.Error(), syscall.ENOSPC.Error()) {
		t.Fatalf("append on a full disk = %v", err)
	}
	if again := recorded.append([]byte("more")); again == nil || again.Error() != err.Error() {
		t.Fatalf("append after the failure = %v, want the first failure", again)
	}
	if _, next := recorded.bounds(); next != 6 {
		t.Fatalf("next = %d, want the 6 bytes that reached the disk", next)
	}
	if got := readAll(t, recorded, 0); string(got) != "keptlo" {
		t.Fatalf("recorded = %q", got)
	}
}

func TestHistoryReaderReportsEviction(t *testing.T) {
	recorded := newTestHistory(t, MinHistoryLimit)
	if err := recorded.append(patterned(0, 100)); err != nil {
		t.Fatal(err)
	}
	reader := recorded.newReader(0)
	defer reader.close()
	buffer := make([]byte, 50)
	if count, err := reader.read(buffer); count != 50 || err != nil {
		t.Fatalf("read = %d, %v", count, err)
	}
	offset := uint64(100)
	for offset < 3*MinHistoryLimit {
		chunk := patterned(offset, 8192)
		if err := recorded.append(chunk); err != nil {
			t.Fatal(err)
		}
		offset += uint64(len(chunk))
	}
	if _, err := reader.read(buffer); err != errHistoryEvicted {
		t.Fatalf("read behind the base = %v, want errHistoryEvicted", err)
	}
	if pending := func() int {
		recorded.mu.Lock()
		defer recorded.mu.Unlock()
		return len(recorded.removals)
	}(); pending != 0 {
		t.Fatalf("%d evicted segments were not removed after the reader moved off them", pending)
	}
}

func TestSafeReplayStart(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{"line start", "tail of line\nnext line\n", "next line\n"},
		{"inside CSI", "1;31mred\x1b[0m\ngreen\n", "green\n"},
		{"newline inside an OSC string", "title\nmore\x1b\\after\nclean\n", "clean\n"},
		{"newline inside an OSC string ended by BEL", "\x1b]0;a\nb\x07x\nline\n", "line\n"},
		{"UTF-8 continuation without newline", "\x96\x87文字", "文字"},
		{"escape sequence at start without newline", "\x1b[2J\x1b[Hscreen", "screen"},
		{"nothing safe", "\x9f\x9f", ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			data := []byte(test.data)
			if got := string(data[safeReplayStart(data):]); got != test.want {
				t.Fatalf("replay starts at %q, want %q", got, test.want)
			}
		})
	}
}

func TestModesPreludeRestoresInputModes(t *testing.T) {
	var modes terminalModes
	modes.feed([]byte("\x1b[?1049h\x1b[?1h\x1b=\x1b[?2004h\x1b[?1000;1006h\x1b[?25l"))
	prelude := string(modes.prelude())
	for _, want := range []string{"\x1b[?1049h", "\x1b[?1h", "\x1b=", "\x1b[?2004h", "\x1b[?1000h", "\x1b[?1006h", "\x1b[?25l"} {
		if !strings.Contains(prelude, want) {
			t.Fatalf("prelude %q lacks %q", prelude, want)
		}
	}
	modes.feed([]byte("\x1b[?1049l\x1b[?1l\x1b>\x1b[?2004l\x1b[?1000;1006l\x1b[?25h"))
	if prelude := modes.prelude(); len(prelude) != 0 {
		t.Fatalf("prelude after resetting every mode = %q", prelude)
	}
	modes.feed([]byte("\x1b[?2004h\x1bc"))
	if prelude := modes.prelude(); len(prelude) != 0 {
		t.Fatalf("prelude after RIS = %q", prelude)
	}
}

func BenchmarkHistoryReplay(b *testing.B) {
	recorded, err := createHistory(b.TempDir(), DefaultHistoryLimit)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = recorded.closeWriter() }()
	line := []byte(fmt.Sprintf("%-78s\r\n", "\x1b[32mbuild\x1b[0m step output with some text"))
	for offset := uint64(0); offset < DefaultHistoryLimit; offset += uint64(len(line)) {
		if err := recorded.append(line); err != nil {
			b.Fatal(err)
		}
	}
	base, next := recorded.bounds()
	buffer := make([]byte, replayChunkSize)
	b.SetBytes(int64(next - base))
	b.ResetTimer()
	for range b.N {
		reader := recorded.newReader(base)
		for {
			count, err := reader.read(buffer)
			if err != nil {
				b.Fatal(err)
			}
			if count == 0 {
				break
			}
		}
		reader.close()
	}
}

func BenchmarkHistoryRecord(b *testing.B) {
	recorded, err := createHistory(b.TempDir(), DefaultHistoryLimit)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = recorded.closeWriter() }()
	chunk := bytes.Repeat([]byte("output line\r\n"), 32<<10/13)
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for range b.N {
		if err := recorded.append(chunk); err != nil {
			b.Fatal(err)
		}
	}
}
