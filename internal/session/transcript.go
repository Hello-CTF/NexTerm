package session

import (
	"context"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// A tab is recorded while a client watches it on a connected session: each
// such stretch is one recording. When the last client leaves, the recording
// ends after recordingGrace, so a page reload or a quick workspace switch
// keeps it going; a disconnect or a closed tab ends it at once. The next time
// a client watches the tab, a new recording starts with the screen as it
// stands, so a replay never starts from a blank screen.
const recordingGrace = time.Minute

// recordingScreenBytes bounds the screen state a recording starts with: the
// tail of the tab's output that redraws its screen, like an attach replay.
const recordingScreenBytes = 256 << 10

type TranscriptInfo struct {
	SessionID string
	TabID     string
	TabTitle  string
	AssetID   string
	AssetName string
	AssetKind string
}

// TranscriptSink receives the recordings of watched tabs.
type TranscriptSink interface {
	// RecordingStarted opens a tab's recording with its size and screen.
	RecordingStarted(ctx context.Context, info TranscriptInfo, cols, rows uint32, screen []byte)
	// RecordingScreen redraws the screen inside an open recording after
	// output went unrecorded while no client watched.
	RecordingScreen(ctx context.Context, sessionID, tabID string, cols, rows uint32, screen []byte)
	RecordingOutput(ctx context.Context, sessionID, tabID string, data []byte)
	RecordingResize(ctx context.Context, sessionID, tabID string, cols, rows uint32)
	RecordingEnded(ctx context.Context, sessionID, tabID string)
}

type TranscriptSinkFuncs struct {
	StartedFunc func(ctx context.Context, info TranscriptInfo, cols, rows uint32, screen []byte)
	ScreenFunc  func(ctx context.Context, sessionID, tabID string, cols, rows uint32, screen []byte)
	OutputFunc  func(ctx context.Context, sessionID, tabID string, data []byte)
	ResizeFunc  func(ctx context.Context, sessionID, tabID string, cols, rows uint32)
	EndedFunc   func(ctx context.Context, sessionID, tabID string)
}

func (f TranscriptSinkFuncs) RecordingStarted(ctx context.Context, info TranscriptInfo, cols, rows uint32, screen []byte) {
	if f.StartedFunc != nil {
		f.StartedFunc(ctx, info, cols, rows, screen)
	}
}

func (f TranscriptSinkFuncs) RecordingScreen(ctx context.Context, sessionID, tabID string, cols, rows uint32, screen []byte) {
	if f.ScreenFunc != nil {
		f.ScreenFunc(ctx, sessionID, tabID, cols, rows, screen)
	}
}

func (f TranscriptSinkFuncs) RecordingOutput(ctx context.Context, sessionID, tabID string, data []byte) {
	if f.OutputFunc != nil {
		f.OutputFunc(ctx, sessionID, tabID, data)
	}
}

func (f TranscriptSinkFuncs) RecordingResize(ctx context.Context, sessionID, tabID string, cols, rows uint32) {
	if f.ResizeFunc != nil {
		f.ResizeFunc(ctx, sessionID, tabID, cols, rows)
	}
}

func (f TranscriptSinkFuncs) RecordingEnded(ctx context.Context, sessionID, tabID string) {
	if f.EndedFunc != nil {
		f.EndedFunc(ctx, sessionID, tabID)
	}
}

// tabRecording is the recording state of a tab, guarded by its mu.
type tabRecording struct {
	// open is set while the tab has a recording.
	open bool
	// missed is set when output went unrecorded inside the open recording,
	// so the screen is redrawn when a client watches again.
	missed bool
	// timer ends the recording after the grace period; epoch tells a timer
	// that fired after the state moved on that it is stale.
	timer *time.Timer
	epoch uint64
}

// watchedLocked reports whether the tab is to be recorded now. The caller
// holds session.mu and t.mu.
func (t *Tab) watchedLocked() bool {
	return t.session.status == StatusConnected && !t.session.closed &&
		!t.closed && !t.destroying && t.hasInteractiveSubscriberLocked()
}

// recordingWatched runs once a client watches the tab, with the feed gate
// held so the screen the recording starts with precedes any later output.
func (m *Manager) recordingWatched(ctx context.Context, tab *Tab) {
	if m.transcripts == nil {
		return
	}
	session := tab.session
	session.mu.Lock()
	tab.mu.Lock()
	if !tab.watchedLocked() {
		tab.mu.Unlock()
		session.mu.Unlock()
		return
	}
	rec := &tab.recording
	rec.epoch++
	if rec.timer != nil {
		rec.timer.Stop()
		rec.timer = nil
	}
	start, redraw := !rec.open, rec.open && rec.missed
	rec.open, rec.missed = true, false
	info := TranscriptInfo{
		SessionID: session.ID, TabID: tab.ID, TabTitle: tab.title,
		AssetID: session.asset.ID, AssetName: session.asset.Name, AssetKind: session.asset.Kind,
	}
	cols, rows := tab.cols, tab.rows
	tab.mu.Unlock()
	session.mu.Unlock()
	if !start && !redraw {
		return
	}
	screen := append(append([]byte(nil), replayClear...), tab.terminal.Dump(recordingScreenBytes)...)
	sinkCtx := ownerCtx(context.WithoutCancel(ctx), session.userID)
	if start {
		m.transcripts.RecordingStarted(sinkCtx, info, cols, rows, screen)
		return
	}
	m.transcripts.RecordingScreen(sinkCtx, session.ID, tab.ID, cols, rows, screen)
}

// recordingLeft runs after a client left the tab: with nobody watching, the
// recording ends after the grace period.
func (m *Manager) recordingLeft(tab *Tab) {
	if m.transcripts == nil {
		return
	}
	session := tab.session
	session.mu.Lock()
	tab.mu.Lock()
	rec := &tab.recording
	if !rec.open || tab.watchedLocked() || rec.timer != nil {
		tab.mu.Unlock()
		session.mu.Unlock()
		return
	}
	rec.epoch++
	epoch := rec.epoch
	rec.timer = time.AfterFunc(recordingGrace, func() { m.recordingGraceEnded(tab, epoch) })
	tab.mu.Unlock()
	session.mu.Unlock()
}

func (m *Manager) recordingGraceEnded(tab *Tab, epoch uint64) {
	session := tab.session
	session.mu.Lock()
	tab.mu.Lock()
	rec := &tab.recording
	if rec.epoch != epoch || !rec.open || tab.watchedLocked() {
		tab.mu.Unlock()
		session.mu.Unlock()
		return
	}
	rec.open, rec.missed, rec.timer = false, false, nil
	rec.epoch++
	tab.mu.Unlock()
	session.mu.Unlock()
	m.transcripts.RecordingEnded(ownerCtx(m.ctx, session.userID), session.ID, tab.ID)
}

// recordingStop ends the tab's recording now, for a closed tab or a lost
// connection.
func (m *Manager) recordingStop(tab *Tab) {
	if m.transcripts == nil {
		return
	}
	tab.mu.Lock()
	ended := tab.recording.stopLocked()
	tab.mu.Unlock()
	if ended {
		m.transcripts.RecordingEnded(ownerCtx(m.ctx, tab.session.userID), tab.SessionID, tab.ID)
	}
}

func (rec *tabRecording) stopLocked() bool {
	rec.epoch++
	if rec.timer != nil {
		rec.timer.Stop()
		rec.timer = nil
	}
	ended := rec.open
	rec.open, rec.missed = false, false
	return ended
}

// transcriptStarted runs when the session connected: tabs clients already
// watch start recording.
func (m *Manager) transcriptStarted(ctx context.Context, session *Session) {
	if m.transcripts == nil {
		return
	}
	session.mu.Lock()
	tabs := make([]*Tab, 0, len(session.tabs))
	for _, tab := range session.tabs {
		tabs = append(tabs, tab)
	}
	session.mu.Unlock()
	for _, tab := range tabs {
		if err := tab.lockFeed(ctx); err != nil {
			continue
		}
		m.recordingWatched(ctx, tab)
		tab.unlockFeed()
	}
}

// transcriptEnded runs when the session lost its connection: every recording
// of its tabs ends. A reconnect starts new ones for the tabs still watched.
func (m *Manager) transcriptEnded(session *Session) {
	if m.transcripts == nil {
		return
	}
	session.mu.Lock()
	var ended []string
	for _, tab := range session.tabs {
		tab.mu.Lock()
		if tab.recording.stopLocked() {
			ended = append(ended, tab.ID)
		}
		tab.mu.Unlock()
	}
	session.mu.Unlock()
	ctx := ownerCtx(m.ctx, session.userID)
	for _, tabID := range ended {
		m.transcripts.RecordingEnded(ctx, session.ID, tabID)
	}
}

// transcriptOutput receives the session's own output, never replay preludes
// or notices, so it also drives the observed attention state. Only output a
// client is watching is recorded; the history a recovered durable tab
// replays is history and is left out.
func (m *Manager) transcriptOutput(ctx context.Context, tab *Tab, data []byte) {
	m.observeAttentionOutput(tab, data)
	if m.transcripts == nil {
		return
	}
	tab.mu.Lock()
	replayEnded := false
	if skip := min(int64(len(data)), tab.transcriptSkip); skip > 0 {
		tab.transcriptSkip -= skip
		data = data[skip:]
		replayEnded = tab.transcriptSkip == 0
	}
	watched := tab.hasInteractiveSubscriberLocked()
	// A recording opened before a recovered tab replayed its history started
	// from an empty screen: once the replay is fed, it gets the screen the
	// replay restored, which already holds the rest of this chunk.
	redraw := replayEnded && tab.recording.open && watched
	record := !redraw && len(data) > 0 && tab.recording.open
	if record && !watched {
		tab.recording.missed = true
		record = false
	}
	cols, rows := tab.cols, tab.rows
	tab.mu.Unlock()
	sinkCtx := ownerCtx(ctx, tab.session.userID)
	if redraw {
		screen := append(append([]byte(nil), replayClear...), tab.terminal.Dump(recordingScreenBytes)...)
		m.transcripts.RecordingScreen(sinkCtx, tab.SessionID, tab.ID, cols, rows, screen)
		return
	}
	if record {
		m.transcripts.RecordingOutput(sinkCtx, tab.SessionID, tab.ID, data)
	}
}

// transcriptNotice records a notice into the tab's open recording.
func (m *Manager) transcriptNotice(ctx context.Context, tab *Tab, notice []byte) {
	if m.transcripts == nil {
		return
	}
	tab.mu.Lock()
	record := tab.recording.open && tab.hasInteractiveSubscriberLocked()
	tab.mu.Unlock()
	if record {
		m.transcripts.RecordingOutput(ownerCtx(ctx, tab.session.userID), tab.SessionID, tab.ID, notice)
	}
}

// noteInput receives every input written to the session. Input is not
// recorded: nothing replays it and it can carry passwords.
func (m *Manager) noteInput(tab *Tab, data []byte) {
	if len(data) > 0 {
		m.observeAttentionInput(tab)
	}
}

func (m *Manager) transcriptResize(ctx context.Context, tab *Tab, cols, rows uint32) {
	if m.transcripts == nil {
		return
	}
	tab.mu.Lock()
	record := tab.recording.open
	tab.mu.Unlock()
	if record {
		m.transcripts.RecordingResize(ownerCtx(ctx, tab.session.userID), tab.SessionID, tab.ID, cols, rows)
	}
}

// ownerCtx returns a context carrying the session owner's identity for
// owner-scoped durable stores and lifecycle output. Desktop and open
// deployments have no owner and keep the shared store behavior.
func ownerCtx(parent context.Context, userID string) context.Context {
	if userID == "" {
		return parent
	}
	return ipc.WithUserID(parent, userID)
}
