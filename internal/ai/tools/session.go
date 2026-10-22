package tools

import (
	"context"
	"math"
	"time"

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

// sessionRequestContext passes the caller through unchanged: the session
// layer natively denies identity-less open access on non-open servers and
// scopes identified callers to their own resources. An app-marked
// (owner.WithOpenAccess) caller carrying an identity is vouched to open
// access; an identity-less mark is never elevated.
func sessionRequestContext(ctx context.Context) context.Context {
	if _, ok := ipc.UserIDFromContext(ctx); !ok {
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

// SessionAssetIdentity reports the immutable asset identity the session is
// currently bound to (asset ID plus config fingerprint), so mutation tools
// can re-validate a resolved identity at execution time.
func (s *sessionTerminal) SessionAssetIdentity(ctx context.Context, sessionID string) (AssetIdentity, error) {
	current, err := s.manager.SessionFor(sessionRequestContext(ctx), sessionID)
	if err != nil {
		return AssetIdentity{}, err
	}
	asset := current.Asset()
	return AssetIdentity{AssetID: asset.ID, ConfigFingerprint: asset.ConfigFingerprint}, nil
}

// ListTerminalTabs enumerates the tabs visible to the caller with the exact
// handle each is addressed by; the asset kind is resolved server-side so
// privacy decisions never depend on client-supplied fields.
func (s *sessionTerminal) ListTerminalTabs(ctx context.Context) ([]TerminalBrief, error) {
	ctx = sessionRequestContext(ctx)
	infos, err := s.manager.ListTabsFor(ctx)
	if err != nil {
		return nil, err
	}
	briefs := make([]TerminalBrief, 0, len(infos))
	for _, info := range infos {
		brief := TerminalBrief{
			Handle:    TerminalHandle{TabID: info.ID, SessionID: info.SessionID}.String(),
			TabID:     info.ID,
			SessionID: info.SessionID,
			Title:     info.Title,
			CWD:       info.Cwd,
			Durable:   info.Durable,
			Exited:    info.Exited,
		}
		if session, err := s.manager.SessionFor(ctx, info.SessionID); err == nil {
			asset := session.Asset()
			brief.AssetID = asset.ID
			brief.Kind = asset.Kind
		}
		briefs = append(briefs, brief)
	}
	return briefs, nil
}

// TabDetail reports the per-tab identity (kind, title, shell-reported cwd,
// lifecycle state) surfaced by read_screen headers. The title follows the
// session manager's custom-title precedence: a user/AI-set title wins over
// the live OSC 0/2 dynamic title, which surfaces only while no custom title
// is set.
func (s *sessionTerminal) TabDetail(ctx context.Context, tabID string) (TabDetail, error) {
	ctx = sessionRequestContext(ctx)
	tab, err := s.manager.TabFor(ctx, tabID)
	if err != nil {
		return TabDetail{}, err
	}
	info := tab.Info()
	detail := TabDetail{
		TabID:     info.ID,
		SessionID: info.SessionID,
		Title:     info.Title,
		CWD:       info.Cwd,
		Exited:    info.Exited,
		Durable:   info.Durable,
	}
	if detail.Title == "" {
		if snapshot, err := s.manager.TerminalSnapshotFor(ctx, tabID); err == nil {
			detail.Title = snapshot.Title
		}
	}
	if session, err := s.manager.SessionFor(ctx, info.SessionID); err == nil {
		detail.Kind = session.Asset().Kind
	}
	return detail, nil
}

// WritePasteLiteral writes text under the terminal's write lock with the
// bracketed-paste mode read inside that same lock: when the mode is off,
// text with line breaks is refused before any byte is written. force stays
// false — the tools layer's own pre-write guard already refuses that shape,
// so an in-lock refusal is a genuine mode flip and must surface.
func (s *sessionTerminal) WritePasteLiteral(ctx context.Context, tabID, text string) error {
	return s.manager.WritePasteLiteralFor(sessionRequestContext(ctx), tabID, text, false)
}

// WritePasteSubmit delegates to the session manager's combined paste-submit:
// the paste block, the submit delay, and the Enter key are written under one
// and the same terminal write lock, with the literal-newline guard enforced
// inside that lock, so send_keys never falls back to split writes on
// session-backed terminals.
func (s *sessionTerminal) WritePasteSubmit(ctx context.Context, tabID, text string, submit bool, delay time.Duration) error {
	return s.manager.WritePasteSubmitFor(sessionRequestContext(ctx), tabID, text, submit, delay)
}

// QuietGroupSnapshot delegates to the session manager's exact-roster group
// snapshot: the response carries exactly one state per requested tab — each
// with its version, closed fact, and creation incarnation from one versioned
// terminal read — and the roster is re-verified against the manager's
// current incarnations before returning, so a tab recreated under the same
// ID during the read fails the whole snapshot instead of silently adopting
// the new incarnation's state.
func (s *sessionTerminal) QuietGroupSnapshot(ctx context.Context, tabIDs []string) ([]TabQuietState, error) {
	states, err := s.manager.QuietGroupSnapshot(sessionRequestContext(ctx), tabIDs)
	if err != nil {
		return nil, err
	}
	mapped := make([]TabQuietState, 0, len(states))
	for _, state := range states {
		mapped = append(mapped, TabQuietState{
			TabID:       state.TabID,
			IdleMS:      state.IdleMS,
			Version:     state.Version,
			Closed:      state.Closed,
			Incarnation: state.Incarnation,
		})
	}
	return mapped, nil
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
