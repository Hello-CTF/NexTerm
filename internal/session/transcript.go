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
	SessionInput(ctx context.Context, sessionID, tabID string, data []byte)
	SessionResize(ctx context.Context, sessionID, tabID string, cols, rows uint32)
	SessionEnded(ctx context.Context, sessionID string)
}

type TranscriptSinkFuncs struct {
	StartedFunc func(ctx context.Context, info TranscriptInfo)
	OutputFunc  func(ctx context.Context, durableID, sessionID, tabID string, data []byte)
	InputFunc   func(ctx context.Context, sessionID, tabID string, data []byte)
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

func (f TranscriptSinkFuncs) SessionInput(ctx context.Context, sessionID, tabID string, data []byte) {
	if f.InputFunc != nil {
		f.InputFunc(ctx, sessionID, tabID, data)
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
// or notices, so it also drives the observed attention state.
func (m *Manager) transcriptOutput(ctx context.Context, tab *Tab, data []byte) {
	m.observeAttentionOutput(tab, data)
	if m.transcripts == nil {
		return
	}
	tab.mu.Lock()
	remaining := tab.catchUpRemaining
	catchUpDone := false
	if remaining > 0 {
		if int64(len(data)) <= remaining {
			tab.catchUpRemaining = remaining - int64(len(data))
			tab.mu.Unlock()
			return
		}
		tab.catchUpRemaining = 0
		data = data[remaining:]
		catchUpDone = true
	}
	durableID := ""
	if tab.durable != nil {
		durableID = tab.ID
	}
	tab.mu.Unlock()
	if catchUpDone {
		m.logger.Info("durable transcript catch-up complete", "tab", tab.ID)
	}
	if len(data) == 0 {
		return
	}
	m.transcripts.SessionOutput(ownerCtx(ctx, tab.session.userID), durableID, tab.SessionID, tab.ID, data)
}

func (m *Manager) transcriptEnded(session *Session) {
	if m.transcripts == nil {
		return
	}
	m.transcripts.SessionEnded(ownerCtx(m.ctx, session.userID), session.ID)
}

// transcriptInput receives every input written to the session.
func (m *Manager) transcriptInput(ctx context.Context, tab *Tab, data []byte) {
	if len(data) > 0 {
		m.observeAttentionInput(tab)
	}
	if m.transcripts == nil || len(data) == 0 {
		return
	}
	m.transcripts.SessionInput(ownerCtx(ctx, tab.session.userID), tab.SessionID, tab.ID, data)
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

func (m *Manager) durableTranscriptCatchUpBytes(ctx context.Context, tabID string) int64 {
	if m.offsets == nil {
		return 0
	}
	return m.offsets.DurableTranscriptCatchUpBytesContext(ctx, tabID)
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

func (m *Manager) durableTabTitle(ctx context.Context, tabID string) string {
	if m.titles == nil {
		return ""
	}
	return m.titles.DurableTabTitleContext(ctx, tabID)
}

func (m *Manager) durableTabTitleChangedAt(ctx context.Context, tabID string) int64 {
	if times, ok := m.titles.(DurableTabTitleTimeStore); ok {
		return times.DurableTabTitleChangedAtContext(ctx, tabID)
	}
	return 0
}

func (m *Manager) deleteDurableTabTitle(ctx context.Context, tabID string) {
	if m.titles != nil {
		m.titles.DeleteDurableTabTitleContext(ctx, tabID)
	}
}
