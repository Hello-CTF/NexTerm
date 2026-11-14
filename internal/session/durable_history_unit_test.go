package session

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// gapNotice is the part of the notice a recording carries where the helper
// dropped skipped bytes of output before this process saw them.
func gapNotice(skipped int) []byte {
	return []byte(fmt.Sprintf("此处有 %d 字节输出", skipped))
}

type replayAttachment struct {
	base.DurableAttachment
	replay base.DurableReplay
}

func (a replayAttachment) DurableReplay() base.DurableReplay { return a.replay }
func (a replayAttachment) DurableOffset() uint64             { return a.replay.ReplayFrom }

func historyTestTab(t *testing.T) (*Manager, *Tab, *transcriptRecorder) {
	t.Helper()
	recorder := &transcriptRecorder{}
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
	// The recording marks the dropped output where it was dropped.
	recorded := recorder.outputBytes()
	shownAt, noticeAt, afterAt := bytes.Index(recorded, []byte("shown")), bytes.Index(recorded, gapNotice(95)), bytes.Index(recorded, []byte("after-gap"))
	if shownAt < 0 || noticeAt < shownAt || afterAt < noticeAt {
		t.Fatalf("recording = %q, want the output with a notice of 95 dropped bytes between", recorded)
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
	if recorded := recorder.outputBytes(); !bytes.Contains(recorded, gapNotice(200)) {
		t.Fatalf("recording = %q, want a notice of 200 dropped bytes", recorded)
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

// Output the helper dropped inside a recovered tab's replay is history from
// before the recording: it is not marked as a gap, and the live output after
// the replay is still recorded.
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
