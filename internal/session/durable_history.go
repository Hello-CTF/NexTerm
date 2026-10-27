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

// DurableHistoryLatest as the before offset of ReadDurableHistoryFor reads up
// to the newest output this process received from the helper.
const DurableHistoryLatest = ^uint64(0)

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
		_ = m.feedWith(ctx, tab, generation, prelude, feedOptions{replay: true})
	}
	if banner {
		// A reconnect inside the recovered tail renders its notice where the
		// tail resumes: replay by position.
		tab.mu.Lock()
		inReplay := tab.replayUntil > tab.durableFed
		tab.mu.Unlock()
		_ = m.feedWith(ctx, tab, generation, durableHistoryGapBanner, feedOptions{replay: inReplay})
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
			_ = m.feedWith(ctx, tab, generation, durableHistoryGapBanner, feedOptions{replay: start < replayUntil})
		}
		m.emit(context.Background(), TopicTerminalControl, control)
	}
	hidden := min(max(displayFrom-start, 0), int64(len(chunk)))
	if hidden > 0 {
		m.transcriptOutput(ctx, tab, chunk[:hidden])
		chunk = chunk[hidden:]
		start += hidden
	}
	// The recovered tail before replayUntil is history, not new output. A
	// sequence straddling replayUntil belongs to the live part: whatever its
	// final byte completes is new output. Every fed range is classified by
	// stream sequence, so deliveries stay exact however the tail ends.
	if replayed := min(max(replayUntil-start, 0), int64(len(chunk))); replayed > 0 {
		if err := m.feedWith(ctx, tab, generation, chunk[:replayed], feedOptions{
			transcript: true, replay: true, helper: true, helperOffset: uint64(start),
		}); err != nil {
			return err
		}
		chunk = chunk[replayed:]
		start += replayed
	}
	if len(chunk) > 0 {
		if err := m.feedWith(ctx, tab, generation, chunk, feedOptions{
			transcript: true, helper: true, helperOffset: uint64(start),
		}); err != nil {
			return err
		}
	}
	tab.mu.Lock()
	if tab.durable != nil && tab.generation == generation {
		tab.durableFed = end
		if replayUntil > 0 && end >= replayUntil {
			tab.replayUntil = 0
		}
	}
	tab.mu.Unlock()
	return nil
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
// helper dropped the output before Start. before = DurableHistoryLatest reads
// up to the newest output this process received.
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
	if before == DurableHistoryLatest {
		before = uint64(max(tab.durableFed, 0))
	}
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

// maxDurableSeqSegments bounds the sequence map. Dropping the oldest segment
// only moves an old view's reported start later, which overlaps the view
// instead of leaving a gap.
const maxDurableSeqSegments = 256

type durableSeqSegment struct {
	seq, offset, length uint64
}

// durableSeqMap maps display ring sequences to helper offsets: each run of
// helper output fed contiguously is one segment. Preludes and notices are fed
// without a segment, so they map to the next helper byte.
type durableSeqMap struct {
	segments []durableSeqSegment
}

func (m *durableSeqMap) record(seq, offset, length uint64) {
	if length == 0 {
		return
	}
	if n := len(m.segments); n > 0 {
		last := &m.segments[n-1]
		if last.seq+last.length == seq && last.offset+last.length == offset {
			last.length += length
			return
		}
	}
	if len(m.segments) >= maxDurableSeqSegments {
		m.segments = append(m.segments[:0], m.segments[1:]...)
	}
	m.segments = append(m.segments, durableSeqSegment{seq: seq, offset: offset, length: length})
}

// offsetAt returns the helper offset of the first helper byte at or after seq,
// or false when no helper output was fed at or after it.
func (m *durableSeqMap) offsetAt(seq uint64) (uint64, bool) {
	for _, segment := range m.segments {
		if seq < segment.seq {
			return segment.offset, true
		}
		if seq < segment.seq+segment.length {
			return segment.offset + (seq - segment.seq), true
		}
	}
	return 0, false
}

func (m *durableSeqMap) next() (uint64, bool) {
	if len(m.segments) == 0 {
		return 0, false
	}
	last := m.segments[len(m.segments)-1]
	return last.offset + last.length, true
}

// durableHistoryFrom is the helper offset of the first helper byte a stream
// starting at ring sequence seq renders, for durable tabs; nil otherwise.
func (t *Tab) durableHistoryFrom(seq uint64) *uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.durable == nil {
		return nil
	}
	offset, ok := t.durableSeqs.offsetAt(seq)
	if !ok {
		// Nothing rendered yet at or after seq: the stream starts with the
		// next helper byte that will be rendered.
		next, _ := t.durableSeqs.next()
		offset = max(next, uint64(max(t.durableFed, t.displayFrom, 0)))
	}
	return &offset
}

// helperFeedOptions are the feed options of the pump's next chunk: durable
// output is mapped from the helper offset of the next byte it feeds.
func (t *Tab) helperFeedOptions() feedOptions {
	t.mu.Lock()
	defer t.mu.Unlock()
	options := feedOptions{transcript: true}
	if t.durable != nil {
		options.helper = true
		options.helperOffset = uint64(max(t.durableFed, 0))
	}
	return options
}
