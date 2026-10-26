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
		segments: []historySegment{{start: 0, path: path}},
		active:   file,
	}, nil
}

// loadHistory reads the segments of a recovered session. Segments that do not
// form one contiguous chain are moved to a quarantine directory and the
// history reports ErrHistoryQuarantined instead of guessing offsets. A
// recording from before segmented history (output.raw) is read as one segment
// at offset zero.
func loadHistory(dir string, limit uint64) *history {
	h := &history{dir: dir, limit: limit, segSize: historySegmentSize(limit)}
	if err := h.load(); err != nil {
		h.quarantine(err)
	}
	return h
}

func (h *history) load() error {
	dirents, err := os.ReadDir(h.dir)
	if err != nil {
		return fmt.Errorf("read session directory: %w", err)
	}
	var segments []historySegment
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
			return fmt.Errorf("malformed segment name %q", name)
		}
		segments = append(segments, historySegment{start: start, path: filepath.Join(h.dir, name)})
	}
	if legacy {
		if len(segments) > 0 {
			return errors.New("both a legacy recording and segments are present")
		}
		segments = []historySegment{{start: 0, path: filepath.Join(h.dir, recordingName)}}
	}
	if len(segments) == 0 {
		return errors.New("no recorded output segments")
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].start < segments[j].start })
	next := segments[0].start
	for _, segment := range segments {
		info, err := os.Lstat(segment.path)
		if err != nil {
			return fmt.Errorf("inspect segment: %w", err)
		}
		if err := privateRegularFile(info); err != nil {
			return fmt.Errorf("segment %s is not a private regular file", filepath.Base(segment.path))
		}
		if segment.start != next {
			return fmt.Errorf("segment at %d does not continue the chain ending at %d", segment.start, next)
		}
		size := uint64(info.Size())
		if next+size < next {
			return errors.New("segment offsets overflow")
		}
		next += size
	}
	h.segments = segments
	h.next = next
	return nil
}

func (h *history) quarantine(cause error) {
	h.segments = nil
	h.next = 0
	target := filepath.Join(h.dir, historyQuarantineDir, strconv.FormatInt(time.Now().UnixNano(), 10))
	moveErr := os.MkdirAll(target, 0o700)
	if moveErr == nil {
		dirents, err := os.ReadDir(h.dir)
		if err != nil {
			moveErr = err
		}
		for _, dirent := range dirents {
			name := dirent.Name()
			if name != recordingName && !strings.HasPrefix(name, historySegmentPrefix) {
				continue
			}
			if err := os.Rename(filepath.Join(h.dir, name), filepath.Join(target, name)); err != nil {
				moveErr = errors.Join(moveErr, err)
			}
		}
	}
	h.quarantined = fmt.Errorf("%w: %v", ErrHistoryQuarantined, cause)
	if moveErr != nil {
		h.quarantined = fmt.Errorf("%w (moving it aside failed: %v)", h.quarantined, moveErr)
	}
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
	h.segments = append(h.segments, historySegment{start: h.next, path: path})
	return nil
}

// evictLocked drops whole oldest segments while the retained output exceeds
// the limit. The segment being written is never dropped.
func (h *history) evictLocked() {
	evicted := false
	for len(h.segments) > 1 && h.next-h.segments[0].start > h.limit {
		h.removals = append(h.removals, h.segments[0].path)
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
// nil.
func (r *historyReader) read(p []byte) (int, error) {
	h := r.h
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.closed {
		return 0, ErrClosed
	}
	if h.quarantined != nil {
		return 0, h.quarantined
	}
	if r.pos < h.baseLocked() {
		r.closeFileLocked()
		return 0, errHistoryEvicted
	}
	if r.pos >= h.next {
		return 0, nil
	}
	segment, end, ok := h.segmentLocked(r.pos)
	if !ok {
		return 0, fmt.Errorf("%w: offset %d is outside the recorded segments", ErrUnavailable, r.pos)
	}
	if r.file == nil || r.segStart != segment.start {
		r.closeFileLocked()
		file, err := openRecording(segment.path)
		if err != nil {
			return 0, err
		}
		r.file, r.segStart = file, segment.start
	}
	want := min(uint64(len(p)), end-r.pos)
	count, err := r.file.ReadAt(p[:want], int64(r.pos-segment.start))
	r.pos += uint64(count)
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
