package terminal

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"testing"
	"time"
)

// referenceTranscript is the deliberately naive model: every query is a
// linear scan over the full history. The sparse Transcript must match it
// exactly for every case in these tests.
type referenceTranscript struct {
	lines []TranscriptLine
}

func (r *referenceTranscript) append(offset uint64, text string, at time.Time) {
	r.lines = append(r.lines, TranscriptLine{Offset: offset, End: offset + uint64(len(text)), Time: at, Text: text})
}

func (r *referenceTranscript) firstScreen(pageSize int) []TranscriptLine {
	if len(r.lines) == 0 || pageSize <= 0 {
		return nil
	}
	start := max(0, len(r.lines)-pageSize)
	return append([]TranscriptLine(nil), r.lines[start:]...)
}

func (r *referenceTranscript) seekToTime(at time.Time, pageSize int) []TranscriptLine {
	if len(r.lines) == 0 || pageSize <= 0 {
		return nil
	}
	idx := sort.Search(len(r.lines), func(i int) bool { return !r.lines[i].Time.Before(at) })
	if idx == len(r.lines) {
		return r.firstScreen(pageSize)
	}
	count := min(pageSize, len(r.lines)-idx)
	return append([]TranscriptLine(nil), r.lines[idx:idx+count]...)
}

func (r *referenceTranscript) seekToOffset(offset uint64, pageSize int) []TranscriptLine {
	if len(r.lines) == 0 || pageSize <= 0 {
		return nil
	}
	idx := sort.Search(len(r.lines), func(i int) bool { return r.lines[i].Offset >= offset })
	if idx == len(r.lines) {
		return r.firstScreen(pageSize)
	}
	count := min(pageSize, len(r.lines)-idx)
	return append([]TranscriptLine(nil), r.lines[idx:idx+count]...)
}

func transcriptLinesEqual(a, b []TranscriptLine) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func requireTranscriptParity(t *testing.T, what string, got, want []TranscriptLine) {
	t.Helper()
	if !transcriptLinesEqual(got, want) {
		t.Fatalf("%s = %+v, want reference %+v", what, got, want)
	}
}

func transcriptFixture(t *testing.T, stride int) (*Transcript, *referenceTranscript) {
	t.Helper()
	indexed := NewTranscript(stride)
	reference := &referenceTranscript{}
	base := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	// 200 lines with mixed cadence, including duplicate timestamp runs.
	for i := 0; i < 200; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		if i >= 60 && i < 70 {
			at = base.Add(60 * time.Second) // duplicate run
		}
		offset := uint64(i * 10)
		text := fmt.Sprintf("line-%03d", i)
		if err := indexed.Append(offset, text, at); err != nil {
			t.Fatal(err)
		}
		reference.append(offset, text, at)
	}
	return indexed, reference
}

func TestTranscriptFirstScreenEmpty(t *testing.T) {
	indexed := NewTranscript(0)
	reference := &referenceTranscript{}
	requireTranscriptParity(t, "empty first screen", indexed.FirstScreen(10), reference.firstScreen(10))
	requireTranscriptParity(t, "empty seek time", indexed.SeekToTime(time.Now(), 10), reference.seekToTime(time.Now(), 10))
	requireTranscriptParity(t, "empty seek offset", indexed.SeekToOffset(0, 10), reference.seekToOffset(0, 10))
}

func TestTranscriptFirstScreenTailPage(t *testing.T) {
	indexed, reference := transcriptFixture(t, 16)
	requireTranscriptParity(t, "first screen page", indexed.FirstScreen(25), reference.firstScreen(25))
	requireTranscriptParity(t, "first screen exact", indexed.FirstScreen(200), reference.firstScreen(200))
	requireTranscriptParity(t, "first screen over", indexed.FirstScreen(500), reference.firstScreen(500))
	got := indexed.FirstScreen(25)
	if len(got) != 25 || got[0].Text != "line-175" || got[24].Text != "line-199" {
		t.Fatalf("first screen window = %+v", got)
	}
}

func TestTranscriptSeekToTimeParity(t *testing.T) {
	indexed, reference := transcriptFixture(t, 16)
	base := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"before first":   base.Add(-time.Hour),
		"exactly first":  base,
		"between lines":  base.Add(90 * time.Second),
		"duplicate run":  base.Add(60 * time.Second),
		"duplicate edge": base.Add(69 * time.Second),
		"exactly last":   base.Add(199 * time.Second),
		"after last":     base.Add(24 * time.Hour),
	}
	for name, at := range cases {
		for _, pageSize := range []int{1, 7, 64, 500} {
			requireTranscriptParity(t, fmt.Sprintf("seek %s page %d", name, pageSize),
				indexed.SeekToTime(at, pageSize), reference.seekToTime(at, pageSize))
		}
	}
	// The duplicate run must resolve to the first line of the group.
	got := indexed.SeekToTime(base.Add(60*time.Second), 5)
	if len(got) != 5 || got[0].Text != "line-060" {
		t.Fatalf("duplicate seek = %+v, want first of group line-060", got)
	}
}

func TestTranscriptSeekToOffsetParity(t *testing.T) {
	indexed, reference := transcriptFixture(t, 16)
	cases := map[string]uint64{
		"zero":          0,
		"exact line":    500,
		"mid line":      505,
		"past end":      2000,
		"max uint64":    ^uint64(0),
		"between lines": 1234,
	}
	for name, offset := range cases {
		for _, pageSize := range []int{1, 7, 64, 500} {
			requireTranscriptParity(t, fmt.Sprintf("seek offset %s page %d", name, pageSize),
				indexed.SeekToOffset(offset, pageSize), reference.seekToOffset(offset, pageSize))
		}
	}
}

func TestTranscriptOutOfOrderRejected(t *testing.T) {
	indexed := NewTranscript(0)
	base := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	if err := indexed.Append(0, "a", base); err != nil {
		t.Fatal(err)
	}
	if err := indexed.Append(10, "b", base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := indexed.Append(20, "c", base.Add(-time.Second)); !errors.Is(err, ErrTranscriptOutOfOrder) {
		t.Fatalf("out-of-order time = %v, want ErrTranscriptOutOfOrder", err)
	}
	if err := indexed.Append(5, "d", base.Add(2*time.Second)); !errors.Is(err, ErrTranscriptOutOfOrder) {
		t.Fatalf("offset regression = %v, want ErrTranscriptOutOfOrder", err)
	}
	if indexed.Len() != 2 {
		t.Fatalf("rejected appends changed the transcript: len = %d", indexed.Len())
	}
}

func TestTranscriptSparseIndexBounded(t *testing.T) {
	indexed, reference := transcriptFixture(t, 16)
	// 200 lines at stride 16 → 13 anchors (every 16th line starting at 0).
	if got := indexed.AnchorCount(); got != 13 {
		t.Fatalf("anchor count = %d, want 13", got)
	}
	base := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	// Distant seeks across the whole timeline stay exact against the
	// full-scan reference.
	for _, at := range []time.Time{
		base, base.Add(30 * time.Second), base.Add(100 * time.Second),
		base.Add(150 * time.Second), base.Add(199 * time.Second),
	} {
		requireTranscriptParity(t, "bounded distant seek", indexed.SeekToTime(at, 10), reference.seekToTime(at, 10))
	}
}

func TestTranscriptSeekSingleLine(t *testing.T) {
	indexed := NewTranscript(0)
	reference := &referenceTranscript{}
	at := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	if err := indexed.Append(42, "only", at); err != nil {
		t.Fatal(err)
	}
	reference.append(42, "only", at)
	requireTranscriptParity(t, "single first screen", indexed.FirstScreen(10), reference.firstScreen(10))
	requireTranscriptParity(t, "single seek before", indexed.SeekToTime(at.Add(-time.Hour), 10), reference.seekToTime(at.Add(-time.Hour), 10))
	requireTranscriptParity(t, "single seek after", indexed.SeekToTime(at.Add(time.Hour), 10), reference.seekToTime(at.Add(time.Hour), 10))
	requireTranscriptParity(t, "single seek offset", indexed.SeekToOffset(100, 10), reference.seekToOffset(100, 10))
}

func TestTranscriptAppendOffsetOverflow(t *testing.T) {
	indexed := NewTranscript(4)
	at := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	if err := indexed.Append(math.MaxUint64, "x", at); !errors.Is(err, ErrTranscriptOffsetOverflow) {
		t.Fatalf("max-offset append = %v, want ErrTranscriptOffsetOverflow", err)
	}
	if err := indexed.Append(math.MaxUint64-1, "ab", at); !errors.Is(err, ErrTranscriptOffsetOverflow) {
		t.Fatalf("wrapping append = %v, want ErrTranscriptOffsetOverflow", err)
	}
	if indexed.Len() != 0 || indexed.AnchorCount() != 0 {
		t.Fatalf("rejected appends changed state: len = %d, anchors = %d", indexed.Len(), indexed.AnchorCount())
	}
	// The largest non-wrapping range is still accepted.
	if err := indexed.Append(math.MaxUint64-1, "a", at); err != nil {
		t.Fatalf("max-offset single-byte append = %v", err)
	}
	if indexed.Len() != 1 || indexed.lines[0].End != math.MaxUint64 {
		t.Fatalf("accepted line = %+v, want End == MaxUint64", indexed.lines[0])
	}
	// A follow-up append at offset 0 is an out-of-order regression, not a
	// second overflow.
	if err := indexed.Append(0, "b", at.Add(time.Second)); !errors.Is(err, ErrTranscriptOutOfOrder) {
		t.Fatalf("append after max offset = %v, want ErrTranscriptOutOfOrder", err)
	}
	if indexed.Len() != 1 || indexed.AnchorCount() != 1 {
		t.Fatalf("state changed after rejection: len = %d, anchors = %d", indexed.Len(), indexed.AnchorCount())
	}
}

func TestTranscriptSeekPageSizeOverflow(t *testing.T) {
	indexed, reference := transcriptFixture(t, 16)
	base := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	// A huge page size on a nonzero matching index must not panic and must
	// match the reference exactly.
	requireTranscriptParity(t, "max-int page seek time",
		indexed.SeekToTime(base.Add(100*time.Second), math.MaxInt), reference.seekToTime(base.Add(100*time.Second), math.MaxInt))
	requireTranscriptParity(t, "max-int page seek offset",
		indexed.SeekToOffset(1000, math.MaxInt), reference.seekToOffset(1000, math.MaxInt))
	got := indexed.SeekToTime(base.Add(100*time.Second), math.MaxInt)
	if len(got) != 100 || got[0].Text != "line-100" {
		t.Fatalf("max-int page = %d lines from %v, want 100 from line-100", len(got), got[0])
	}
	// Zero and negative page sizes return nothing from every paged method.
	for _, pageSize := range []int{0, -1} {
		if got := indexed.FirstScreen(pageSize); got != nil {
			t.Fatalf("FirstScreen(%d) = %+v, want nil", pageSize, got)
		}
		if got := indexed.SeekToTime(base, pageSize); got != nil {
			t.Fatalf("SeekToTime(%d) = %+v, want nil", pageSize, got)
		}
		if got := indexed.SeekToOffset(0, pageSize); got != nil {
			t.Fatalf("SeekToOffset(%d) = %+v, want nil", pageSize, got)
		}
	}
}

func TestTranscriptSeekProbeCount(t *testing.T) {
	indexed := NewTranscript(1)
	base := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	if err := indexed.Append(0, "a", base); err != nil {
		t.Fatal(err)
	}
	if err := indexed.Append(1, "b", base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// stride 1, two lines: seeking the second line probes the preceding
	// interval only — the matching anchor itself is never rescanned.
	probes := 0
	idx := indexed.seekLocked(func(line TranscriptLine) bool {
		probes++
		return !line.Time.Before(base.Add(time.Second))
	})
	if idx != 1 {
		t.Fatalf("seek index = %d, want 1", idx)
	}
	// Two anchor probes from the binary search plus at most stride(=1)
	// line probes.
	if probes > 2+1 {
		t.Fatalf("probe count = %d, want ≤ 3", probes)
	}
	// The tail scan past the last anchor still finds late lines.
	late := NewTranscript(4)
	for i := 0; i < 6; i++ {
		if err := late.Append(uint64(i*2), fmt.Sprintf("l%d", i), base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	probes = 0
	idx = late.seekLocked(func(line TranscriptLine) bool {
		probes++
		return !line.Time.Before(base.Add(5 * time.Second))
	})
	if idx != 5 {
		t.Fatalf("tail seek index = %d, want 5", idx)
	}
	// Binary search over 2 anchors plus at most stride(=4) line probes.
	if probes > 2+4 {
		t.Fatalf("tail probe count = %d, want ≤ 6", probes)
	}
}
