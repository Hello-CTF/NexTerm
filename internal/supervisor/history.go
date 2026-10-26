package supervisor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Recorded output is kept per session as a chain of segment files named by the
// absolute offset of their first byte. The oldest retained offset (the history
// base) is the name of the first segment and the next offset is the end of the
// last one, so both survive a crash without a separate index: dropping the
// oldest data is one unlink and starting a segment is one exclusive create.
// Offsets are absolute and only grow; eviction never renumbers bytes.
const (
	// DefaultHistoryLimit is the recorded output a session keeps when its
	// creator does not choose a limit.
	DefaultHistoryLimit uint64 = 8 << 20
	// MinHistoryLimit and MaxHistoryLimit bound a requested limit.
	MinHistoryLimit uint64 = 1 << 20
	MaxHistoryLimit uint64 = 1 << 30
	// DefaultTailBytes is how much recorded output a fresh open replays.
	DefaultTailBytes uint64 = 256 << 10
	// MaxHistoryRead bounds one history read reply.
	MaxHistoryRead = 192 << 10

	minHistorySegment = 64 << 10
	maxHistorySegment = 4 << 20

	historySegmentPrefix = "seg-"
	historySegmentSuffix = ".raw"
	historyOffsetDigits  = 20
	historyQuarantineDir = "quarantine"

	// boundaryScanBytes bounds how far a replay start that must land on a safe
	// boundary is moved forward.
	boundaryScanBytes = 64 << 10
)

// ErrHistoryQuarantined reports a session whose recorded output failed the
// consistency check at load and was moved aside instead of being served.
var ErrHistoryQuarantined = errors.New("supervisor history quarantined")

type historySegment struct {
	start uint64
	path  string
	// modes is the terminal mode state at start; nil when unknown.
	modes *terminalModes
}

type history struct {
	dir     string
	limit   uint64
	segSize uint64

	mu       sync.Mutex
	segments []historySegment
	next     uint64
	// active is the writer of the last segment; nil for a recovered session.
	active *os.File
	// err is the first write failure. Recording stops there so offsets keep
	// matching the bytes on disk.
	err error
	// quarantined is set when the segments on disk were inconsistent.
	quarantined error
	// removals are evicted segments whose unlink failed (an open reader on
	// Windows); they are retried on later evictions and reader closes.
	removals []string

	// modes tracks the terminal modes at next for the snapshot of the next
	// segment; only a history being written keeps it current.
	modes terminalModes

	// writeHook replaces the active segment write in tests.
	writeHook func([]byte) (int, error)
}

func historySegmentSize(limit uint64) uint64 {
	return min(max(limit/8, minHistorySegment), maxHistorySegment)
}

func validHistoryLimit(limit uint64) error {
	if limit < MinHistoryLimit || limit > MaxHistoryLimit {
		return fmt.Errorf("%w: history limit must be between %d and %d bytes", ErrInvalidInput, MinHistoryLimit, MaxHistoryLimit)
	}
	return nil
}

func historySegmentPath(dir string, start uint64) string {
	return filepath.Join(dir, fmt.Sprintf("%s%0*d%s", historySegmentPrefix, historyOffsetDigits, start, historySegmentSuffix))
}

func parseHistorySegmentName(name string) (uint64, bool) {
	digits, ok := strings.CutPrefix(name, historySegmentPrefix)
	if !ok {
		return 0, false
	}
	digits, ok = strings.CutSuffix(digits, historySegmentSuffix)
	if !ok || len(digits) != historyOffsetDigits {
		return 0, false
	}
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseUint(digits, 10, 64)
	return value, err == nil
}

// createHistory starts an empty history at offset zero in dir.
func createHistory(dir string, limit uint64) (*history, error) {
	path := historySegmentPath(dir, 0)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create supervisor recording: %w", err)
	}
	return &history{
		dir: dir, limit: limit, segSize: historySegmentSize(limit),
		segments: []historySegment{{start: 0, path: path, modes: &terminalModes{}}},
		active:   file,
	}, nil
}

// loadHistory reads the segments of a recovered session. It keeps the
// longest contiguous chain of private segments that ends at the last one;
// segments before a break are moved to a quarantine directory and the
// history base becomes the start of the kept chain. A history with no usable
// chain reports ErrHistoryQuarantined instead of guessing offsets. A
// recording from before segmented history (output.raw) is read as one
// segment at offset zero and is never moved: it stays read-only, and with
// segments beside it the history is reported unavailable.
func loadHistory(dir string, limit uint64) *history {
	h := &history{dir: dir, limit: limit, segSize: historySegmentSize(limit)}
	if err := h.load(); err != nil {
		h.segments = nil
		h.next = 0
		h.quarantined = fmt.Errorf("%w: %v", ErrHistoryQuarantined, err)
	}
	return h
}

type loadedSegment struct {
	historySegment
	size  uint64
	valid bool
}

func (h *history) load() error {
	dirents, err := os.ReadDir(h.dir)
	if err != nil {
		return fmt.Errorf("read session directory: %w", err)
	}
	var segments []loadedSegment
	var malformed []string
	legacy := false
	for _, dirent := range dirents {
		name := dirent.Name()
		if name == recordingName {
			legacy = true
			continue
		}
		if !strings.HasPrefix(name, historySegmentPrefix) {
			continue
		}
		start, ok := parseHistorySegmentName(name)
		if !ok {
			malformed = append(malformed, name)
			continue
		}
		segment := loadedSegment{historySegment: historySegment{start: start, path: filepath.Join(h.dir, name)}}
		if info, err := os.Lstat(segment.path); err == nil && privateRegularFile(info) == nil {
			segment.size, segment.valid = uint64(info.Size()), true
		}
		segments = append(segments, segment)
	}
	if legacy {
		if len(segments) > 0 || len(malformed) > 0 {
			return errors.New("a legacy recording and segments are both present; the legacy recording is left untouched")
		}
		path := filepath.Join(h.dir, recordingName)
		info, err := os.Lstat(path)
		if err != nil || privateRegularFile(info) != nil {
			return errors.New("the legacy recording is not a private regular file")
		}
		h.segments = []historySegment{{start: 0, path: path, modes: &terminalModes{}}}
		h.next = uint64(info.Size())
		return nil
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].start < segments[j].start })
	first := len(segments)
	for index := len(segments) - 1; index >= 0; index-- {
		segment := segments[index]
		if !segment.valid || segment.start+segment.size < segment.start {
			break
		}
		if index+1 < len(segments) && segment.start+segment.size != segments[index+1].start {
			break
		}
		first = index
	}
	var reasons []string
	if len(malformed) > 0 {
		reasons = append(reasons, fmt.Sprintf("malformed segment names %v", malformed))
	}
	if first > 0 {
		reasons = append(reasons, fmt.Sprintf("%d segment(s) before offset %d do not continue the chain", first, segments[first-1].start))
	}
	var moveErr error
	if len(reasons) > 0 {
		moveErr = h.quarantineFiles(segments[:first], malformed)
	}
	if first == len(segments) {
		if len(segments) == 0 && len(malformed) == 0 {
			return errors.New("no recorded output segments")
		}
		return fmt.Errorf("no contiguous recorded output (%s)", strings.Join(reasons, "; "))
	}
	for _, segment := range segments[first:] {
		segment.modes = readModesSnapshot(h.dir, segment.start)
		h.segments = append(h.segments, segment.historySegment)
	}
	last := segments[len(segments)-1]
	h.next = last.start + last.size
	if moveErr != nil {
		return fmt.Errorf("quarantine the broken part of the recorded output: %w", moveErr)
	}
	return nil
}

// quarantineFiles moves segments that cannot be served, with their mode
// snapshots, to a quarantine directory for inspection.
func (h *history) quarantineFiles(segments []loadedSegment, malformed []string) error {
	target := filepath.Join(h.dir, historyQuarantineDir, strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	names := append([]string(nil), malformed...)
	for _, segment := range segments {
		names = append(names, filepath.Base(segment.path), filepath.Base(historyModesPath(h.dir, segment.start)))
	}
	var moveErr error
	for _, name := range names {
		if err := os.Rename(filepath.Join(h.dir, name), filepath.Join(target, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			moveErr = errors.Join(moveErr, err)
		}
	}
	return moveErr
}

// bounds returns the oldest retained offset and the next offset to record.
func (h *history) bounds() (base, next uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.baseLocked(), h.next
}

func (h *history) baseLocked() uint64 {
	if len(h.segments) == 0 {
		return h.next
	}
	return h.segments[0].start
}

func (h *history) usable() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.quarantined
}

// append records chunk. After the first write failure nothing more is
// recorded and the failure is returned for every later chunk.
func (h *history) append(chunk []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return h.err
	}
	if h.active == nil {
		h.err = fmt.Errorf("%w: supervisor recording is closed", ErrUnavailable)
		return h.err
	}
	last := h.segments[len(h.segments)-1]
	if size := h.next - last.start; size > 0 && size+uint64(len(chunk)) > h.segSize {
		if err := h.rollLocked(); err != nil {
			h.err = fmt.Errorf("%w: start supervisor recording segment: %v", ErrUnavailable, err)
			return h.err
		}
	}
	written, err := h.writeLocked(chunk)
	h.next += uint64(written)
	h.modes.feed(chunk[:written])
	if err == nil && written < len(chunk) {
		err = io.ErrShortWrite
	}
	if err != nil {
		h.err = fmt.Errorf("%w: write supervisor recording: %v", ErrUnavailable, err)
	}
	h.evictLocked()
	return h.err
}

func (h *history) writeLocked(chunk []byte) (int, error) {
	if h.writeHook != nil {
		return h.writeHook(chunk)
	}
	return h.active.Write(chunk)
}

func (h *history) rollLocked() error {
	modes := h.modes
	// A snapshot that cannot be written only costs a recovered session the
	// modes before this segment; recording continues.
	_ = writeModesSnapshot(h.dir, h.next, modes)
	path := historySegmentPath(h.dir, h.next)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := h.active.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	h.active = file
	h.segments = append(h.segments, historySegment{start: h.next, path: path, modes: &modes})
	return nil
}

// evictLocked drops whole oldest segments while the retained output exceeds
// the limit. The segment being written is never dropped.
func (h *history) evictLocked() {
	evicted := false
	for len(h.segments) > 1 && h.next-h.segments[0].start > h.limit {
		h.removals = append(h.removals, h.segments[0].path, historyModesPath(h.dir, h.segments[0].start))
		h.segments = h.segments[1:]
		evicted = true
	}
	if evicted {
		h.retryRemovalsLocked()
	}
}

func (h *history) retryRemovalsLocked() {
	if len(h.removals) == 0 {
		return
	}
	kept := h.removals[:0]
	for _, path := range h.removals {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			kept = append(kept, path)
		}
	}
	h.removals = kept
}

func (h *history) closeWriter() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active == nil {
		return nil
	}
	err := h.active.Close()
	h.active = nil
	if errors.Is(err, os.ErrClosed) {
		err = nil
	}
	return err
}

// segmentLocked returns the segment holding offset and its end.
func (h *history) segmentLocked(offset uint64) (historySegment, uint64, bool) {
	index := sort.Search(len(h.segments), func(i int) bool { return h.segments[i].start > offset }) - 1
	if index < 0 {
		return historySegment{}, 0, false
	}
	end := h.next
	if index+1 < len(h.segments) {
		end = h.segments[index+1].start
	}
	if offset >= end && index+1 < len(h.segments) {
		return historySegment{}, 0, false
	}
	return h.segments[index], end, true
}

// readAt copies retained bytes starting at offset into p. It returns the count
// and false when offset is no longer retained.
func (h *history) readAt(offset uint64, p []byte) (int, bool, error) {
	reader := h.newReader(offset)
	defer reader.close()
	total := 0
	for total < len(p) {
		count, err := reader.read(p[total:])
		total += count
		if err == errHistoryEvicted {
			return total, total > 0, nil
		}
		if err != nil {
			return total, true, err
		}
		if count == 0 {
			break
		}
	}
	return total, true, nil
}

var errHistoryEvicted = errors.New("recorded output was evicted")

// historyReader streams retained bytes from an absolute offset, keeping the
// current segment open between reads.
type historyReader struct {
	h        *history
	pos      uint64
	file     *os.File
	segStart uint64
	closed   bool
}

func (h *history) newReader(offset uint64) *historyReader {
	return &historyReader{h: h, pos: offset}
}

// read returns retained bytes at the reader position. A position below the
// history base returns errHistoryEvicted; a position at the end returns 0 and
// nil. The file read runs outside the history lock, so recording never waits
// for a replay's disk read; an evicted segment stays readable through the
// open descriptor.
func (r *historyReader) read(p []byte) (int, error) {
	h := r.h
	h.mu.Lock()
	if r.closed {
		h.mu.Unlock()
		return 0, ErrClosed
	}
	if h.quarantined != nil {
		h.mu.Unlock()
		return 0, h.quarantined
	}
	if r.pos < h.baseLocked() {
		r.closeFileLocked()
		h.mu.Unlock()
		return 0, errHistoryEvicted
	}
	if r.pos >= h.next {
		h.mu.Unlock()
		return 0, nil
	}
	segment, end, ok := h.segmentLocked(r.pos)
	if !ok {
		h.mu.Unlock()
		return 0, fmt.Errorf("%w: offset %d is outside the recorded segments", ErrUnavailable, r.pos)
	}
	if r.file == nil || r.segStart != segment.start {
		r.closeFileLocked()
		file, err := openRecording(segment.path)
		if err != nil {
			h.mu.Unlock()
			return 0, err
		}
		r.file, r.segStart = file, segment.start
	}
	file := r.file
	want := min(uint64(len(p)), end-r.pos)
	h.mu.Unlock()

	count, err := file.ReadAt(p[:want], int64(r.pos-segment.start))
	r.pos += uint64(count)
	if errors.Is(err, os.ErrClosed) && count == 0 {
		return 0, ErrClosed
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return count, fmt.Errorf("%w: read supervisor recording: %v", ErrUnavailable, err)
	}
	if count == 0 {
		return 0, fmt.Errorf("%w: supervisor recording is shorter than its recorded offsets", ErrUnavailable)
	}
	return count, nil
}

func (r *historyReader) closeFileLocked() {
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
		r.h.retryRemovalsLocked()
	}
}

func (r *historyReader) close() {
	r.h.mu.Lock()
	r.closed = true
	r.closeFileLocked()
	r.h.mu.Unlock()
}

// safeStartFrom returns the first offset at or after offset where a replay
// can begin: the start of a line outside any escape sequence when one is
// close, otherwise a character boundary outside any escape sequence. Offset
// zero is always safe.
func (h *history) safeStartFrom(offset uint64) (uint64, error) {
	if offset == 0 {
		return 0, nil
	}
	_, next := h.bounds()
	window := make([]byte, min(boundaryScanBytes, next-min(offset, next)))
	count, ok, err := h.readAt(offset, window)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errHistoryEvicted
	}
	return offset + uint64(safeReplayStart(window[:count])), nil
}
