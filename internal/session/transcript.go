package session

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type TranscriptInfo struct {
	SessionID string
	AssetID   string
	AssetName string
	AssetKind string
}

type TranscriptSink interface {
	SessionStarted(ctx context.Context, info TranscriptInfo)
	SessionOutput(ctx context.Context, durableID, sessionID, tabID string, data []byte)
	SessionResize(ctx context.Context, sessionID, tabID string, cols, rows uint32)
	SessionEnded(ctx context.Context, sessionID string)
}

type TranscriptSinkFuncs struct {
	StartedFunc func(ctx context.Context, info TranscriptInfo)
	OutputFunc  func(ctx context.Context, durableID, sessionID, tabID string, data []byte)
	ResizeFunc  func(ctx context.Context, sessionID, tabID string, cols, rows uint32)
	EndedFunc   func(ctx context.Context, sessionID string)
}

func (f TranscriptSinkFuncs) SessionStarted(ctx context.Context, info TranscriptInfo) {
	if f.StartedFunc != nil {
		f.StartedFunc(ctx, info)
	}
}

func (f TranscriptSinkFuncs) SessionOutput(ctx context.Context, durableID, sessionID, tabID string, data []byte) {
	if f.OutputFunc != nil {
		f.OutputFunc(ctx, durableID, sessionID, tabID, data)
	}
}

func (f TranscriptSinkFuncs) SessionResize(ctx context.Context, sessionID, tabID string, cols, rows uint32) {
	if f.ResizeFunc != nil {
		f.ResizeFunc(ctx, sessionID, tabID, cols, rows)
	}
}

func (f TranscriptSinkFuncs) SessionEnded(ctx context.Context, sessionID string) {
	if f.EndedFunc != nil {
		f.EndedFunc(ctx, sessionID)
	}
}

func (m *Manager) transcriptStarted(ctx context.Context, session *Session) {
	if m.transcripts == nil {
		return
	}
	m.transcripts.SessionStarted(ownerCtx(context.WithoutCancel(ctx), session.userID), TranscriptInfo{
		SessionID: session.ID,
		AssetID:   session.asset.ID,
		AssetName: session.asset.Name,
		AssetKind: session.asset.Kind,
	})
}

// transcriptOutput receives the session's own output, never replay preludes
// or notices, so it also drives the observed attention state. Only output a
// client is watching is recorded: output produced while the tab is in the
// background, and the history a recovered durable tab replays, is left out
// and marked by one notice when recording resumes.
func (m *Manager) transcriptOutput(ctx context.Context, tab *Tab, data []byte) {
	m.observeAttentionOutput(tab, data)
	if m.transcripts == nil {
		return
	}
	tab.mu.Lock()
	if skip := min(int64(len(data)), tab.transcriptSkip); skip > 0 {
		tab.transcriptSkip -= skip
		data = data[skip:]
	}
	if len(data) > 0 && !tab.hasInteractiveSubscriberLocked() {
		tab.unrecorded += int64(len(data))
		data = nil
	}
	var unrecorded int64
	if len(data) > 0 {
		unrecorded, tab.unrecorded = tab.unrecorded, 0
	}
	durableID := ""
	if tab.durable != nil {
		durableID = tab.ID
	}
	tab.mu.Unlock()
	if len(data) == 0 {
		return
	}
	m.transcriptUnrecorded(ctx, tab, durableID, unrecorded)
	m.transcripts.SessionOutput(ownerCtx(ctx, tab.session.userID), durableID, tab.SessionID, tab.ID, data)
}

// flushUnrecorded writes the notice for output left out of the transcript once
// a client watches the tab again.
func (m *Manager) flushUnrecorded(ctx context.Context, tab *Tab) {
	if m.transcripts == nil {
		return
	}
	tab.mu.Lock()
	if !tab.hasInteractiveSubscriberLocked() || tab.unrecorded == 0 {
		tab.mu.Unlock()
		return
	}
	skipped := tab.unrecorded
	tab.unrecorded = 0
	durableID := ""
	if tab.durable != nil {
		durableID = tab.ID
	}
	tab.mu.Unlock()
	m.transcriptUnrecorded(ctx, tab, durableID, skipped)
}

func (m *Manager) transcriptUnrecorded(ctx context.Context, tab *Tab, durableID string, skipped int64) {
	if sink, ok := m.transcripts.(TranscriptUnrecordedSink); ok && skipped > 0 {
		sink.SessionOutputUnrecorded(ownerCtx(ctx, tab.session.userID), durableID, tab.SessionID, tab.ID, skipped)
	}
}

// TranscriptUnrecordedSink is implemented by transcript sinks that mark where
// output was left out of the transcript; a durable tab's recorded offset
// advances past it so it keeps matching the helper's absolute offsets.
type TranscriptUnrecordedSink interface {
	SessionOutputUnrecorded(ctx context.Context, durableID, sessionID, tabID string, skipped int64)
}

func (m *Manager) transcriptEnded(session *Session) {
	if m.transcripts == nil {
		return
	}
	// A regular terminal's next connection runs a new shell: output its
	// previous one produced unwatched is not that period's to report.
	session.mu.Lock()
	for _, tab := range session.tabs {
		tab.mu.Lock()
		if tab.durable == nil {
			tab.unrecorded = 0
		}
		tab.mu.Unlock()
	}
	session.mu.Unlock()
	m.transcripts.SessionEnded(ownerCtx(m.ctx, session.userID), session.ID)
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
	m.transcripts.SessionResize(ownerCtx(ctx, tab.session.userID), tab.SessionID, tab.ID, cols, rows)
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

func (m *Manager) durableTranscriptOffset(ctx context.Context, tabID string) int64 {
	if m.offsets == nil {
		return 0
	}
	return m.offsets.DurableTranscriptOffsetContext(ctx, tabID)
}

func (m *Manager) persistDurableTranscriptOffset(ctx context.Context, tabID string, offset int64) {
	if m.offsets != nil {
		m.offsets.PersistDurableTranscriptOffsetContext(ctx, tabID, offset)
	}
}

func (m *Manager) deleteDurableTranscriptOffset(ctx context.Context, tabID string) {
	if m.offsets != nil {
		m.offsets.DeleteDurableTranscriptOffsetContext(ctx, tabID)
	}
}
