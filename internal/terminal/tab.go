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

// DefaultTailDumpBytes bounds how much raw scrollback TailLines decodes.
const DefaultTailDumpBytes = 256 * 1024

// MaxTerminalDimension bounds accepted resize dimensions (1..1024).
const MaxTerminalDimension = 1024

// ErrClosed reports use of a closed Tab where an error can be returned.
var ErrClosed = errors.New("terminal: tab closed")

// State mirrors the terminal mode bits derived from the output stream.
type State struct {
	AltScreen         bool `json:"altScreen"`
	CursorVisible     bool `json:"cursorVisible"`
	BracketedPaste    bool `json:"bracketedPaste"`
	ApplicationCursor bool `json:"applicationCursor"`
}

// Snapshot is a structured screen read (AI takeover / UI screen panel).
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

// Tab is one terminal tab: a raw-byte ring, a VT screen, a transcoder and
// mode state. Transports feed output through Output (which also records
// and fans out); Feed alone only updates the tab state.
//
// Tab is safe for concurrent use. Close is idempotent; after it, Feed is
// a no-op and Resize returns ErrClosed.
type Tab struct {
	id        string
	sessionID string

	cols atomic.Int64
	rows atomic.Int64

	visible    atomic.Bool
	lastOutput atomic.Int64 // Unix milliseconds
	closed     atomic.Bool

	mu    sync.Mutex // guards ring, transcoder, modes, screen, state
	ring  *Ring
	tr    *Transcoder
	modes *ModeTracker
	scr   *screen
	state State

	recMu sync.Mutex
	rec   *recording

	resizeMu        sync.Mutex   // serializes Resize incl. the handler
	responseHandler atomic.Value // of responseHandler
	resizeHandler   atomic.Value // of resizeHandler

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

// TabOption customizes NewTab (mainly for tests).
type TabOption func(*tabConfig)

// WithClock overrides the wall clock used for idle/last-output accounting.
func WithClock(clock func() time.Time) TabOption {
	return func(c *tabConfig) { c.clock = clock }
}

// WithScrollbackBytes overrides the raw ring capacity.
func WithScrollbackBytes(n int) TabOption {
	return func(c *tabConfig) { c.ringBytes = n }
}

// WithScreenHistory overrides the VT scrollback line capacity.
func WithScreenHistory(n int) TabOption {
	return func(c *tabConfig) { c.historyLines = n }
}

// WithResponseHandler installs the initial terminal-reply handler.
func WithResponseHandler(h func([]byte)) TabOption {
	return func(c *tabConfig) { c.onResponse = h }
}

// NewTab creates a tab. Non-positive dimensions fall back to 80x24.
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

// ID returns the tab identifier.
func (t *Tab) ID() string { return t.id }

// SessionID returns the owning session identifier.
func (t *Tab) SessionID() string { return t.sessionID }

// Cols returns the current width in cells.
func (t *Tab) Cols() int { return int(t.cols.Load()) }

// Rows returns the current height in cells.
func (t *Tab) Rows() int { return int(t.rows.Load()) }

// SetVisible marks the frontend tab visible/hidden; hidden tabs get
// batched output via Output.
func (t *Tab) SetVisible(visible bool) { t.visible.Store(visible) }

// IsVisible reports the visibility flag.
func (t *Tab) IsVisible() bool { return t.visible.Load() }

// SetResponseHandler installs the callback receiving terminal-generated
// replies (cursor reports, device attributes). Transports must write
// these bytes back to the peer; ConPTY in particular stalls without
// cursor reports. The handler is invoked synchronously inside Feed, in
// Feed order: it must not call Tab methods and must not block
// indefinitely (queue inside the transport instead).
func (t *Tab) SetResponseHandler(h func([]byte)) {
	t.responseHandler.Store(responseHandler{fn: h})
}

// SetResizeHandler installs a callback invoked after a successful Resize
// so transports can forward the window change to the peer. Calls are
// serialized with Resize; the handler must not call Resize itself.
func (t *Tab) SetResizeHandler(h func(cols, rows int)) {
	t.resizeHandler.Store(resizeHandler{fn: h})
}

// Close marks the tab closed. It is idempotent. After Close, Feed becomes
// a no-op and Resize returns ErrClosed.
func (t *Tab) Close() {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed.Store(true)
		t.mu.Unlock()
	})
}

// Feed ingests raw peer output into the ring, the transcoder, the mode
// tracker and the VT screen. The ring keeps the original bytes; only the
// screen sees decoded UTF-8. Terminal query replies produced by the
// screen are delivered to the response handler before Feed returns.
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

// State returns the current mode bits.
func (t *Tab) State() State {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// SwitchEncoding changes the source encoding at runtime; buffered partial
// characters of the old encoding are dropped.
func (t *Tab) SwitchEncoding(enc Encoding) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tr.Switch(enc)
}

// Resize updates the screen grid, publishes the new dimensions and
// forwards them to the resize handler, in that order. The whole operation
// is serialized: concurrent Resize calls cannot leave the grid, the
// getters and the peer disagreeing. Valid dimensions are 1..1024 cells.
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

// ScreenText returns the visible screen as plain text (no ANSI residue).
func (t *Tab) ScreenText() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.scr.text()
}

// Snapshot returns a structured screen read.
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

// ScreenHistory returns the last n lines that scrolled off the main
// screen, oldest first (up to the configured history capacity).
func (t *Tab) ScreenHistory(n int) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.scr.historyLines(n)
}

// ScrollbackLen returns the current number of VT history lines.
func (t *Tab) ScrollbackLen() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.scr.scrollbackLen()
}

// splitScreenLines omits the trailing empty element after a final newline
// and trims trailing whitespace per line.
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

// TailLines returns the last n lines combining recent raw scrollback
// (decoded with the current encoding) and the visible screen.
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

// Dump returns the most recent maxBytes of raw scrollback (all retained
// bytes when maxBytes <= 0).
func (t *Tab) Dump(maxBytes int) []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ring.Dump(maxBytes)
}

// BaseSeq returns the sequence number of the oldest retained raw byte.
func (t *Tab) BaseSeq() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ring.Base()
}

// LatestSeq returns the sequence number after the newest raw byte.
func (t *Tab) LatestSeq() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ring.Total()
}

// DroppedBytes returns how many raw bytes were evicted by the ring limit.
func (t *Tab) DroppedBytes() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ring.Dropped()
}

// ReplayFrom returns up to max raw bytes from absolute sequence seq; see
// Ring.ReplayFrom for clamping semantics.
func (t *Tab) ReplayFrom(seq uint64, max int) (data []byte, start uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ring.ReplayFrom(seq, max)
}

// ExportLog writes raw scrollback (up to maxBytes, all when <= 0) to path
// and returns the number of bytes written.
func (t *Tab) ExportLog(path string, maxBytes int) (uint64, error) {
	data := t.Dump(maxBytes)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return 0, fmt.Errorf("terminal: export %s: %w", path, err)
	}
	return uint64(len(data)), nil
}

// IsIdle reports whether no output arrived for at least quiet.
func (t *Tab) IsIdle(quiet time.Duration) bool { return t.LastOutputAgo() >= quiet }

// LastOutputAgo returns how long ago the last output chunk arrived.
func (t *Tab) LastOutputAgo() time.Duration {
	ago := t.now().UnixMilli() - t.lastOutput.Load()
	if ago < 0 {
		ago = 0
	}
	return time.Duration(ago) * time.Millisecond
}

// StartRecording appends subsequent raw output (fed via Output) to path.
// An already active recording is flushed and closed first.
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

// StopRecording closes the recording and returns the number of bytes
// recorded during this recording session (0 when not recording).
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

// RecordingBytes returns the bytes recorded so far (0 when not recording).
func (t *Tab) RecordingBytes() uint64 {
	t.recMu.Lock()
	defer t.recMu.Unlock()
	if t.rec == nil {
		return 0
	}
	return t.rec.bytes
}

// recordChunk writes raw output to the active recording. Write errors are
// intentionally ignored (recording must never stall the terminal).
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
