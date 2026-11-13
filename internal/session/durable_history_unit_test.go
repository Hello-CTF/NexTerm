package session

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type memoryTranscriptOffsets struct {
	mu      sync.Mutex
	offsets map[string]int64
}

func (o *memoryTranscriptOffsets) DurableTranscriptOffsetContext(_ context.Context, tabID string) int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.offsets[tabID]
}

func (o *memoryTranscriptOffsets) PersistDurableTranscriptOffsetContext(_ context.Context, tabID string, offset int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.offsets == nil {
		o.offsets = make(map[string]int64)
	}
	o.offsets[tabID] = offset
}

func (o *memoryTranscriptOffsets) DeleteDurableTranscriptOffsetContext(_ context.Context, tabID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.offsets, tabID)
}

// gapRecorder is a transcript sink that also records the output the helper
// dropped before it could be transcribed.
type gapRecorder struct {
	*transcriptRecorder
	gapMu      sync.Mutex
	gaps       []int64
	unrecorded []int64
}

func (r *gapRecorder) SessionOutputGap(ctx context.Context, durableID, _, _ string, skipped int64) {
	r.gapMu.Lock()
	r.gaps = append(r.gaps, skipped)
	r.gapMu.Unlock()
	r.mu.Lock()
	offsets := r.offsets
	r.mu.Unlock()
	if durableID != "" && offsets != nil {
		offsets.PersistDurableTranscriptOffsetContext(ctx, durableID, offsets.DurableTranscriptOffsetContext(ctx, durableID)+skipped)
	}
}

func (r *gapRecorder) SessionOutputUnrecorded(ctx context.Context, durableID, _, _ string, skipped int64) {
	r.gapMu.Lock()
	r.unrecorded = append(r.unrecorded, skipped)
	r.gapMu.Unlock()
	r.mu.Lock()
	offsets := r.offsets
	r.mu.Unlock()
	if durableID != "" && offsets != nil {
		offsets.PersistDurableTranscriptOffsetContext(ctx, durableID, offsets.DurableTranscriptOffsetContext(ctx, durableID)+skipped)
	}
}

func (r *gapRecorder) unrecordedBytes() int64 {
	r.gapMu.Lock()
	defer r.gapMu.Unlock()
	var total int64
	for _, skipped := range r.unrecorded {
		total += skipped
	}
	return total
}

func (r *gapRecorder) skipped() int64 {
	r.gapMu.Lock()
	defer r.gapMu.Unlock()
	var total int64
	for _, gap := range r.gaps {
		total += gap
	}
	return total
}

type replayAttachment struct {
	base.DurableAttachment
	replay base.DurableReplay
}

func (a replayAttachment) DurableReplay() base.DurableReplay { return a.replay }
func (a replayAttachment) DurableOffset() uint64             { return a.replay.ReplayFrom }

func historyTestTab(t *testing.T) (*Manager, *Tab, *gapRecorder) {
	t.Helper()
	recorder := &gapRecorder{transcriptRecorder: &transcriptRecorder{}}
	manager := NewManager(Config{Connector: newFakeConnector(), Terminals: newFakeTerminalFactory(), Transcripts: recorder})
	t.Cleanup(func() { _ = manager.Close() })
	connected, err := manager.Connect(context.Background(), Asset{ID: "history-unit", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	tab := openTestTab(t, manager, connected, "client-a", "history-unit-1")
	tab.mu.Lock()
	tab.durable = replayAttachment{}
	tab.mu.Unlock()
	return manager, tab, recorder
}

func TestFeedDurableReportsGaps(t *testing.T) {
	manager, tab, recorder := historyTestTab(t)
	ctx := context.Background()
	tab.mu.Lock()
	generation := tab.generation
	tab.mu.Unlock()

	if err := manager.feedDurable(ctx, tab, generation, []byte("shown"), 5); err != nil {
		t.Fatal(err)
	}
	if err := manager.feedDurable(ctx, tab, generation, []byte("after-gap"), 109); err != nil {
		t.Fatal(err)
	}
	shown := tab.terminal.Dump(1 << 20)
	if !bytes.Contains(shown, []byte("shown")) || !bytes.Contains(shown, []byte("after-gap")) {
		t.Fatalf("rendered %q", shown)
	}
	if !bytes.Contains(shown, durableHistoryGapBanner) {
		t.Fatalf("the dropped output is not shown as a notice: %q", shown)
	}
	if got := recorder.outputBytes(); !bytes.Equal(got, []byte("shownafter-gap")) {
		t.Fatalf("transcript = %q", got)
	}
	if skipped := recorder.skipped(); skipped != 95 {
		t.Fatalf("transcript gap = %d, want 95", skipped)
	}
	tab.mu.Lock()
	fed, truncated := tab.durableFed, tab.historyTruncated
	tab.mu.Unlock()
	if fed != 109 || !truncated {
		t.Fatalf("durable fed %d truncated %v", fed, truncated)
	}
}

func TestReconnectReplacementBelowBaseMarksTruncation(t *testing.T) {
	manager, tab, recorder := historyTestTab(t)
	tab.mu.Lock()
	tab.durableFed = 300
	tab.noteDurableReplacementLocked(replayAttachment{replay: base.DurableReplay{
		HistoryBase: 450, HistoryNext: 900, ReplayFrom: 500, Truncated: true,
	}})
	generation := tab.generation
	fed, truncated, historyBase := tab.durableFed, tab.historyTruncated, tab.historyBase
	tab.mu.Unlock()
	if fed != 500 || !truncated || historyBase != 450 {
		t.Fatalf("after replacement fed=%d truncated=%v base=%d", fed, truncated, historyBase)
	}
	manager.startDurableReplay(context.Background(), tab, generation)
	if skipped := recorder.skipped(); skipped != 200 {
		t.Fatalf("transcript gap = %d, want 200", skipped)
	}
	if shown := tab.terminal.Dump(1 << 20); !bytes.Contains(shown, durableHistoryGapBanner) {
		t.Fatalf("truncated resume is not shown: %q", shown)
	}

	tab.mu.Lock()
	tab.noteDurableReplacementLocked(replayAttachment{replay: base.DurableReplay{HistoryNext: 900, ReplayFrom: 600}})
	fed = tab.durableFed
	pending := tab.pendingGap
	tab.mu.Unlock()
	if fed != 600 || pending != 0 {
		t.Fatalf("exact resume fed=%d pending gap=%d", fed, pending)
	}
}

// Output the helper dropped inside a recovered tab's replay was reported as
// unrecorded when the tab recovered: it is not reported again as a gap, and
// the live output after the replay is still recorded.
func TestFeedDurableGapInsideReplayIsNotCountedTwice(t *testing.T) {
	manager, tab, recorder := historyTestTab(t)
	ctx := context.Background()
	tab.mu.Lock()
	generation := tab.generation
	tab.replayUntil = 10
	tab.transcriptSkip = 10
	tab.mu.Unlock()

	if err := manager.feedDurable(ctx, tab, generation, []byte("live"), 14); err != nil {
		t.Fatal(err)
	}
	if skipped := recorder.skipped(); skipped != 0 {
		t.Fatalf("transcript gap = %d, want none inside the replay", skipped)
	}
	if got := recorder.outputBytes(); !bytes.Equal(got, []byte("live")) {
		t.Fatalf("transcript = %q, want the live output after the replay", got)
	}
	tab.mu.Lock()
	skip := tab.transcriptSkip
	tab.mu.Unlock()
	if skip != 0 {
		t.Fatalf("replay still to skip = %d, want 0", skip)
	}
}
