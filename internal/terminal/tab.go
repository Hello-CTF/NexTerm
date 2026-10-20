package terminal

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

const DefaultTailDumpBytes = 256 * 1024

const MaxTerminalDimension = 1024

var ErrClosed = errors.New("terminal: tab closed")

// ErrRecordingSaturated marks a recording whose bounded write queue overflowed:
// the recording is failed and the dropped byte count is visible in its status.
var ErrRecordingSaturated = errors.New("terminal: recording saturated")

// ErrRecordingDrainTimeout marks a stop whose queued chunks could not be
// written within the drain timeout; the queue was abandoned.
var ErrRecordingDrainTimeout = errors.New("terminal: recording drain timeout")

type State struct {
	AltScreen         bool `json:"altScreen"`
	CursorVisible     bool `json:"cursorVisible"`
	BracketedPaste    bool `json:"bracketedPaste"`
	ApplicationCursor bool `json:"applicationCursor"`
}

type Snapshot struct {
	Text            string   `json:"text"`
	Lines           []string `json:"lines"`
	CursorRow       int      `json:"cursorRow"`
	CursorCol       int      `json:"cursorCol"`
	Cols            int      `json:"cols"`
	Rows            int      `json:"rows"`
	AltScreen       bool     `json:"altScreen"`
	LastOutputMsAgo uint64   `json:"lastOutputMsAgo"`
	// Title is the latest OSC 0/2 dynamic window title reported by the
	// application; it is empty until the application sets one.
	Title string `json:"title,omitempty"`
}

type RecordingState string

const (
	RecordingNeverStarted RecordingState = "never-started"
	RecordingRunning      RecordingState = "running"
	RecordingStopped      RecordingState = "stopped"
	RecordingFailed       RecordingState = "failed"
)

type RecordingStatus struct {
	TabID      string         `json:"tabId"`
	State      RecordingState `json:"state"`
	Bytes      uint64         `json:"bytes"`
	Path       string         `json:"path,omitempty"`
	SafeError  string         `json:"safeError,omitempty"`
	Generation uint64         `json:"generation"`
	// Dropped counts bytes that never reached the file: chunks rejected
	// after a failure and queue entries discarded when a write failed or a
	// drain was abandoned.
	Dropped uint64 `json:"dropped,omitempty"`
}

const (
	defaultRecordingQueueChunks  = 1024
	defaultRecordingQueueBytes   = 4 << 20
	defaultRecordingDrainTimeout = 5 * time.Second
)

// recording writes chunks to the file from a dedicated goroutine so terminal
// interaction only ever enqueues. The queue is bounded: on saturation the
// recording fails visibly (status error plus dropped count) instead of
// blocking the terminal. Stop drains the queue with a bounded wait, then
// abandons it explicitly.
type recording struct {
	file       io.WriteCloser
	path       string
	generation uint64

	maxChunks int
	maxBytes  int

	mu       sync.Mutex
	bytes    uint64
	err      error
	queue    [][]byte
	queued   int
	dropped  uint64
	closed   bool
	notified bool
	done     chan struct{}
	wake     chan struct{}

	// onFailure runs once on the writer goroutine after a failed recording
	// has fully drained and closed its file; it emits the failure status.
	onFailure func()

	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error
}

func newRecording(tab *Tab, file io.WriteCloser, path string, generation uint64, maxChunks, maxBytes int) *recording {
	if maxChunks <= 0 {
		maxChunks = defaultRecordingQueueChunks
	}
	if maxBytes <= 0 {
		maxBytes = defaultRecordingQueueBytes
	}
	rec := &recording{
		file: file, path: path, generation: generation,
		maxChunks: maxChunks, maxBytes: maxBytes,
		done: make(chan struct{}), wake: make(chan struct{}, 1), closeDone: make(chan struct{}),
	}
	rec.onFailure = func() {
		tab.enqueueRecordingNotification(rec.status(tab.id))
		tab.drainRecordingNotifications()
	}
	go rec.run()
	return rec
}

// run owns the file: it drains the queue in FIFO order and closes the file
// once, when no chunks remain or the recording has failed. The close itself
// runs on a dedicated goroutine: run waits for it without holding r.mu, so a
// stuck external Close can never take the recording mutex with it.
func (r *recording) run() {
	defer close(r.done)
	for {
		chunk, ok := r.next()
		if !ok {
			break
		}
		r.write(chunk)
	}
	r.requestClose()
	<-r.closeDone
	r.mu.Lock()
	if r.closeErr != nil && !errors.Is(r.closeErr, os.ErrClosed) && r.err == nil {
		r.err = r.closeErr
	}
	failed := r.err != nil
	r.mu.Unlock()
	if failed && r.onFailure != nil && r.claimNotify() {
		r.onFailure()
	}
}

// next pops the next queued chunk, parking only while the queue is empty and
// the recording is still accepting chunks.
func (r *recording) next() ([]byte, bool) {
	for {
		r.mu.Lock()
		if len(r.queue) > 0 {
			chunk := r.queue[0]
			r.queue[0] = nil
			r.queue = r.queue[1:]
			r.queued -= len(chunk)
			r.mu.Unlock()
			return chunk, true
		}
		if r.closed || r.err != nil {
			r.mu.Unlock()
			return nil, false
		}
		r.mu.Unlock()
		<-r.wake
	}
}

// write writes one chunk; the first failure discards the rest of the queue
// because the file is dead. The unwritten remainder of a failed write always
// counts as dropped, even when an earlier error (e.g. a drain abandonment)
// is already recorded.
func (r *recording) write(chunk []byte) {
	n, err := r.file.Write(chunk)
	r.mu.Lock()
	r.bytes += uint64(n)
	if err == nil && n < len(chunk) {
		err = io.ErrShortWrite
	}
	if err != nil {
		r.dropped += uint64(len(chunk) - n)
		if r.err == nil {
			r.err = err
			r.discardQueueLocked()
		}
	}
	r.mu.Unlock()
}

// enqueue accepts a chunk without blocking; a full queue fails the recording
// with ErrRecordingSaturated and counts the rejected chunk as dropped. The
// failure transition is published immediately: the claim happens here on the
// interaction path, the notification runs on its own goroutine.
func (r *recording) enqueue(p []byte) {
	r.mu.Lock()
	if r.err != nil || r.closed {
		r.dropped += uint64(len(p))
		r.mu.Unlock()
		return
	}
	if len(r.queue) >= r.maxChunks || r.queued+len(p) > r.maxBytes {
		r.err = ErrRecordingSaturated
		r.dropped += uint64(len(p))
		r.mu.Unlock()
		if r.claimNotify() {
			go r.onFailure()
		}
		return
	}
	r.queue = append(r.queue, bytes.Clone(p))
	r.queued += len(p)
	r.mu.Unlock()
	r.signalWake()
}

// requestClose closes the underlying file exactly once, on a dedicated
// goroutine, and returns immediately: an io.WriteCloser does not guarantee
// that Close returns promptly, so no caller ever waits for an in-flight or
// stuck Close. The close result is recorded for the status bookkeeping.
func (r *recording) requestClose() {
	r.closeOnce.Do(func() {
		go func() {
			err := r.file.Close()
			r.mu.Lock()
			r.closeErr = err
			r.mu.Unlock()
			close(r.closeDone)
		}()
	})
}

// stop closes the queue; the writer drains what remains and exits.
func (r *recording) stop() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.signalWake()
}

// waitDrain waits for the writer to finish the queued chunks.
func (r *recording) waitDrain(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-r.done:
		return true
	case <-timer.C:
		return false
	}
}

// abandon gives up on a stuck writer: the queue is dropped, the timeout
// becomes the recording error when none was set, and the file close is
// initiated best-effort. The writer is never joined here: io.WriteCloser
// does not guarantee that Close interrupts an in-flight Write or returns
// promptly itself, so the writer finalizes asynchronously whenever its
// Write and the file Close return.
func (r *recording) abandon(timeout time.Duration) {
	r.mu.Lock()
	if r.err == nil {
		r.err = errors.Join(ErrRecordingDrainTimeout, fmt.Errorf("after %s", timeout))
	}
	r.discardQueueLocked()
	r.mu.Unlock()
	r.requestClose()
}

// terminate stops a failed recording without waiting for its writer: the
// queue is discarded and the file close is initiated best-effort so a stuck
// write is interrupted. Unlike the healthy drain path this never waits,
// because a reentrant notification handler may be running on the writer
// goroutine itself.
func (r *recording) terminate() {
	select {
	case <-r.done:
		return // the writer already exited and closed the file
	default:
	}
	r.mu.Lock()
	r.closed = true
	r.discardQueueLocked()
	r.mu.Unlock()
	r.requestClose()
}

func (r *recording) discardQueueLocked() {
	for _, chunk := range r.queue {
		r.dropped += uint64(len(chunk))
	}
	r.queue = nil
	r.queued = 0
}

func (r *recording) signalWake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *recording) failed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err != nil
}

// claimNotify reports whether the caller should emit the failure
// notification, so the writer and a concurrent stop emit it exactly once.
func (r *recording) claimNotify() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.notified {
		return false
	}
	r.notified = true
	return true
}

func (r *recording) counts() (written, dropped uint64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bytes, r.dropped, r.err
}

func (r *recording) status(tabID string) RecordingStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := RecordingStatus{
		TabID: tabID, State: RecordingRunning, Bytes: r.bytes,
		Path: r.path, Generation: r.generation, Dropped: r.dropped,
	}
	if r.err != nil {
		status.State = RecordingFailed
		status.SafeError = r.err.Error()
	}
	return status
}

type Tab struct {
	id        string
	sessionID string

	cols atomic.Int64
	rows atomic.Int64

	visible      atomic.Bool
	lastOutput   atomic.Int64
	lastOutputAt atomic.Int64
	closed       atomic.Bool
	// quietSeq is the seqlock guarding every quiet-visible write: writers
	// mark the begin and end of a mutation with odd/even transitions, and
	// readers retry until they observe a stable even sequence.
	quietSeq atomic.Uint64

	mu    sync.Mutex
	ring  *Ring
	tr    *Transcoder
	modes *ModeTracker
	scr   *screen
	state State

	recOpMu       sync.Mutex
	recMu         sync.Mutex
	rec           *recording
	recGeneration uint64
	recStopped    RecordingStatus

	notifyMu       sync.Mutex
	notifyQueue    []RecordingStatus
	notifyDraining bool

	recQueueChunks  int
	recQueueBytes   int
	recDrainTimeout time.Duration

	resizeMu         sync.Mutex
	responseHandler  atomic.Value
	resizeHandler    atomic.Value
	recordingHandler atomic.Value

	now func() time.Time

	closeOnce sync.Once
}

type responseHandler struct{ fn func([]byte) }
type resizeHandler struct{ fn func(cols, rows int) }
type recordingHandler struct{ fn func(RecordingStatus) }

type tabConfig struct {
	clock           func() time.Time
	historyLines    int
	onResponse      func([]byte)
	recQueueChunks  int
	recQueueBytes   int
	recDrainTimeout time.Duration
}

type TabOption func(*tabConfig)

func WithClock(clock func() time.Time) TabOption {
	return func(c *tabConfig) { c.clock = clock }
}

func WithScreenHistory(n int) TabOption {
	return func(c *tabConfig) { c.historyLines = n }
}

func WithResponseHandler(h func([]byte)) TabOption {
	return func(c *tabConfig) { c.onResponse = h }
}

// WithRecordingQueue bounds the asynchronous recording write queue; the
// defaults are defaultRecordingQueueChunks/defaultRecordingQueueBytes.
func WithRecordingQueue(chunks, bytes int) TabOption {
	return func(c *tabConfig) { c.recQueueChunks, c.recQueueBytes = chunks, bytes }
}

// WithRecordingDrainTimeout bounds how long StopRecording waits for queued
// chunks before abandoning them; the default is defaultRecordingDrainTimeout.
func WithRecordingDrainTimeout(d time.Duration) TabOption {
	return func(c *tabConfig) { c.recDrainTimeout = d }
}

func NewTab(id, sessionID string, cols, rows int, enc Encoding, opts ...TabOption) *Tab {
	cfg := tabConfig{
		clock:        time.Now,
		historyLines: ScrollbackLines,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.clock == nil {
		cfg.clock = time.Now
	}
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	t := &Tab{
		id:        id,
		sessionID: sessionID,
		ring:      NewRing(ScrollbackBytes),
		tr:        NewTranscoder(enc),
		modes:     NewModeTracker(),
		scr:       newScreen(cols, rows, cfg.historyLines),
		state:     State{CursorVisible: true},
		now:       cfg.clock,

		recQueueChunks:  cfg.recQueueChunks,
		recQueueBytes:   cfg.recQueueBytes,
		recDrainTimeout: cfg.recDrainTimeout,
	}
	if t.recQueueChunks <= 0 {
		t.recQueueChunks = defaultRecordingQueueChunks
	}
	if t.recQueueBytes <= 0 {
		t.recQueueBytes = defaultRecordingQueueBytes
	}
	if t.recDrainTimeout <= 0 {
		t.recDrainTimeout = defaultRecordingDrainTimeout
	}
	t.cols.Store(int64(cols))
	t.rows.Store(int64(rows))
	t.visible.Store(true)
	t.lastOutput.Store(cfg.clock().UnixMilli())
	if cfg.onResponse != nil {
		t.SetResponseHandler(cfg.onResponse)
	}
	return t
}

func (t *Tab) ID() string { return t.id }

func (t *Tab) SessionID() string { return t.sessionID }

func (t *Tab) Cols() int { return int(t.cols.Load()) }

func (t *Tab) Rows() int { return int(t.rows.Load()) }

func (t *Tab) SetVisible(visible bool) { t.visible.Store(visible) }

func (t *Tab) IsVisible() bool { return t.visible.Load() }

func (t *Tab) SetResponseHandler(h func([]byte)) {
	t.responseHandler.Store(responseHandler{fn: h})
}

func (t *Tab) SetResizeHandler(h func(cols, rows int)) {
	t.resizeHandler.Store(resizeHandler{fn: h})
}

func (t *Tab) Close() {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.quietBegin()
		t.closed.Store(true)
		t.quietEnd()
		t.mu.Unlock()
		// Closing the tab stops the active recording — healthy or failed —
		// instead of leaking the writer goroutine and the file.
		_, _ = t.StopRecording()
	})
}

func (t *Tab) Feed(raw []byte) {
	if len(raw) == 0 || t.closed.Load() {
		return
	}
	t.mu.Lock()
	if t.closed.Load() {
		t.mu.Unlock()
		return
	}
	t.quietBegin()
	now := t.now().UnixMilli()
	t.lastOutput.Store(now)
	t.lastOutputAt.Store(now)
	t.ring.Push(raw)
	decoded := t.tr.Feed(raw)
	t.modes.Feed(decoded)
	t.scr.write(decoded)
	t.state = State{
		AltScreen:         t.scr.altScreen(),
		CursorVisible:     t.modes.CursorVisible(),
		BracketedPaste:    t.modes.BracketedPaste(),
		ApplicationCursor: t.modes.ApplicationCursor(),
	}
	responses := t.scr.takeResponses()
	t.quietEnd()
	t.mu.Unlock()
	// The response handler runs after the quiet write closes so a blocked
	// callback can never stall quiet readers.
	if len(responses) > 0 {
		if h, _ := t.responseHandler.Load().(responseHandler); h.fn != nil {
			h.fn(responses)
		}
	}
}

func (t *Tab) State() State {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// TabQuietState is one tab's quiet reading: idle milliseconds since the
// last output, a version that bumps on any output, resize, mode change,
// encoding switch, or close, and whether the tab is closed. Two readings
// whose versions match describe one consistent state.
type TabQuietState struct {
	IdleMS  int64
	Version uint64
	Closed  bool
}

// quietBegin opens a quiet-visible write: the sequence turns odd until
// quietEnd, so readers never observe a partially applied mutation.
func (t *Tab) quietBegin() {
	t.quietSeq.Add(1)
}

// quietEnd closes a quiet-visible write and advances the version: the
// sequence turns even again, publishing every field written since
// quietBegin as one consistent state.
func (t *Tab) quietEnd() {
	t.quietSeq.Add(1)
}

// QuietState returns a consistent snapshot of the tab's quiet state. The
// sequence is sampled before and after the field reads and the result is
// retried unless both samples are the same even value, so a snapshot never
// certifies pre-output idle with a version that predates the output.
func (t *Tab) QuietState() TabQuietState {
	for {
		seq := t.quietSeq.Load()
		if seq%2 != 0 {
			continue
		}
		version := seq / 2
		idle := t.LastOutputAgo().Milliseconds()
		closed := t.closed.Load()
		if seq == t.quietSeq.Load() && seq%2 == 0 {
			return TabQuietState{IdleMS: idle, Version: version, Closed: closed}
		}
	}
}

func (t *Tab) BracketedPasteMode() (bool, uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.modes.BracketedPaste(), t.modes.BracketedPasteGeneration()
}

func (t *Tab) SwitchEncoding(enc Encoding) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.quietBegin()
	t.tr.Switch(enc)
	t.quietEnd()
}

func (t *Tab) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > MaxTerminalDimension || rows > MaxTerminalDimension {
		return fmt.Errorf("terminal: invalid size %dx%d", cols, rows)
	}
	t.resizeMu.Lock()
	defer t.resizeMu.Unlock()
	t.mu.Lock()
	if t.closed.Load() {
		t.mu.Unlock()
		return ErrClosed
	}
	t.quietBegin()
	t.scr.resize(cols, rows)
	t.cols.Store(int64(cols))
	t.rows.Store(int64(rows))
	t.quietEnd()
	t.mu.Unlock()
	if h, _ := t.resizeHandler.Load().(resizeHandler); h.fn != nil {
		h.fn(cols, rows)
	}
	return nil
}

func (t *Tab) ScreenText() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.scr.text()
}

func (t *Tab) Snapshot() Snapshot {
	t.mu.Lock()
	text := t.scr.text()
	row, col := t.scr.cursor()
	cols, rows := t.scr.size()
	alt := t.scr.altScreen()
	title := t.scr.title
	t.mu.Unlock()
	return Snapshot{
		Text:            text,
		Lines:           splitScreenLines(text),
		CursorRow:       row,
		CursorCol:       col,
		Cols:            cols,
		Rows:            rows,
		AltScreen:       alt,
		LastOutputMsAgo: uint64(t.LastOutputAgo().Milliseconds()),
		Title:           title,
	}
}

// Title returns the latest OSC 0/2 dynamic window title reported by the
// application; it is empty until the application sets one.
func (t *Tab) Title() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.scr.title
}

func (t *Tab) ScreenHistory(n int) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.scr.historyLines(n)
}

func (t *Tab) ScrollbackLen() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.scr.scrollbackLen()
}

func splitScreenLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i, line := range lines {
		lines[i] = strings.TrimRightFunc(line, unicode.IsSpace)
	}
	return lines
}

func (t *Tab) TailLines(n int) []string {
	if n <= 0 {
		return nil
	}
	t.mu.Lock()
	dump := t.ring.Dump(DefaultTailDumpBytes)
	enc := t.tr.Encoding()
	t.mu.Unlock()

	text := string(decodeAll(enc, dump))
	all := strings.Split(text, "\n")
	for i, line := range all {
		all[i] = strings.TrimRight(line, "\r")
	}
	all = append(all, t.Snapshot().Lines...)
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

func (t *Tab) Dump(maxBytes int) []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ring.Dump(maxBytes)
}

func (t *Tab) BaseSeq() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ring.Base()
}

func (t *Tab) LatestSeq() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ring.Total()
}

func (t *Tab) DroppedBytes() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ring.Dropped()
}

func (t *Tab) ReplayFrom(seq uint64, max int) (data []byte, start uint64, clamped bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ring.ReplayFrom(seq, max)
}

func (t *Tab) ExportLog(path string, maxBytes int) (uint64, error) {
	data := t.Dump(maxBytes)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return 0, fmt.Errorf("terminal: export %s: %w", path, err)
	}
	return uint64(len(data)), nil
}

func (t *Tab) IsIdle(quiet time.Duration) bool { return t.LastOutputAgo() >= quiet }

func (t *Tab) LastOutputAt() int64 { return t.lastOutputAt.Load() }

func (t *Tab) LastOutputAgo() time.Duration {
	ago := t.now().UnixMilli() - t.lastOutput.Load()
	if ago < 0 {
		ago = 0
	}
	return time.Duration(ago) * time.Millisecond
}

func (t *Tab) StartRecording(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("terminal: record %s: %w", path, err)
	}
	t.recOpMu.Lock()
	t.recMu.Lock()
	old := t.rec
	t.recMu.Unlock()
	if old != nil {
		// A generation that already failed was announced when it failed; a
		// failure that only surfaces while stopping the old generation (a
		// close error) must stay visible to the caller.
		wasFailed := old.failed()
		if _, err := t.stopRecording(old); err != nil && !wasFailed {
			t.recOpMu.Unlock()
			t.drainRecordingNotifications()
			_ = file.Close()
			return err
		}
	}
	t.recMu.Lock()
	t.recGeneration++
	rec := newRecording(t, file, path, t.recGeneration, t.recQueueChunks, t.recQueueBytes)
	t.rec = rec
	status := t.recordingStatusLocked()
	t.recMu.Unlock()
	// Handlers run after the operation lock is released so a callback can
	// synchronously reenter Start/Stop without deadlocking; notifications
	// are delivered in transition order from the shared queue.
	t.recOpMu.Unlock()
	t.enqueueRecordingNotification(status)
	t.drainRecordingNotifications()
	return nil
}

func (t *Tab) StopRecording() (uint64, error) {
	t.recOpMu.Lock()
	t.recMu.Lock()
	rec := t.rec
	t.recMu.Unlock()
	if rec == nil {
		t.recOpMu.Unlock()
		return 0, nil
	}
	n, err := t.stopRecording(rec)
	// Handlers run after the operation lock is released so a callback can
	// synchronously reenter Start/Stop without deadlocking; notifications
	// are delivered in transition order from the shared queue.
	t.recOpMu.Unlock()
	t.drainRecordingNotifications()
	return n, err
}

// stopRecording stops a recording without holding recMu across the drain, so
// the output path never waits on a stalled file. A recording that failed
// before entry is terminated without any wait: its writer may be stuck, and
// a reentrant notification handler may be running on the writer goroutine
// itself. A healthy recording gets a bounded drain; on timeout the queue is
// abandoned and the writer finalizes asynchronously — it is never joined,
// because Close does not guarantee interrupting an in-flight Write.
func (t *Tab) stopRecording(rec *recording) (uint64, error) {
	if rec.failed() {
		rec.terminate()
		n, _, err := rec.counts()
		if rec.claimNotify() {
			t.enqueueRecordingNotification(rec.status(t.id))
		}
		return n, err
	}
	rec.stop()
	if !rec.waitDrain(t.recDrainTimeout) {
		rec.abandon(t.recDrainTimeout)
	}
	n, _, err := rec.counts()
	status := rec.status(t.id)
	t.recMu.Lock()
	if t.rec == rec && err == nil {
		status.State = RecordingStopped
		t.recStopped = status
		t.rec = nil
	}
	t.recMu.Unlock()
	if err != nil {
		if rec.claimNotify() {
			t.enqueueRecordingNotification(status)
		}
		return n, err
	}
	t.enqueueRecordingNotification(status)
	return n, nil
}

func (t *Tab) RecordingStatus() RecordingStatus {
	t.recMu.Lock()
	defer t.recMu.Unlock()
	return t.recordingStatusLocked()
}

func (t *Tab) recordingStatusLocked() RecordingStatus {
	if t.rec != nil {
		return t.rec.status(t.id)
	}
	if t.recStopped.State == RecordingStopped {
		return t.recStopped
	}
	return RecordingStatus{TabID: t.id, State: RecordingNeverStarted}
}

func (t *Tab) SetRecordingHandler(handler func(RecordingStatus)) {
	t.recordingHandler.Store(recordingHandler{fn: handler})
}

func (t *Tab) loadRecordingHandler() func(RecordingStatus) {
	if h, _ := t.recordingHandler.Load().(recordingHandler); h.fn != nil {
		return h.fn
	}
	return nil
}

func (t *Tab) RecordingBytes() uint64 {
	t.recMu.Lock()
	rec := t.rec
	t.recMu.Unlock()
	if rec == nil {
		return 0
	}
	written, _, _ := rec.counts()
	return written
}

func (t *Tab) recordChunk(p []byte) {
	if len(p) == 0 {
		return
	}
	t.recMu.Lock()
	rec := t.rec
	t.recMu.Unlock()
	if rec == nil {
		return
	}
	rec.enqueue(p)
}

// enqueueRecordingNotification appends a lifecycle status to the tab's
// ordered notification queue. Statuses are appended in state-transition
// order (under recOpMu); delivery happens in that same order.
func (t *Tab) enqueueRecordingNotification(status RecordingStatus) {
	t.notifyMu.Lock()
	t.notifyQueue = append(t.notifyQueue, status)
	t.notifyMu.Unlock()
}

// drainRecordingNotifications delivers queued statuses to the handler in
// order, one drainer at a time. The drainer is the outermost emitter: a
// handler that reenters Start/Stop only appends behind the events already
// queued, so nested operations never jump ahead of pending notifications.
// Handlers run without notifyMu held, so reentry is safe.
func (t *Tab) drainRecordingNotifications() {
	t.notifyMu.Lock()
	if t.notifyDraining || len(t.notifyQueue) == 0 {
		t.notifyMu.Unlock()
		return
	}
	t.notifyDraining = true
	t.notifyMu.Unlock()
	for {
		t.notifyMu.Lock()
		if len(t.notifyQueue) == 0 {
			t.notifyDraining = false
			t.notifyMu.Unlock()
			return
		}
		status := t.notifyQueue[0]
		t.notifyQueue[0] = RecordingStatus{}
		t.notifyQueue = t.notifyQueue[1:]
		t.notifyMu.Unlock()
		if handler := t.loadRecordingHandler(); handler != nil {
			handler(status)
		}
	}
}
