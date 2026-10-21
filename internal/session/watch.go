package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// WatchStreamState is the watch-facing view of one tab's runtime episode and
// lifecycle. It reuses the roster same-source snapshot for the episode and
// adds the tab's stream epoch from the same locked read: the daemon
// incarnation of a durable tab survives a close/recover cycle, but the
// recovered tab is a brand-new stream view (new ring, new stream epoch, fresh
// absolute positions), and the epoch is what proves which stream a position
// belongs to.
type WatchStreamState struct {
	Episode     string
	EpisodeOK   bool
	StreamEpoch string
	Exited      bool
	Status      Status
}

// WatchIdentity is the identity a watch must bind to: the runtime episode
// qualified by the stream epoch. Binding to the bare episode would let a
// watch anchored in a closed tab's coordinates silently resume against the
// recovered tab's reset positions; the epoch changes exactly when the stream
// coordinate space does, so identity semantics stay honest.
func (s WatchStreamState) WatchIdentity() string {
	if !s.EpisodeOK {
		return ""
	}
	return s.Episode + "@" + s.StreamEpoch
}

// WatchStreamState returns the tab's current watch stream state. The boolean
// is false when the tab does not exist for the caller (wrong owner included).
func (m *Manager) WatchStreamState(ctx context.Context, tabID string) (WatchStreamState, bool) {
	snapshot, ok := m.RosterRuntimeSnapshot(ctx, tabID)
	if !ok {
		return WatchStreamState{}, false
	}
	tab, err := m.TabFor(ctx, tabID)
	if err != nil {
		return WatchStreamState{}, false
	}
	tab.mu.Lock()
	streamEpoch := tab.streamEpoch
	tab.mu.Unlock()
	return WatchStreamState{
		Episode: snapshot.Episode, EpisodeOK: snapshot.EpisodeOK,
		StreamEpoch: streamEpoch,
		Exited:      snapshot.Exited, Status: snapshot.Status,
	}, true
}

// WatchGapError reports that the tab's retained output before FromSeq rolled
// out of the scrollback: the stream honestly resumes at FromSeq, and a watch
// that needed the lost window can never verify it.
type WatchGapError struct {
	FromSeq int64
}

func (e *WatchGapError) Error() string {
	return fmt.Sprintf("session: watch stream output before %d is no longer retained", e.FromSeq)
}

// WatchEpisodeReplacedError reports that the tab's runtime episode advanced
// past the episode the source is bound to (a reconnect committed a
// replacement runtime): output produced by the replacement must never be
// attributed to the old episode.
type WatchEpisodeReplacedError struct {
	Current string
}

func (e *WatchEpisodeReplacedError) Error() string {
	return fmt.Sprintf("session: watch target runtime episode replaced (current %s)", e.Current)
}

// ErrWatchTargetGone reports that the watched tab no longer exists for the
// caller.
var ErrWatchTargetGone = errors.New("session: watch target is gone")

// ErrWatchEpisodeUnevidenced reports that the tab's runtime episode cannot be
// evidenced (a durable tab whose daemon supplied no incarnation): binding a
// watch to it would fail open, so the source refuses to open.
var ErrWatchEpisodeUnevidenced = errors.New("session: watch target episode cannot be evidenced")

// WatchSourceOptions tunes the source loop. Zero values pick defaults.
type WatchSourceOptions struct {
	// PollInterval is how often the source re-checks the stream end and the
	// episode while waiting for new output.
	PollInterval time.Duration
	// MaxChunkBytes bounds one returned chunk; more pending output is picked
	// up by the immediately following read.
	MaxChunkBytes int
}

func (o *WatchSourceOptions) withDefaults() {
	if o.PollInterval <= 0 {
		o.PollInterval = 200 * time.Millisecond
	}
	if o.MaxChunkBytes <= 0 {
		o.MaxChunkBytes = 256 << 10
	}
}

// watchSequenceReader is the absolute-sequence surface of a tab terminal,
// the same shape planAttachReplay consumes.
type watchSequenceReader interface {
	ReplayFrom(seq uint64, max int) (data []byte, start uint64, clamped bool)
	LatestSeq() uint64
}

// WatchSource is an absolute-position raw output stream over one tab's
// retained scrollback, bound to one watch identity (the runtime episode
// qualified by the stream epoch). It never invents bytes: positions come from
// the terminal sequence counter, a clipped read reports the honest resume
// position as *WatchGapError, a replaced stream (a reconnect's new runtime or
// a close/recover cycle's fresh coordinates) reports
// *WatchEpisodeReplacedError, and a tab that exited with no more retained
// output reports io.EOF.
type WatchSource struct {
	manager  *Manager
	tabID    string
	identity string
	reader   watchSequenceReader
	options  WatchSourceOptions
	// ctx carries the opener's owner identity without the request's
	// cancellation; state lookups use it, while each call's own ctx still
	// governs cancellation.
	ctx context.Context
}

// OpenWatchSource binds a watch stream to the tab's current watch identity.
// The identity must be the one from WatchStreamState.WatchIdentity: a
// mismatch means the caller's stream view is stale and the open fails instead
// of attributing a replaced stream's output. Terminals without sequence
// tracking cannot be watched and fail with ErrUnsupported. The source keeps
// polling under the opener's owner identity — the request context cancels
// with the call, but the attachment's credential is the owner that was
// verified here.
func (m *Manager) OpenWatchSource(ctx context.Context, tabID, identity string, options WatchSourceOptions) (*WatchSource, error) {
	options.withDefaults()
	if identity == "" {
		return nil, ErrWatchEpisodeUnevidenced
	}
	state, ok := m.WatchStreamState(ctx, tabID)
	if !ok {
		return nil, ErrWatchTargetGone
	}
	if !state.EpisodeOK {
		return nil, ErrWatchEpisodeUnevidenced
	}
	if current := state.WatchIdentity(); current != identity {
		return nil, &WatchEpisodeReplacedError{Current: current}
	}
	tab, err := m.TabFor(ctx, tabID)
	if err != nil {
		return nil, err
	}
	tab.mu.Lock()
	terminal := tab.terminal
	tab.mu.Unlock()
	reader, ok := terminal.(watchSequenceReader)
	if !ok {
		return nil, fmt.Errorf("session: watch target terminal has no sequence tracking: %w", ErrUnsupported)
	}
	pollCtx := context.Background()
	if ownerID := ipc.OwnerID(ctx); ownerID != "" {
		pollCtx = ipc.WithUserID(pollCtx, ownerID)
	}
	return &WatchSource{manager: m, tabID: tabID, identity: identity, reader: reader, options: options, ctx: pollCtx}, nil
}

// Chunk returns the next retained output starting at fromSeq and the position
// immediately after it. It waits (bounded by the poll interval) for new
// output, an exit, or an identity change; io.EOF marks the exited stream end.
func (s *WatchSource) Chunk(ctx context.Context, fromSeq int64) ([]byte, int64, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, fromSeq, err
		}
		state, ok := s.manager.WatchStreamState(s.ctx, s.tabID)
		if !ok {
			return nil, fromSeq, ErrWatchTargetGone
		}
		if current := state.WatchIdentity(); state.EpisodeOK && current != s.identity {
			return nil, fromSeq, &WatchEpisodeReplacedError{Current: current}
		}
		latest := int64(s.reader.LatestSeq())
		if latest > fromSeq {
			data, start, clamped := s.reader.ReplayFrom(uint64(fromSeq), s.options.MaxChunkBytes)
			if clamped {
				return nil, fromSeq, &WatchGapError{FromSeq: int64(start)}
			}
			if len(data) > 0 {
				return data, int64(start) + int64(len(data)), nil
			}
		}
		if state.Exited {
			return nil, fromSeq, io.EOF
		}
		timer := time.NewTimer(s.options.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fromSeq, ctx.Err()
		case <-timer.C:
		}
	}
}

// End returns the current stream end position.
func (s *WatchSource) End(ctx context.Context) (int64, error) {
	if _, ok := s.manager.WatchStreamState(s.ctx, s.tabID); !ok {
		return 0, ErrWatchTargetGone
	}
	return int64(s.reader.LatestSeq()), nil
}

// Tail returns up to maxBytes of the retained stream immediately before
// beforeSeq. Fewer bytes than requested mean the stream (or its retention)
// is shorter — never shifted data.
func (s *WatchSource) Tail(ctx context.Context, beforeSeq int64, maxBytes int) ([]byte, error) {
	if beforeSeq <= 0 || maxBytes <= 0 {
		return nil, nil
	}
	if _, ok := s.manager.WatchStreamState(s.ctx, s.tabID); !ok {
		return nil, ErrWatchTargetGone
	}
	start := beforeSeq - int64(maxBytes)
	if start < 0 {
		start = 0
	}
	data, _, _ := s.reader.ReplayFrom(uint64(start), int(beforeSeq-start))
	return data, nil
}

// Close releases the source. The tab's stream is owned by the session
// manager, so closing only detaches this reader.
func (s *WatchSource) Close() error { return nil }
