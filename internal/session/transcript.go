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
	SessionOutput(sessionID, tabID string, data []byte)
	SessionEnded(sessionID string)
}

type TranscriptSinkFuncs struct {
	StartedFunc func(ctx context.Context, info TranscriptInfo)
	OutputFunc  func(sessionID, tabID string, data []byte)
	EndedFunc   func(sessionID string)
}

func (f TranscriptSinkFuncs) SessionStarted(ctx context.Context, info TranscriptInfo) {
	if f.StartedFunc != nil {
		f.StartedFunc(ctx, info)
	}
}

func (f TranscriptSinkFuncs) SessionOutput(sessionID, tabID string, data []byte) {
	if f.OutputFunc != nil {
		f.OutputFunc(sessionID, tabID, data)
	}
}

func (f TranscriptSinkFuncs) SessionEnded(sessionID string) {
	if f.EndedFunc != nil {
		f.EndedFunc(sessionID)
	}
}

func (m *Manager) transcriptStarted(ctx context.Context, session *Session) {
	if m.transcripts == nil {
		return
	}
	m.transcripts.SessionStarted(ctx, TranscriptInfo{
		SessionID: session.ID,
		AssetID:   session.asset.ID,
		AssetName: session.asset.Name,
		AssetKind: session.asset.Kind,
	})
}

func (m *Manager) transcriptOutput(tab *Tab, data []byte) {
	if m.transcripts == nil {
		return
	}
	m.transcripts.SessionOutput(tab.SessionID, tab.ID, data)
}

func (m *Manager) transcriptEnded(sessionID string) {
	if m.transcripts == nil {
		return
	}
	m.transcripts.SessionEnded(sessionID)
}
