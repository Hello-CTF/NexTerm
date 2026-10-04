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
	SessionEnded(ctx context.Context, sessionID string)
}

type TranscriptSinkFuncs struct {
	StartedFunc func(ctx context.Context, info TranscriptInfo)
	OutputFunc  func(ctx context.Context, durableID, sessionID, tabID string, data []byte)
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
	if remaining > 0 {
		if int64(len(data)) <= remaining {
			tab.catchUpRemaining = remaining - int64(len(data))
			tab.mu.Unlock()
			return
		}
		tab.catchUpRemaining = 0
		data = data[remaining:]
	}
	durableID := ""
	if tab.durable != nil {
		durableID = tab.ID
	}
	tab.mu.Unlock()
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
