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

func (m *Manager) transcriptOutput(ctx context.Context, tab *Tab, data []byte) {
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

func (m *Manager) transcriptInput(ctx context.Context, tab *Tab, data []byte) {
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

// failClosedStoreWarn notes a legacy adapter that cannot scope by owner on a
// non-open server: the operation degrades to its zero value instead of
// running ownerless.
func (m *Manager) failClosedStoreWarn(tabID string, operation string) {
	m.logger.Warn("legacy durable store on a non-open server; failing closed instead of ownerless",
		"tab", tabID, "operation", operation)
}

func (m *Manager) durableTranscriptCatchUpBytes(ctx context.Context, tabID string) int64 {
	if m.offsets == nil {
		return 0
	}
	if store, ok := m.offsets.(OwnerScopedDurableTranscriptOffsetStore); ok {
		return store.DurableTranscriptCatchUpBytesContext(ctx, tabID)
	}
	if m.openAccess {
		return m.offsets.DurableTranscriptCatchUpBytes(tabID)
	}
	m.failClosedStoreWarn(tabID, "catch-up read")
	return 0
}

func (m *Manager) persistDurableTranscriptOffset(ctx context.Context, tabID string, offset int64) {
	if m.offsets == nil {
		return
	}
	if store, ok := m.offsets.(OwnerScopedDurableTranscriptOffsetStore); ok {
		store.PersistDurableTranscriptOffsetContext(ctx, tabID, offset)
		return
	}
	if m.openAccess {
		m.offsets.PersistDurableTranscriptOffset(tabID, offset)
		return
	}
	m.failClosedStoreWarn(tabID, "offset persist")
}

func (m *Manager) deleteDurableTranscriptOffset(ctx context.Context, tabID string) {
	if m.offsets == nil {
		return
	}
	if store, ok := m.offsets.(OwnerScopedDurableTranscriptOffsetStore); ok {
		store.DeleteDurableTranscriptOffsetContext(ctx, tabID)
		return
	}
	if m.openAccess {
		m.offsets.DeleteDurableTranscriptOffset(tabID)
		return
	}
	m.failClosedStoreWarn(tabID, "offset delete")
}

func (m *Manager) durableTabTitle(ctx context.Context, tabID string) string {
	if m.titles == nil {
		return ""
	}
	if store, ok := m.titles.(OwnerScopedDurableTabTitleStore); ok {
		return store.DurableTabTitleContext(ctx, tabID)
	}
	if m.openAccess {
		return m.titles.DurableTabTitle(tabID)
	}
	m.failClosedStoreWarn(tabID, "title read")
	return ""
}

func (m *Manager) persistDurableTabTitle(ctx context.Context, tabID, title string) {
	if m.titles == nil {
		return
	}
	if store, ok := m.titles.(OwnerScopedDurableTabTitleStore); ok {
		store.PersistDurableTabTitleContext(ctx, tabID, title)
		return
	}
	if m.openAccess {
		m.titles.PersistDurableTabTitle(tabID, title)
		return
	}
	m.failClosedStoreWarn(tabID, "title persist")
}

func (m *Manager) deleteDurableTabTitle(ctx context.Context, tabID string) {
	if m.titles == nil {
		return
	}
	if store, ok := m.titles.(OwnerScopedDurableTabTitleStore); ok {
		store.DeleteDurableTabTitleContext(ctx, tabID)
		return
	}
	if m.openAccess {
		m.titles.DeleteDurableTabTitle(tabID)
		return
	}
	m.failClosedStoreWarn(tabID, "title delete")
}
