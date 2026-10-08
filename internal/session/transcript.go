package session

import "context"

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
	m.transcripts.SessionStarted(context.WithoutCancel(ctx), TranscriptInfo{
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
	m.transcripts.SessionOutput(ctx, durableID, tab.SessionID, tab.ID, data)
}

func (m *Manager) transcriptEnded(sessionID string) {
	if m.transcripts == nil {
		return
	}
	m.transcripts.SessionEnded(m.ctx, sessionID)
}

func (m *Manager) transcriptInput(ctx context.Context, tab *Tab, data []byte) {
	if m.transcripts == nil || len(data) == 0 {
		return
	}
	m.transcripts.SessionInput(ctx, tab.SessionID, tab.ID, data)
}

func (m *Manager) transcriptResize(ctx context.Context, tab *Tab, cols, rows uint32) {
	if m.transcripts == nil {
		return
	}
	m.transcripts.SessionResize(ctx, tab.SessionID, tab.ID, cols, rows)
}

func (m *Manager) deleteDurableTranscriptOffset(tabID string) {
	if m.offsets == nil {
		return
	}
	m.offsets.DeleteDurableTranscriptOffset(tabID)
}
