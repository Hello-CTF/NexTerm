package tools

import (
	"context"
	"math"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/session"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type sessionTerminal struct {
	manager *session.Manager
}

func SessionTerminal(manager *session.Manager) Terminal {
	return &sessionTerminal{manager: manager}
}

// sessionRequestContext elevates an identity-less caller to open access only
// in trusted-open shapes: a desktop deployment, an explicitly open
// deployment, or an internal shared-state caller already marked by the app
// (owner.WithOpenAccess). Server account mode never elevates here, so a
// gateway request without an identity keeps requester-less reads denied
// (ErrForbidden) instead of reaching every tab.
func sessionRequestContext(ctx context.Context) context.Context {
	if _, ok := ipc.UserIDFromContext(ctx); ok {
		return ctx
	}
	if owner.OpenAccess(ctx) {
		return session.WithOpenAccess(ctx)
	}
	return ctx
}

func (s *sessionTerminal) Snapshot(ctx context.Context, tabID string) (Screen, error) {
	ctx = sessionRequestContext(ctx)
	snapshot, err := s.manager.TerminalSnapshotFor(ctx, tabID)
	if err != nil {
		return Screen{}, err
	}
	idle := int64(0)
	if snapshot.LastOutputMsAgo <= math.MaxInt64 {
		idle = int64(snapshot.LastOutputMsAgo)
	}
	tail, tailErr := s.manager.TailLinesFor(ctx, tabID, 30)
	if tailErr != nil {
		tail = snapshot.Lines
	}
	seq, seqErr := s.manager.ScreenSeqFor(ctx, tabID)
	if seqErr != nil {
		seq = 0
	}
	mode, modeErr := s.manager.TerminalModeStateFor(ctx, tabID)
	return Screen{Text: snapshot.Text, Tail: tail, CursorRow: snapshot.CursorRow, CursorCol: snapshot.CursorCol, Cols: snapshot.Cols, Rows: snapshot.Rows, IdleMS: idle, Seq: seq, AltScreen: snapshot.AltScreen, BracketedPaste: modeErr == nil && mode.BracketedPaste}, nil
}

func (s *sessionTerminal) Write(ctx context.Context, tabID string, data []byte) error {
	ctx = sessionRequestContext(ctx)
	if _, err := s.manager.TabFor(ctx, tabID); err != nil {
		return err
	}
	return s.manager.WriteInternal(ctx, tabID, data)
}

func (s *sessionTerminal) WritePaste(ctx context.Context, tabID, text string) error {
	return s.manager.WritePasteFor(sessionRequestContext(ctx), tabID, text)
}

func (s *sessionTerminal) WaitSnapshot(ctx context.Context, tabID string) (Screen, error) {
	snapshot, tail, err := s.manager.TerminalWaitSnapshotFor(sessionRequestContext(ctx), tabID)
	if err != nil {
		return Screen{}, err
	}
	idle := int64(0)
	if snapshot.LastOutputMsAgo <= math.MaxInt64 {
		idle = int64(snapshot.LastOutputMsAgo)
	}
	return Screen{Text: snapshot.Text, Tail: tail, CursorRow: snapshot.CursorRow, CursorCol: snapshot.CursorCol, Cols: snapshot.Cols, Rows: snapshot.Rows, IdleMS: idle, AltScreen: snapshot.AltScreen}, nil
}

func (s *sessionTerminal) CommandState(ctx context.Context, tabID string) (CommandState, error) {
	tab, err := s.manager.TabFor(sessionRequestContext(ctx), tabID)
	if err != nil {
		return CommandState{}, err
	}
	return tab.CommandState(), nil
}

func (s *sessionTerminal) OutputSince(ctx context.Context, tabID string, seq uint64, maxBytes int) ([]byte, uint64, uint64, error) {
	data, start, latest, _, err := s.manager.OutputSinceFor(sessionRequestContext(ctx), tabID, seq, maxBytes)
	return data, start, latest, err
}

func (s *sessionTerminal) SessionAssetKind(ctx context.Context, sessionID string) (string, error) {
	session, err := s.manager.SessionFor(sessionRequestContext(ctx), sessionID)
	if err != nil {
		return "", err
	}
	return session.Asset().Kind, nil
}

func (s *sessionTerminal) SessionAssetID(ctx context.Context, sessionID string) (string, error) {
	session, err := s.manager.SessionFor(sessionRequestContext(ctx), sessionID)
	if err != nil {
		return "", err
	}
	return session.Asset().ID, nil
}

func (s *sessionTerminal) Create(ctx context.Context, sessionID string, options CreateTerminalOptions) (CreatedTab, error) {
	info, err := s.manager.CreateTabFor(sessionRequestContext(ctx), session.CreateTabOptions{
		SessionID: sessionID, Title: options.Title, Command: options.Command,
	})
	if err != nil {
		return CreatedTab{}, err
	}
	return CreatedTab{TabID: info.ID, Title: info.Title}, nil
}

func (s *sessionTerminal) Rename(ctx context.Context, tabID, title string) error {
	_, err := s.manager.RenameTabFor(sessionRequestContext(ctx), tabID, title)
	return err
}

func WithSession(deps Dependencies, manager *session.Manager) Dependencies {
	deps.Transport = func(ctx context.Context, sessionID string) (base.Transport, error) {
		return manager.TransportFor(sessionRequestContext(ctx), sessionID)
	}
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
