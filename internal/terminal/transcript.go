package terminal

import (
	"errors"
	"math"
	"sort"
	"sync"
	"time"
)

// ErrTranscriptOutOfOrder rejects an append whose time is earlier than the
// last recorded line's, or whose offset overlaps the previous line.
var ErrTranscriptOutOfOrder = errors.New("terminal: transcript out-of-order append")

// ErrTranscriptOffsetOverflow rejects an append whose text would wrap the
// stream offset past math.MaxUint64.
var ErrTranscriptOffsetOverflow = errors.New("terminal: transcript offset overflow")

// defaultTranscriptSparseStride is the anchor interval of the sparse index:
// one anchor line every this many lines bounds a seek to a binary search
// plus at most one stride of linear scanning.
const defaultTranscriptSparseStride = 64

// TranscriptLine is one recorded output line with its stream byte range and
// wall-clock time. Times are non-decreasing along the transcript; equal
// times are allowed and resolve to the earliest matching line.
type TranscriptLine struct {
	Offset uint64
	End    uint64
	Time   time.Time
	Text   string
}

type transcriptAnchor struct {
	index int
	line  TranscriptLine
}

// Transcript is an indexed, sparse transcript of terminal output lines. The
// first screen reads the tail directly, and distant seeks do a binary search
// over the sparse anchors plus at most one stride of linear scanning, so
// query cost stays bounded by the stride instead of the history length.
type Transcript struct {
	mu      sync.Mutex
	lines   []TranscriptLine
	anchors []transcriptAnchor
	stride  int
}

// NewTranscript builds an empty transcript; stride <= 0 selects
// defaultTranscriptSparseStride.
func NewTranscript(stride int) *Transcript {
	if stride <= 0 {
		stride = defaultTranscriptSparseStride
	}
	return &Transcript{stride: stride}
}

// Append records one line. The time must not be earlier than the last
// line's, and the offset must not overlap the previous line. An offset
// range that would wrap past math.MaxUint64 is rejected before any state
// changes.
func (t *Transcript) Append(offset uint64, text string, at time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if uint64(len(text)) > math.MaxUint64-offset {
		return ErrTranscriptOffsetOverflow
	}
	if n := len(t.lines); n > 0 {
		last := t.lines[n-1]
		if at.Before(last.Time) || offset < last.End {
			return ErrTranscriptOutOfOrder
		}
	}
	line := TranscriptLine{Offset: offset, End: offset + uint64(len(text)), Time: at, Text: text}
	if len(t.lines)%t.stride == 0 {
		t.anchors = append(t.anchors, transcriptAnchor{index: len(t.lines), line: line})
	}
	t.lines = append(t.lines, line)
	return nil
}

// Len returns the number of recorded lines.
func (t *Transcript) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.lines)
}

// AnchorCount returns the number of sparse anchors; it exposes the index
// density for tests and diagnostics.
func (t *Transcript) AnchorCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.anchors)
}

// FirstScreen returns the latest page of lines straight from the tail: the
// cost is the page size, never a rescan of the full history.
func (t *Transcript) FirstScreen(pageSize int) []TranscriptLine {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.lines) == 0 || pageSize <= 0 {
		return nil
	}
	start := max(0, len(t.lines)-pageSize)
	return append([]TranscriptLine(nil), t.lines[start:]...)
}

// SeekToTime returns the page starting at the first line with Time >= at;
// a time past the last line yields the final page. The seek is a binary
// search over the anchors plus at most one stride of linear scanning.
func (t *Transcript) SeekToTime(at time.Time, pageSize int) []TranscriptLine {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.lines) == 0 || pageSize <= 0 {
		return nil
	}
	idx := t.seekLocked(func(line TranscriptLine) bool { return !line.Time.Before(at) })
	return t.pageLocked(idx, pageSize)
}

// SeekToOffset returns the page starting at the first line with
// Offset >= offset; an offset past the last line yields the final page.
func (t *Transcript) SeekToOffset(offset uint64, pageSize int) []TranscriptLine {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.lines) == 0 || pageSize <= 0 {
		return nil
	}
	idx := t.seekLocked(func(line TranscriptLine) bool { return line.Offset >= offset })
	return t.pageLocked(idx, pageSize)
}

// seekLocked returns the first line index matching the predicate, bounded
// to a binary search over the anchors plus at most one stride of line
// probes. A matching anchor is never rescanned: only the interval preceding
// it is probed, and its index is returned directly otherwise.
func (t *Transcript) seekLocked(match func(TranscriptLine) bool) int {
	anchor := sort.Search(len(t.anchors), func(i int) bool { return match(t.anchors[i].line) })
	start := 0
	if anchor > 0 {
		start = t.anchors[anchor-1].index
	}
	end := len(t.lines)
	known := -1
	if anchor < len(t.anchors) {
		end = t.anchors[anchor].index
		known = end
	}
	for i := start; i < end; i++ {
		if match(t.lines[i]) {
			return i
		}
	}
	if known >= 0 {
		return known
	}
	return len(t.lines)
}

// pageLocked returns up to pageSize lines starting at idx; an idx past the
// end yields the final page. The count is derived from the remaining
// length, so a huge page size can never overflow the slice bound.
func (t *Transcript) pageLocked(idx, pageSize int) []TranscriptLine {
	if idx >= len(t.lines) {
		idx = max(0, len(t.lines)-pageSize)
	}
	count := min(pageSize, len(t.lines)-idx)
	return append([]TranscriptLine(nil), t.lines[idx:idx+count]...)
}
