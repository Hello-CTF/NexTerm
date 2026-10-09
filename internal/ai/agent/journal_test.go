package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
)

func journalTestStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	conversation, err := storage.ConvCreate(context.Background(), "journal", map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.RunInsert(context.Background(), store.RunRow{ID: "run-journal", ConversationID: conversation.ID, Status: store.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	return storage, "run-journal"
}

func journalEventsOf(t *testing.T, storage *store.Store, runID string) []store.RunEventRow {
	t.Helper()
	events, err := storage.RunEventsAfter(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func waitJournalEventCount(t *testing.T, storage *store.Store, runID string, count int) []store.RunEventRow {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		events := journalEventsOf(t, storage, runID)
		if len(events) >= count {
			return events
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d journal events", count)
	return nil
}

func journalDeltaText(events []store.RunEventRow) string {
	var builder strings.Builder
	for _, event := range events {
		if event.Type != "delta" {
			continue
		}
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
			continue
		}
		builder.WriteString(payload.Text)
	}
	return builder.String()
}

func TestJournalBatchesDeltasWithinWindow(t *testing.T) {
	storage, runID := journalTestStore(t)
	live := &SliceStream{}
	stream := WithRunJournal(live, storage, runID)
	for _, text := range []string{"你", "好", "啊"} {
		if err := stream.Send(context.Background(), Event{Type: "delta", Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	if events := journalEventsOf(t, storage, runID); len(events) != 0 {
		t.Fatalf("deltas flushed before the window fired: %+v", events)
	}
	events := waitJournalEventCount(t, storage, runID, 1)
	if len(events) != 1 || events[0].Type != "delta" {
		t.Fatalf("journal events = %+v", events)
	}
	if got := journalDeltaText(events); got != "你好啊" {
		t.Fatalf("merged delta text = %q", got)
	}
	liveEvents, _ := live.Snapshot()
	if len(liveEvents) != 1 || liveEvents[0].Seq != 1 || liveEvents[0].Text != "你好啊" {
		t.Fatalf("live events = %+v", liveEvents)
	}
}

func TestJournalFlushesBeforeNonDeltaEvent(t *testing.T) {
	storage, runID := journalTestStore(t)
	live := &SliceStream{}
	stream := WithRunJournal(live, storage, runID)
	for _, text := range []string{"a", "b"} {
		if err := stream.Send(context.Background(), Event{Type: "delta", Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Send(context.Background(), statusEvent("thinking", 1)); err != nil {
		t.Fatal(err)
	}
	events := journalEventsOf(t, storage, runID)
	if len(events) != 2 || events[0].Type != "delta" || events[1].Type != "status" {
		t.Fatalf("journal events = %+v", events)
	}
	if got := journalDeltaText(events); got != "ab" {
		t.Fatalf("merged delta text = %q", got)
	}
	requireContiguousSeq(t, events)
}

func TestJournalFlushesOnByteCap(t *testing.T) {
	storage, runID := journalTestStore(t)
	live := &SliceStream{}
	stream := WithRunJournal(live, storage, runID)
	chunk := strings.Repeat("x", 12<<10)
	for range 3 {
		if err := stream.Send(context.Background(), Event{Type: "delta", Text: chunk}); err != nil {
			t.Fatal(err)
		}
	}
	events := journalEventsOf(t, storage, runID)
	if len(events) != 1 {
		t.Fatalf("byte cap did not flush immediately: %+v", events)
	}
	if got := journalDeltaText(events); got != chunk+chunk+chunk {
		t.Fatalf("merged delta length = %d", len(got))
	}
}

func TestJournalTerminalEventPersistedImmediately(t *testing.T) {
	storage, runID := journalTestStore(t)
	live := &SliceStream{}
	stream := WithRunJournal(live, storage, runID)
	if err := stream.Send(context.Background(), Event{Type: "delta", Text: "tail"}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(context.Background(), doneEvent("done", 1, 2, 3)); err != nil {
		t.Fatal(err)
	}
	events := journalEventsOf(t, storage, runID)
	if len(events) != 2 || events[0].Type != "delta" || events[1].Type != "done" {
		t.Fatalf("journal events = %+v", events)
	}
	time.Sleep(2 * journalFlushWindow)
	if after := journalEventsOf(t, storage, runID); len(after) != 2 {
		t.Fatalf("terminal buffer not empty: %+v", after)
	}
}

func TestJournalCloseFlushesPending(t *testing.T) {
	storage, runID := journalTestStore(t)
	live := &SliceStream{}
	stream := WithRunJournal(live, storage, runID)
	if err := stream.Send(context.Background(), Event{Type: "delta", Text: "bye"}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	events := journalEventsOf(t, storage, runID)
	if len(events) != 1 || events[0].Type != "delta" || journalDeltaText(events) != "bye" {
		t.Fatalf("journal events = %+v", events)
	}
	if _, closed := live.Snapshot(); !closed {
		t.Fatal("underlying stream not closed")
	}
}

func TestJournalReplayReconstructsIdenticalText(t *testing.T) {
	storage, runID := journalTestStore(t)
	live := &SliceStream{}
	stream := WithRunJournal(live, storage, runID)
	sends := []Event{
		{Type: "delta", Text: "你"},
		{Type: "reasoning", Text: "想"},
		{Type: "delta", Text: "好"},
		{Type: "toolArgs", Tool: "write_file", Chars: 5},
		{Type: "toolArgs", Tool: "write_file", Chars: 9},
		{Type: "delta", Text: "！"},
		statusEvent("thinking", 1),
		{Type: "delta", Text: "。"},
		doneEvent("answer", 1, 2, 3),
	}
	for _, event := range sends {
		if err := stream.Send(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	events := journalEventsOf(t, storage, runID)
	requireContiguousSeq(t, events)
	if got := journalDeltaText(events); got != "你好！。" {
		t.Fatalf("replayed delta text = %q", got)
	}
	var nonDelta []string
	for _, event := range events {
		if event.Type != "delta" && event.Type != "reasoning" {
			nonDelta = append(nonDelta, event.Type)
		}
	}
	if strings.Join(nonDelta, ",") != "toolArgs,status,done" {
		t.Fatalf("non-delta order = %v", nonDelta)
	}
	var toolArgsChars int
	for _, event := range events {
		if event.Type != "toolArgs" {
			continue
		}
		var payload struct {
			Chars int `json:"chars"`
		}
		if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
			t.Fatal(err)
		}
		toolArgsChars = payload.Chars
	}
	if toolArgsChars != 9 {
		t.Fatalf("toolArgs chars = %d, want latest cumulative 9", toolArgsChars)
	}
	for cut := uint64(0); cut < uint64(len(events)); cut++ {
		page, err := storage.RunEventsAfter(context.Background(), runID, cut)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 || page[0].Seq != cut+1 {
			t.Fatalf("replay after %d starts at %+v", cut, page)
		}
		prefixText := ""
		if cut > 0 {
			prefix, err := storage.RunEventsAfterLimit(context.Background(), runID, 0, int(cut))
			if err != nil {
				t.Fatal(err)
			}
			prefixText = journalDeltaText(prefix)
		}
		if got := prefixText + journalDeltaText(page); got != "你好！。" {
			t.Fatalf("replay at cut %d reconstructs %q", cut, got)
		}
	}
}

func TestJournalTerminalJournaledWhenDownstreamFailsDuringFlush(t *testing.T) {
	storage, runID := journalTestStore(t)
	stream := WithRunJournal(&failingDownstream{}, storage, runID)
	for _, event := range []Event{
		{Type: "delta", Text: "a"},
		{Type: "delta", Text: "b"},
		{Type: "reasoning", Text: "r"},
		{Type: "toolArgs", Tool: "write_file", Chars: 7},
	} {
		if err := stream.Send(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Send(context.Background(), doneEvent("done", 1, 2, 3)); err == nil {
		t.Fatal("terminal send must surface the downstream failure")
	}
	events := journalEventsOf(t, storage, runID)
	requireContiguousSeq(t, events)
	if len(events) != 4 {
		t.Fatalf("journaled events = %+v, want merged delta, reasoning, toolArgs and done", events)
	}
	if events[0].Type != "delta" || journalDeltaText(events) != "ab" {
		t.Fatalf("journaled delta = %+v", events)
	}
	if events[1].Type != "reasoning" || events[2].Type != "toolArgs" {
		t.Fatalf("journaled merged events = %+v", events[1:3])
	}
	if events[3].Type != "done" {
		t.Fatalf("terminal event missing from journal: %+v", events)
	}
}

type failingDownstream struct{}

func (failingDownstream) Send(context.Context, Event) error { return errors.New("downstream broken") }
func (failingDownstream) Close() error                      { return nil }

type failFirstDownstream struct {
	*SliceStream
	remaining int
}

func (f *failFirstDownstream) Send(ctx context.Context, event Event) error {
	if f.remaining > 0 {
		f.remaining--
		return errors.New("downstream broken")
	}
	return f.SliceStream.Send(ctx, event)
}

func TestJournalFlushDownstreamFailureKeepsJournalComplete(t *testing.T) {
	storage, runID := journalTestStore(t)
	live := &SliceStream{}
	stream := WithRunJournal(&failFirstDownstream{SliceStream: live, remaining: 2}, storage, runID)
	for _, event := range []Event{
		{Type: "delta", Text: "x"},
		{Type: "delta", Text: "y"},
	} {
		if err := stream.Send(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Send(context.Background(), statusEvent("thinking", 1)); err == nil {
		t.Fatal("status send must surface its own downstream failure")
	}
	if err := stream.Send(context.Background(), doneEvent("done", 0, 0, 0)); err != nil {
		t.Fatalf("terminal send must not fail after flush swallowed the downstream error: %v", err)
	}
	events := journalEventsOf(t, storage, runID)
	requireContiguousSeq(t, events)
	if len(events) != 3 || events[0].Type != "delta" || events[1].Type != "status" || events[2].Type != "done" {
		t.Fatalf("journaled events = %+v", events)
	}
	if got := journalDeltaText(events); got != "xy" {
		t.Fatalf("journaled delta text = %q", got)
	}
	liveEvents, _ := live.Snapshot()
	if len(liveEvents) != 1 || liveEvents[0].Type != "done" {
		t.Fatalf("live events = %+v, want only the terminal event", liveEvents)
	}
}

func TestJournalConcurrentSendClose(t *testing.T) {
	storage, runID := journalTestStore(t)
	live := &SliceStream{}
	stream := WithRunJournal(live, storage, runID)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for index := 0; index < 20; index++ {
				event := Event{Type: "delta", Text: "x"}
				if index%5 == 4 {
					event = statusEvent("thinking", index)
				}
				_ = stream.Send(context.Background(), event)
			}
		}(worker)
	}
	wg.Wait()
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	events := journalEventsOf(t, storage, runID)
	requireContiguousSeq(t, events)
	if len(events) == 0 || events[len(events)-1].Type == "delta" {
		t.Fatalf("journal must end with a non-delta event: %+v", events)
	}
}
