package session

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// durableHistoryGapBanner is rendered (never transcribed) where a durable tab's stream skipped
// output the helper dropped under its history limit before it was streamed.
var durableHistoryGapBanner = []byte("\r\n\x1b[33m[NexTerm] 早期历史已截断：超出持久终端历史上限的输出已被丢弃\x1b[0m\r\n")

// MaxDurableHistoryRead bounds one ReadDurableHistoryFor reply.
const MaxDurableHistoryRead = 192 << 10

// TranscriptGapSink is implemented by transcript sinks that record where a
// durable tab's output was dropped by the helper before this process saw it,
// so the recorded offset keeps matching the helper's absolute offsets.
type TranscriptGapSink interface {
	SessionOutputGap(ctx context.Context, durableID, sessionID, tabID string, skipped int64)
}

func (m *Manager) durableHistoryLimit(ctx context.Context) uint64 {
	if m.historyLimit == nil {
		return 0
	}
	return m.historyLimit(ctx)
}

func (m *Manager) transcriptGap(ctx context.Context, tab *Tab, skipped int64) {
	sink, ok := m.transcripts.(TranscriptGapSink)
	if !ok || skipped <= 0 {
		return
	}
	tab.mu.Lock()
	durableID := ""
	if tab.durable != nil {
		durableID = tab.ID
	}
	tab.mu.Unlock()
	sink.SessionOutputGap(ownerCtx(ctx, tab.session.userID), durableID, tab.SessionID, tab.ID, skipped)
}

// startDurableReplay runs when a pump starts on a durable channel: it reports
// output dropped before this attach to the transcript and renders the mode
// prelude of a tail replay.
func (m *Manager) startDurableReplay(ctx context.Context, tab *Tab, generation uint64) {
	tab.mu.Lock()
	prelude := tab.historyPrelude
	gap := tab.pendingGap
	banner := tab.pendingBanner
	tab.historyPrelude = nil
	tab.pendingGap = 0
	tab.pendingBanner = false
	tab.mu.Unlock()
	if gap > 0 {
		m.transcriptGap(ctx, tab, gap)
	}
	if len(prelude) > 0 {
		// The prelude restores the replayed tail's modes: it is replay too.
		if err := m.feedWith(ctx, tab, generation, prelude, feedOptions{replay: true}); err != nil {
			// It was never rendered: later attaches must not count it.
			tab.mu.Lock()
			tab.replayPending = max(0, tab.replayPending-int64(len(prelude)))
			tab.mu.Unlock()
		}
	}
	if banner {
		// A reconnect inside the recovered tail renders its notice where the
		// tail resumes: replay by position, without consuming the tail count.
		tab.mu.Lock()
		inReplay := tab.replayPending > 0
		tab.mu.Unlock()
		_ = m.feedWith(ctx, tab, generation, durableHistoryGapBanner, feedOptions{replay: inReplay, uncounted: true})
	}
}

// feedDurable feeds one chunk read from a durable attachment whose next
// offset after the chunk is end. Output before the tab's display start goes
// only to the transcript; a jump past the previous end is output the helper
// dropped before streaming it.
func (m *Manager) feedDurable(ctx context.Context, tab *Tab, generation uint64, chunk []byte, end int64) error {
	start := end - int64(len(chunk))
	tab.mu.Lock()
	fed := tab.durableFed
	displayFrom := tab.displayFrom
	replayUntil := tab.replayUntil
	var control ControlEvent
	gap := start - fed
	if gap > 0 {
		tab.historyTruncated = true
		control = tab.controlEventLocked()
	}
	tab.mu.Unlock()
	if gap > 0 {
		m.logger.Warn("durable output dropped by the history limit before it was streamed", "tab", tab.ID, "from", fed, "to", start)
		m.transcriptGap(ctx, tab, gap)
		if start >= displayFrom {
			_ = m.feedWith(ctx, tab, generation, durableHistoryGapBanner, feedOptions{replay: start < replayUntil, uncounted: true})
		}
		m.emit(context.Background(), TopicTerminalControl, control)
	}
	if hidden := min(max(displayFrom-start, 0), int64(len(chunk))); hidden > 0 {
		m.transcriptOutput(ctx, tab, chunk[:hidden])
		chunk = chunk[hidden:]
		start += hidden
	}
	// The recovered tail before replayUntil is history, not new output. A
	// sequence straddling replayUntil belongs to the live part: whatever its
	// final byte completes is new output. The feed that reaches replayUntil
	// closes the replay window, so output the helper dropped inside the tail
	// never leaves a pending count behind.
	ends := replayUntil > 0 && end >= replayUntil
	replayed := min(max(replayUntil-start, 0), int64(len(chunk)))
	if replayed > 0 {
		last := ends && replayed == int64(len(chunk))
		if err := m.feedWith(ctx, tab, generation, chunk[:replayed], feedOptions{transcript: true, replay: true, endReplay: last}); err != nil {
			return err
		}
		chunk = chunk[replayed:]
		if last {
			ends = false
		}
	}
	if len(chunk) > 0 {
		if err := m.feedWith(ctx, tab, generation, chunk, feedOptions{transcript: true, endReplay: ends}); err != nil {
			return err
		}
	} else if ends {
		m.endReplay(ctx, tab)
	}
	tab.mu.Lock()
	if tab.durable != nil && tab.generation == generation {
		tab.durableFed = end
	}
	tab.mu.Unlock()
	return nil
}

// endReplay closes a recovered tab's replay window when no bytes are left to
// feed with it (the tail ended in output the helper dropped or hid). It holds
// the feed gate so no attach observes a half-closed window.
func (m *Manager) endReplay(ctx context.Context, tab *Tab) {
	if err := tab.lockFeed(ctx); err != nil {
		return
	}
	defer tab.unlockFeed()
	tab.mu.Lock()
	tab.replayPending = 0
	tab.replayUntil = 0
	tab.mu.Unlock()
}

// noteDurableReplacement records where a reconnect's replacement attachment
// resumed. A resume offset the helper no longer kept leaves a gap that is
// reported to the transcript and marks the tab's history truncated. Callers
// hold tab.mu.
func (t *Tab) noteDurableReplacementLocked(attachment base.DurableAttachment) {
	replay, ok := durableReplayOf(attachment)
	if !ok {
		return
	}
	if replay.Truncated {
		if gap := int64(replay.ReplayFrom) - t.durableFed; gap > 0 {
			t.pendingGap += gap
		}
		t.historyTruncated = true
		t.historyBase = replay.HistoryBase
		t.pendingBanner = true
	}
	t.durableFed = int64(replay.ReplayFrom)
	t.displayFrom = int64(replay.DisplayFrom)
}

// DurableHistoryChunk is recorded output of a durable tab read without
// attaching.
type DurableHistoryChunk = base.DurableHistoryChunk

// ReadDurableHistoryFor returns recorded output of a durable tab ending at
// before, at most maxBytes (bounded by MaxDurableHistoryRead), for views that
// load output earlier than their replay. Truncated in the result means the
// helper dropped the output before Start.
func (m *Manager) ReadDurableHistoryFor(ctx context.Context, tabID string, before uint64, maxBytes int) (DurableHistoryChunk, error) {
	tab, err := m.TabFor(ctx, tabID)
	if err != nil {
		return DurableHistoryChunk{}, err
	}
	if maxBytes <= 0 || maxBytes > MaxDurableHistoryRead {
		maxBytes = MaxDurableHistoryRead
	}
	tab.mu.Lock()
	durableTab := tab.durable != nil
	identity := tab.durableIdentity
	tab.mu.Unlock()
	if !durableTab {
		return DurableHistoryChunk{}, ErrUnsupported
	}
	session := tab.session
	session.mu.Lock()
	transport := session.transport
	kind := session.asset.Kind
	generation := session.generation
	session.mu.Unlock()
	if transport == nil {
		return DurableHistoryChunk{}, ErrSessionClosed
	}
	provider, err := m.durableProviderFor(ctx, session, transport, kind, generation, kind == KindLocal)
	if err != nil {
		return DurableHistoryChunk{}, err
	}
	reader, ok := provider.(base.DurableHistoryReader)
	if !ok {
		return DurableHistoryChunk{}, ErrUnsupported
	}
	if identity.Incarnation == "" {
		return DurableHistoryChunk{}, ErrDurableUnrecoverable
	}
	chunk, err := reader.ReadDurableHistory(ctx, tab.ID, identity, base.DurableHistoryRequest{Before: before, MaxBytes: maxBytes})
	if err != nil {
		return DurableHistoryChunk{}, err
	}
	if len(chunk.Data) > maxBytes || chunk.End > before {
		return DurableHistoryChunk{}, errors.New("durable history reply exceeds the request")
	}
	return chunk, nil
}
