package session

import (
	"context"
	"testing"
)

// seqFakeTerminal is a fake terminal whose ring sequence is the number of
// bytes fed, like the real display ring.
type seqFakeTerminal struct {
	*fakeTerminal
}

func (t seqFakeTerminal) LatestSeq() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return uint64(len(t.raw))
}

func (t seqFakeTerminal) ReplayFrom(seq uint64, _ int) ([]byte, uint64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.raw[min(seq, uint64(len(t.raw))):]...), seq, false
}

func historyFromOf(t *testing.T, anchor *streamAnchor) uint64 {
	t.Helper()
	if anchor == nil || anchor.HistoryFrom == nil {
		t.Fatalf("anchor %+v has no history start", anchor)
	}
	return *anchor.HistoryFrom
}

func TestAttachAnchorReportsTheHelperOffsetOfTheViewStart(t *testing.T) {
	manager, tab, _ := historyTestTab(t)
	ctx := context.Background()
	tab.mu.Lock()
	tab.terminal = seqFakeTerminal{fakeTerminal: &fakeTerminal{cols: 80, rows: 24}}
	generation := tab.generation
	tab.displayFrom = 1000
	tab.durableFed = 990
	tab.mu.Unlock()

	// A prelude rendered before any helper output maps to the next helper byte.
	if err := manager.feedDisplay(ctx, tab, generation, []byte("PRE")); err != nil {
		t.Fatal(err)
	}
	anchor, _, _ := manager.planAttachReplay(tab, AttachOptions{ReplayBytes: 1 << 20}, true)
	if got := historyFromOf(t, anchor); got != 1000 {
		t.Fatalf("before helper output the view starts at %d, want 1000", got)
	}

	// Ten bytes before displayFrom stay hidden; "abcdef" is helper [1000, 1006).
	if err := manager.feedDurable(ctx, tab, generation, []byte("0123456789abcdef"), 1006); err != nil {
		t.Fatal(err)
	}
	if err := manager.feedDisplay(ctx, tab, generation, []byte("NOTICE")); err != nil {
		t.Fatal(err)
	}
	if err := manager.feedDurable(ctx, tab, generation, []byte("ghij"), 1010); err != nil {
		t.Fatal(err)
	}
	// Ring: PRE(0-3) abcdef(3-9) NOTICE(9-15) ghij(15-19).
	for _, test := range []struct {
		replay int
		want   uint64
	}{
		{replay: 1 << 20, want: 1000}, // from the prelude
		{replay: 14, want: 1002},      // "cdef" + NOTICE + ghij
		{replay: 7, want: 1006},       // inside the notice: the next helper byte
		{replay: 2, want: 1008},       // "ij"
		{replay: 0, want: 1010},       // nothing replayed: the next helper byte
	} {
		anchor, _, _ := manager.planAttachReplay(tab, AttachOptions{ReplayBytes: test.replay}, true)
		if got := historyFromOf(t, anchor); got != test.want {
			t.Fatalf("replay %d: view starts at helper offset %d, want %d", test.replay, got, test.want)
		}
	}

	// A resumed attach keeps the view's earlier start and reports none.
	anchor, _, _ = manager.planAttachReplay(tab, AttachOptions{FromSeq: 5, FromEpoch: tab.streamEpoch}, false)
	if anchor == nil || anchor.Expired || anchor.HistoryFrom != nil {
		t.Fatalf("resumed anchor %+v", anchor)
	}
}

func TestAttachAnchorOmitsHistoryForRegularTabs(t *testing.T) {
	manager, tab, _ := historyTestTab(t)
	tab.mu.Lock()
	tab.terminal = seqFakeTerminal{fakeTerminal: &fakeTerminal{cols: 80, rows: 24}}
	tab.durable = nil
	generation := tab.generation
	tab.mu.Unlock()
	if err := manager.feed(context.Background(), tab, generation, []byte("output")); err != nil {
		t.Fatal(err)
	}
	anchor, _, _ := manager.planAttachReplay(tab, AttachOptions{ReplayBytes: 1 << 20}, true)
	if anchor == nil || anchor.HistoryFrom != nil {
		t.Fatalf("regular tab anchor %+v", anchor)
	}
}

func TestDurableSeqMapStaysBoundedWithoutGaps(t *testing.T) {
	var seqs durableSeqMap
	for i := uint64(0); i < maxDurableSeqSegments+10; i++ {
		// Every segment is separated by two display-only bytes.
		seqs.record(i*4, 100+i*2, 2)
	}
	if len(seqs.segments) != maxDurableSeqSegments {
		t.Fatalf("segments = %d", len(seqs.segments))
	}
	// A sequence before the oldest kept segment maps to that segment's start:
	// the reported start is later than the truth, never earlier.
	if offset, ok := seqs.offsetAt(0); !ok || offset != 100+10*2 {
		t.Fatalf("offset of an evicted sequence = %d %v", offset, ok)
	}
	seqs = durableSeqMap{}
	seqs.record(0, 50, 3)
	seqs.record(3, 53, 4)
	if len(seqs.segments) != 1 || seqs.segments[0].length != 7 {
		t.Fatalf("contiguous output is not merged: %+v", seqs.segments)
	}
}
