package terminal

import (
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
}

type recording struct {
	file            io.WriteCloser
	bytes           uint64
	err             error
	path            string
	generation      uint64
	failureNotified bool
}

type Tab struct {
	id        string
	sessionID string

	cols atomic.Int64
	rows atomic.Int64

	visible    atomic.Bool
	lastOutput atomic.Int64
	closed     atomic.Bool

	mu    sync.Mutex
	ring  *Ring
	tr    *Transcoder
	modes *ModeTracker
	scr   *screen
	state State

	recMu            sync.Mutex
	rec              *recording
	recGeneration    uint64
	recStopped       RecordingStatus
	recordingHandler func(RecordingStatus)

	resizeMu        sync.Mutex
	responseHandler atomic.Value
	resizeHandler   atomic.Value

	now func() time.Time

	closeOnce sync.Once
}

type responseHandler struct{ fn func([]byte) }
type resizeHandler struct{ fn func(cols, rows int) }

type tabConfig struct {
	clock        func() time.Time
	historyLines int
	onResponse   func([]byte)
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
		t.closed.Store(true)
		t.mu.Unlock()
	})
}

func (t *Tab) Feed(raw []byte) {
	if len(raw) == 0 || t.closed.Load() {
		return
	}
	t.lastOutput.Store(t.now().UnixMilli())
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed.Load() {
		return
	}
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
	if responses := t.scr.takeResponses(); len(responses) > 0 {
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

func (t *Tab) SwitchEncoding(enc Encoding) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tr.Switch(enc)
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
	t.scr.resize(cols, rows)
	t.cols.Store(int64(cols))
	t.rows.Store(int64(rows))
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
	}
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
	t.recMu.Lock()
	var notifications []RecordingStatus
	if t.rec != nil && t.rec.err == nil {
		_, err, notification := t.stopRecordingLocked()
		if notification != nil {
			notifications = append(notifications, *notification)
		}
		if err != nil {
			handler := t.recordingHandler
			t.recMu.Unlock()
			_ = file.Close()
			t.emitRecording(handler, notifications...)
			return err
		}
	}
	t.recGeneration++
	t.rec = &recording{file: file, path: path, generation: t.recGeneration}
	notifications = append(notifications, t.recordingStatusLocked())
	handler := t.recordingHandler
	t.recMu.Unlock()
	t.emitRecording(handler, notifications...)
	return nil
}

func (t *Tab) StopRecording() (uint64, error) {
	t.recMu.Lock()
	n, err, notification := t.stopRecordingLocked()
	handler := t.recordingHandler
	t.recMu.Unlock()
	if notification != nil {
		t.emitRecording(handler, *notification)
	}
	return n, err
}

func (t *Tab) stopRecordingLocked() (uint64, error, *RecordingStatus) {
	if t.rec == nil {
		return 0, nil, nil
	}
	if t.rec.err != nil {
		if t.rec.failureNotified {
			return t.rec.bytes, t.rec.err, nil
		}
		t.rec.failureNotified = true
		status := t.recordingStatusLocked()
		return t.rec.bytes, t.rec.err, &status
	}
	if err := t.rec.file.Close(); err != nil {
		t.rec.err = err
		t.rec.failureNotified = true
		status := t.recordingStatusLocked()
		return t.rec.bytes, err, &status
	}
	n := t.rec.bytes
	status := t.recordingStatusLocked()
	status.State = RecordingStopped
	t.recStopped = status
	t.rec = nil
	return n, nil, &status
}

func (t *Tab) RecordingStatus() RecordingStatus {
	t.recMu.Lock()
	defer t.recMu.Unlock()
	return t.recordingStatusLocked()
}

func (t *Tab) recordingStatusLocked() RecordingStatus {
	if t.rec != nil {
		status := RecordingStatus{
			TabID: t.id, State: RecordingRunning, Bytes: t.rec.bytes,
			Path: t.rec.path, Generation: t.rec.generation,
		}
		if t.rec.err != nil {
			status.State = RecordingFailed
			status.SafeError = t.rec.err.Error()
		}
		return status
	}
	if t.recStopped.State == RecordingStopped {
		return t.recStopped
	}
	return RecordingStatus{TabID: t.id, State: RecordingNeverStarted}
}

func (t *Tab) SetRecordingHandler(handler func(RecordingStatus)) {
	t.recMu.Lock()
	t.recordingHandler = handler
	t.recMu.Unlock()
}

func (t *Tab) RecordingBytes() uint64 {
	t.recMu.Lock()
	defer t.recMu.Unlock()
	if t.rec == nil {
		return 0
	}
	return t.rec.bytes
}

func (t *Tab) recordChunk(p []byte) {
	t.recMu.Lock()
	if t.rec == nil || t.rec.err != nil || len(p) == 0 {
		t.recMu.Unlock()
		return
	}
	n, err := t.rec.file.Write(p)
	t.rec.bytes += uint64(n)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	if err == nil {
		t.recMu.Unlock()
		return
	}
	t.rec.err = err
	_ = t.rec.file.Close()
	t.rec.failureNotified = true
	status := t.recordingStatusLocked()
	handler := t.recordingHandler
	t.recMu.Unlock()
	t.emitRecording(handler, status)
}

func (t *Tab) emitRecording(handler func(RecordingStatus), statuses ...RecordingStatus) {
	if handler == nil {
		return
	}
	for _, status := range statuses {
		handler(status)
	}
}
