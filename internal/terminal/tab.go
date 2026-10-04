package terminal

import (
	"errors"
	"fmt"
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

type recording struct {
	file  *os.File
	bytes uint64
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

	recMu sync.Mutex
	rec   *recording

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
	ringBytes    int
	historyLines int
	onResponse   func([]byte)
}

type TabOption func(*tabConfig)

func WithClock(clock func() time.Time) TabOption {
	return func(c *tabConfig) { c.clock = clock }
}

func WithScrollbackBytes(n int) TabOption {
	return func(c *tabConfig) { c.ringBytes = n }
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
		ringBytes:    ScrollbackBytes,
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
		ring:      NewRing(cfg.ringBytes),
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

func (t *Tab) ReplayFrom(seq uint64, max int) (data []byte, start uint64) {
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
	defer t.recMu.Unlock()
	t.stopRecordingLocked()
	t.rec = &recording{file: file}
	return nil
}

func (t *Tab) StopRecording() uint64 {
	t.recMu.Lock()
	defer t.recMu.Unlock()
	return t.stopRecordingLocked()
}

func (t *Tab) stopRecordingLocked() uint64 {
	if t.rec == nil {
		return 0
	}
	_ = t.rec.file.Close()
	n := t.rec.bytes
	t.rec = nil
	return n
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
	defer t.recMu.Unlock()
	if t.rec == nil || len(p) == 0 {
		return
	}
	if _, err := t.rec.file.Write(p); err == nil {
		t.rec.bytes += uint64(len(p))
	}
}
