package tools

import (
	"context"
	"math"

	"github.com/ProbiusOfficial/NexTerm/internal/session"
)

type sessionTerminal struct {
	manager *session.Manager
}

func SessionTerminal(manager *session.Manager) Terminal {
	return &sessionTerminal{manager: manager}
}

func (s *sessionTerminal) Snapshot(ctx context.Context, tabID string) (Screen, error) {
	snapshot, err := s.manager.TerminalSnapshot(tabID)
	if err != nil {
		return Screen{}, err
	}
	idle := int64(0)
	if snapshot.LastOutputMsAgo <= math.MaxInt64 {
		idle = int64(snapshot.LastOutputMsAgo)
	}
	tail, tailErr := s.manager.TailLines(tabID, 30)
	if tailErr != nil {
		tail = snapshot.Lines
	}
	return Screen{Text: snapshot.Text, Tail: tail, CursorRow: snapshot.CursorRow, CursorCol: snapshot.CursorCol, IdleMS: idle}, nil
}

func (s *sessionTerminal) Write(ctx context.Context, tabID string, data []byte) error {
	return s.manager.WriteInternal(ctx, tabID, data)
}

func WithSession(deps Dependencies, manager *session.Manager) Dependencies {
	deps.Transport = manager.Transport
	deps.Terminal = SessionTerminal(manager)
	deps.TabSession = func(tabID string) string {
		tab, err := manager.Tab(tabID)
		if err != nil {
			return ""
		}
		return tab.SessionID
	}
	return deps
}
