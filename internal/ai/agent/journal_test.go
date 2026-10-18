package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/steer"
	"github.com/Hello-CTF/NexTerm/internal/ai/usage"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type failingAppendRunStore struct {
	RunStore
	mu        sync.Mutex
	remaining int
	attempts  int
	err       error
}

func (s *failingAppendRunStore) RunAppendEvent(ctx context.Context, runID, eventType string, payload func(uint64) ([]byte, error)) (uint64, error) {
	s.mu.Lock()
	s.attempts++
	fail := s.remaining > 0
	if fail {
		s.remaining--
	}
	s.mu.Unlock()
	if fail {
		return 0, s.err
	}
	return s.RunStore.RunAppendEvent(ctx, runID, eventType, payload)
}

func (s *failingAppendRunStore) attemptCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts
}

type finishOrderRunStore struct {
	RunStore
	terminalBeforeFinish bool
	statusBeforeClose    bool
	live                 *SliceStream
}

type terminalStatusStream struct {
	*SliceStream
	runs                 RunStore
	runID                string
	terminalObserved     bool
	terminalBeforeStatus bool
}

func (s *terminalStatusStream) Send(ctx context.Context, event Event) error {
	if event.Type == "done" || event.Type == "error" || event.Type == "canceled" {
		s.terminalObserved = true
		row, err := s.runs.RunGet(ctx, s.runID)
		if err != nil || row.Status != store.RunStatusCompleted {
			s.terminalBeforeStatus = true
		}
	}
	return s.SliceStream.Send(ctx, event)
}

func (s *finishOrderRunStore) RunFinishUsage(ctx context.Context, runID, status, answer, errMsg string, turns int, tokensIn, tokensOut, cacheCreationTokens, latencyMS int64) error {
	events, err := s.RunStore.RunEventsAfter(ctx, runID, 0)
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.Type == "done" || event.Type == "error" || event.Type == "canceled" {
			s.terminalBeforeFinish = true
			break
		}
	}
	if s.live != nil {
		_, closed := s.live.Snapshot()
		s.statusBeforeClose = !closed
	}
	return s.RunStore.RunFinishUsage(ctx, runID, status, answer, errMsg, turns, tokensIn, tokensOut, cacheCreationTokens, latencyMS)
}

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

func journalOwnedTestStore(t *testing.T) (*store.Store, string, context.Context) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	ctx := ipc.WithUserID(context.Background(), "journal-owner")
	conversation, err := storage.ConvCreate(ctx, "journal", nil)
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-owned-journal"
	if err := storage.RunInsert(ctx, store.RunRow{ID: runID, ConversationID: conversation.ID, Status: store.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	return storage, runID, ctx
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

func TestJournalRetriesTimedFlushWithoutAnotherEvent(t *testing.T) {
	storage, runID := journalTestStore(t)
	journal := &failingAppendRunStore{RunStore: storage, remaining: 1, err: errors.New("temporary journal failure")}
	live := &SliceStream{}
	stream := WithRunJournal(live, journal, runID)
	if err := stream.Send(context.Background(), Event{Type: "delta", Text: "retained"}); err != nil {
		t.Fatal(err)
	}
	events := waitJournalEventCount(t, storage, runID, 1)
	if got := journalDeltaText(events); got != "retained" {
		t.Fatalf("retried journal text = %q", got)
	}
	if attempts := journal.attemptCount(); attempts < 2 {
		t.Fatalf("journal append attempts = %d, want retry", attempts)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestJournalTimedFlushKeepsOwnerScopeForReplay(t *testing.T) {
	storage, runID, ownerCtx := journalOwnedTestStore(t)
	requestCtx, cancel := context.WithCancel(ownerCtx)
	live := &SliceStream{}
	stream := WithRunJournal(live, storage, runID)
	if err := stream.Send(requestCtx, Event{Type: "delta", Text: "owned"}); err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	var events []store.RunEventRow
	for time.Now().Before(deadline) {
		var err error
		events, err = storage.RunEventsAfter(ownerCtx, runID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(events) != 1 || events[0].Seq != 1 || journalDeltaText(events) != "owned" {
		t.Fatalf("owner-visible timed events = %+v", events)
	}
	if err := stream.Send(ownerCtx, doneEvent("answer", 1, 2, 3)); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := storage.RunEventsAfter(ownerCtx, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Seq != 1 || events[1].Seq != 2 || events[1].Type != "done" {
		t.Fatalf("owner-visible contiguous replay = %+v", events)
	}
}

func TestJournalCloseKeepsOwnerScopeAfterRequestCancellation(t *testing.T) {
	storage, runID, ownerCtx := journalOwnedTestStore(t)
	requestCtx, cancel := context.WithCancel(ownerCtx)
	stream := WithRunJournal(&SliceStream{}, storage, runID)
	if err := stream.Send(requestCtx, Event{Type: "delta", Text: "tail"}); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := storage.RunEventsAfter(ownerCtx, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Seq != 1 || journalDeltaText(events) != "tail" {
		t.Fatalf("owner-visible close events = %+v", events)
	}
}

func TestJournalRejectsDifferentOwnerContext(t *testing.T) {
	storage, runID, ownerCtx := journalOwnedTestStore(t)
	stream := WithRunJournal(&SliceStream{}, storage, runID)
	if err := stream.Send(ownerCtx, statusEvent("thinking", 1)); err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(ipc.WithUserID(context.Background(), "different-owner"), doneEvent("answer", 1, 2, 3)); err == nil {
		t.Fatal("different owner was allowed to send on an existing journal")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := storage.RunEventsAfter(ownerCtx, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "status" {
		t.Fatalf("owner-visible events after rejected send = %+v", events)
	}
}

func TestJournalFailureStillDeliversLiveTerminal(t *testing.T) {
	storage, runID := journalTestStore(t)
	failure := errors.New("journal unavailable")
	journal := &failingAppendRunStore{RunStore: storage, remaining: 1, err: failure}
	live := &SliceStream{}
	stream := WithRunJournal(live, journal, runID)
	if err := stream.Send(context.Background(), doneEvent("answer", 1, 2, 3)); !errors.Is(err, failure) {
		t.Fatalf("send error = %v, want journal failure", err)
	}
	liveEvents, _ := live.Snapshot()
	if len(liveEvents) != 1 || liveEvents[0].Type != "done" || liveEvents[0].Seq != 0 {
		t.Fatalf("live fallback = %+v", liveEvents)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if events := journalEventsOf(t, storage, runID); len(events) != 1 || events[0].Type != "done" {
		t.Fatalf("recovered journal events = %+v", events)
	}
}

func TestJournalPendingFlushFailureStillDeliversLiveTerminal(t *testing.T) {
	storage, runID := journalTestStore(t)
	failure := errors.New("journal unavailable")
	journal := &failingAppendRunStore{RunStore: storage, remaining: 1, err: failure}
	live := &SliceStream{}
	stream := WithRunJournal(live, journal, runID)
	if err := stream.Send(context.Background(), Event{Type: "delta", Text: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(context.Background(), doneEvent("answer", 1, 2, 3)); !errors.Is(err, failure) {
		t.Fatalf("send error = %v, want journal failure", err)
	}
	liveEvents, _ := live.Snapshot()
	if len(liveEvents) != 1 || liveEvents[0].Type != "done" || liveEvents[0].Seq != 0 {
		t.Fatalf("live terminal after pending flush failure = %+v", liveEvents)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	events := journalEventsOf(t, storage, runID)
	if len(events) != 2 || events[0].Type != "delta" || events[1].Type != "done" {
		t.Fatalf("recovered pending journal events = %+v", events)
	}
}

func TestJournalCloseRetriesTransientFailureWithOwnerAndTerminal(t *testing.T) {
	storage, runID, ownerCtx := journalOwnedTestStore(t)
	failure := errors.New("temporary journal failure")
	journal := &failingAppendRunStore{RunStore: storage, remaining: 2, err: failure}
	live := &SliceStream{}
	stream := WithRunJournal(live, journal, runID)
	requestCtx, cancel := context.WithCancel(ownerCtx)
	if err := stream.Send(requestCtx, Event{Type: "delta", Text: "tail"}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(requestCtx, doneEvent("answer", 1, 2, 3)); !errors.Is(err, failure) {
		t.Fatalf("terminal send error = %v, want temporary failure", err)
	}
	cancel()
	if err := stream.Close(); err != nil {
		t.Fatalf("close did not recover temporary journal failure: %v", err)
	}
	events, err := storage.RunEventsAfter(ownerCtx, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Seq != 1 || events[1].Seq != 2 || events[0].Type != "delta" || events[1].Type != "done" {
		t.Fatalf("owner-visible replay after close retry = %+v", events)
	}
	if got := journalDeltaText(events); got != "tail" {
		t.Fatalf("recovered delta text = %q", got)
	}
}

func TestJournalCloseReportsPersistentFailure(t *testing.T) {
	storage, runID := journalTestStore(t)
	failure := errors.New("persistent journal failure")
	journal := &failingAppendRunStore{RunStore: storage, remaining: 100, err: failure}
	live := &SliceStream{}
	stream := WithRunJournal(live, journal, runID)
	if err := stream.Send(context.Background(), Event{Type: "delta", Text: "tail"}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); !errors.Is(err, failure) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("persistent close error = %v, want journal failure and timeout", err)
	}
	if _, closed := live.Snapshot(); !closed {
		t.Fatal("downstream remained open after persistent journal failure")
	}
}

func TestRunnerPersistsTerminalBeforeCompletedStatus(t *testing.T) {
	storage, runID := journalTestStore(t)
	live := &SliceStream{}
	checkedLive := &terminalStatusStream{SliceStream: live, runs: storage, runID: runID}
	journal := &finishOrderRunStore{RunStore: storage, live: live}
	runner := NewRunner(Config{Store: storage, Runs: journal})
	t.Cleanup(func() { _ = runner.Close() })
	current := &job{
		id: runID, ctx: context.Background(), cancel: func() {},
		deliveryCtx: context.Background(), stream: runner.wrapStream(checkedLive, runID),
		steer: steer.NewQueue(1), cancelSync: newCancelSync(),
	}
	if _, err := runner.hitl.RegisterRun(context.Background(), runID, runID); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.jobs[runID] = current
	runner.wg.Add(1)
	runner.mu.Unlock()
	runner.complete(current, "answer", 1, usage.Usage{}, nil)
	if !journal.terminalBeforeFinish {
		t.Fatal("run status completed before terminal event was journaled")
	}
	if !journal.statusBeforeClose {
		t.Fatal("live stream closed before run status was updated")
	}
	if !checkedLive.terminalObserved || checkedLive.terminalBeforeStatus {
		t.Fatal("terminal event reached the live stream before run status was updated")
	}
	run, err := storage.RunGet(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.RunStatusCompleted {
		t.Fatalf("run status = %q", run.Status)
	}
}

func TestRunnerUpdatesStatusBeforeTerminalFallback(t *testing.T) {
	storage, runID := journalTestStore(t)
	journal := &failingAppendRunStore{RunStore: storage, remaining: 1, err: errors.New("temporary journal failure")}
	live := &terminalStatusStream{SliceStream: &SliceStream{}, runs: storage, runID: runID}
	runner := NewRunner(Config{Store: storage, Runs: journal})
	t.Cleanup(func() { _ = runner.Close() })
	current := &job{
		id: runID, ctx: context.Background(), cancel: func() {},
		deliveryCtx: context.Background(), stream: runner.wrapStream(live, runID),
		steer: steer.NewQueue(1), cancelSync: newCancelSync(),
	}
	if _, err := runner.hitl.RegisterRun(context.Background(), runID, runID); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.jobs[runID] = current
	runner.wg.Add(1)
	runner.mu.Unlock()
	runner.complete(current, "answer", 1, usage.Usage{}, nil)
	if !live.terminalObserved || live.terminalBeforeStatus {
		t.Fatal("fallback terminal reached the live stream before run status was updated")
	}
	events, closed := live.Snapshot()
	if !closed || len(events) == 0 || events[0].Type != "done" || events[0].Seq != 0 {
		t.Fatalf("expected unsequenced terminal fallback, got events=%+v closed=%v", events, closed)
	}
}

type failingFinishStream struct {
	sendErr  error
	closeErr error
}

func (s failingFinishStream) Send(context.Context, Event) error { return s.sendErr }
func (s failingFinishStream) Close() error                      { return s.closeErr }

type gracefulTrackingStream struct {
	SliceStream
	graceful bool
}

func (s *gracefulTrackingStream) CloseGracefully() error {
	s.graceful = true
	return s.SliceStream.Close()
}

func TestJobFinishReportsSendAndCloseFailures(t *testing.T) {
	sendErr := errors.New("send failed")
	closeErr := errors.New("close failed")
	current := &job{
		ctx: context.Background(), deliveryCtx: context.Background(),
		stream: failingFinishStream{sendErr: sendErr, closeErr: closeErr},
	}
	err := current.finish("answer", 1, usage.Usage{}, nil)
	if !errors.Is(err, sendErr) || !errors.Is(err, closeErr) {
		t.Fatalf("finish error = %v, want send and close failures", err)
	}
}

func TestJobFinishGracefullyClosesDeliveredFallback(t *testing.T) {
	storage, runID := journalTestStore(t)
	failure := errors.New("journal unavailable")
	journal := &failingAppendRunStore{RunStore: storage, remaining: 2, err: failure}
	live := &gracefulTrackingStream{}
	current := &job{
		ctx: context.Background(), deliveryCtx: context.Background(),
		stream: WithRunJournal(live, journal, runID),
	}
	if err := current.finish("answer", 1, usage.Usage{}, nil); !errors.Is(err, failure) {
		t.Fatalf("finish error = %v, want journal failure", err)
	}
	if !live.graceful {
		t.Fatal("delivered fallback was closed without draining the live stream")
	}
	events, closed := live.Snapshot()
	if !closed || len(events) != 2 || events[0].Type != "done" || events[1].Type != "done" || events[1].Seq != 1 {
		t.Fatalf("live fallback events = %+v, closed = %v", events, closed)
	}
	if journaled := journalEventsOf(t, storage, runID); len(journaled) != 1 || journaled[0].Type != "done" {
		t.Fatalf("graceful close failed to persist terminal: %+v", journaled)
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
