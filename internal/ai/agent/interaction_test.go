package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type journalEvent struct {
	Type     string `json:"type"`
	Seq      uint64 `json:"seq"`
	CallID   string `json:"callId"`
	Decision string `json:"decision"`
	ID       string `json:"id"`
}

func journalEvents(t *testing.T, storage *store.Store, jobID string) []journalEvent {
	t.Helper()
	rows := runEventsOf(t, storage, jobID)
	requireContiguousSeq(t, rows)
	events := make([]journalEvent, 0, len(rows))
	for _, row := range rows {
		var event journalEvent
		if err := json.Unmarshal([]byte(row.PayloadJSON), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func journalIndex(events []journalEvent, kind string) int {
	for index, event := range events {
		if event.Type == kind {
			return index
		}
	}
	return -1
}

func TestInteractionResolvedFollowsEachDecision(t *testing.T) {
	for _, decision := range []string{"allow", "allow_session", "deny"} {
		t.Run(decision, func(t *testing.T) {
			storage := restartStore(t)
			runner := durableRunner(t, storage, dockerConfirmChat(), tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}, nil)
			stream := &SliceStream{}
			response := startTestJob(t, runner, stream, "go")
			confirmation := waitEvent(t, stream, "confirmRequired")
			if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: decision}); err != nil {
				t.Fatal(err)
			}
			live := waitClosed(t, stream)
			resolvedAt, resultAt := eventIndex(live, "interactionResolved"), eventIndex(live, "toolResult")
			if resolvedAt < 0 || resultAt < 0 || resolvedAt > resultAt {
				t.Fatalf("interactionResolved must precede the tool result: resolved=%d result=%d events=%+v", resolvedAt, resultAt, live)
			}
			if got := live[resolvedAt]; got.CallID != confirmation.ID || got.Decision != decision || eventCount(live, "interactionResolved") != 1 {
				t.Fatalf("live resolution = %+v", got)
			}
			waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
			journal := journalEvents(t, storage, response.JobID)
			index := journalIndex(journal, "interactionResolved")
			if index < 0 || journal[index].CallID != confirmation.ID || journal[index].Decision != decision {
				t.Fatalf("replay misses the resolution: %+v", journal)
			}
			if index < journalIndex(journal, "confirmRequired") {
				t.Fatalf("resolution replayed before the request: %+v", journal)
			}
		})
	}
}

func TestInteractionResolvedReportsAnsweredQuestion(t *testing.T) {
	storage := restartStore(t)
	var calls atomic.Int64
	chat := &fakeModel{stream: func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("ask", "ask_user", `{"question":"继续？"}`))}), nil
		}
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
	}}
	runner := durableRunner(t, storage, chat, tools.Dependencies{}, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	question := waitEvent(t, stream, "questionRequired")
	if err := runner.Answer(Answer{JobID: response.JobID, CallID: question.ID, Nonce: question.Nonce, Text: "继续"}); err != nil {
		t.Fatal(err)
	}
	live := waitClosed(t, stream)
	resolved := live[eventIndex(live, "interactionResolved")]
	if resolved.CallID != question.ID || resolved.Decision != interactionAnswered {
		t.Fatalf("answered resolution = %+v", resolved)
	}
}

func TestInteractionResolvedReportsExpiry(t *testing.T) {
	storage := restartStore(t)
	checkpoints := NewStoreCheckpoints(storage)
	clock := newHITLTestClock()
	manager, err := hitl.NewManager(hitl.Config{Checkpoints: checkpoints, Store: hitlStoreBridge{storage}, TTL: time.Hour, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	runner := durableRunner(t, storage, dockerConfirmChat(), tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}, manager)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")
	clock.Advance(2 * time.Hour)
	_ = runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"})
	live := waitClosed(t, stream)
	resolvedAt, errorAt := eventIndex(live, "interactionResolved"), eventIndex(live, "error")
	if resolvedAt < 0 || errorAt < 0 || resolvedAt > errorAt {
		t.Fatalf("expiry must settle the card before the terminal error: %+v", live)
	}
	if got := live[resolvedAt]; got.CallID != confirmation.ID || got.Decision != interactionExpired {
		t.Fatalf("expired resolution = %+v", got)
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusExpired)
	journal := journalEvents(t, storage, response.JobID)
	if index := journalIndex(journal, "interactionResolved"); index < 0 || journal[index].Decision != interactionExpired {
		t.Fatalf("replay misses the expiry: %+v", journal)
	}
}

func TestRestoredRunExpiryIsJournaled(t *testing.T) {
	storage := restartStore(t)
	clock := newHITLTestClock()
	newManager := func(afterFunc func(time.Duration, func()) hitl.Timer) *hitl.Manager {
		manager, err := hitl.NewManager(hitl.Config{Checkpoints: NewStoreCheckpoints(storage), Store: hitlStoreBridge{storage}, TTL: time.Hour, Now: clock.Now, AfterFunc: afterFunc})
		if err != nil {
			t.Fatal(err)
		}
		return manager
	}
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, newManager(nil))
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")

	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), deps, newManager(clock.AfterFunc))
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The restored request expires while no job is loaded for it.
	clock.Advance(2 * time.Hour)
	waitRunStatus(t, storage, response.JobID, store.RunStatusExpired)
	journal := journalEvents(t, storage, response.JobID)
	index := journalIndex(journal, "interactionResolved")
	if index < 0 || journal[index].CallID != confirmation.ID || journal[index].Decision != interactionExpired || index > journalIndex(journal, "error") {
		t.Fatalf("restored expiry journal = %+v", journal)
	}
}

func TestConfirmFromReopenedPanelMovesLiveStream(t *testing.T) {
	storage := restartStore(t)
	runner := durableRunner(t, storage, dockerConfirmChat(), tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}, nil)
	original := &SliceStream{}
	response := startTestJob(t, runner, original, "go")
	confirmation := waitEvent(t, original, "confirmRequired")

	reopened := &SliceStream{}
	if err := runner.ConfirmStream(context.Background(), Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, StaticStream(reopened)); err != nil {
		t.Fatal(err)
	}
	after := waitClosed(t, reopened)
	if done, failed := terminalCounts(after); done != 1 || failed != 0 {
		t.Fatalf("reopened panel terminal counts done=%d failed=%d events=%+v", done, failed, after)
	}
	before, closed := original.Snapshot()
	if !closed {
		t.Fatal("the abandoned stream was not closed")
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
	journal := journalEvents(t, storage, response.JobID)
	seen := map[uint64]int{}
	for _, event := range append(before, after...) {
		seen[event.Seq]++
	}
	for _, event := range journal {
		if seen[event.Seq] != 1 {
			t.Fatalf("journal event %d (%s) delivered %d times across the switch", event.Seq, event.Type, seen[event.Seq])
		}
	}
	if len(seen) != len(journal) {
		t.Fatalf("live events %d, journal %d", len(seen), len(journal))
	}
	if eventIndex(before, "toolResult") >= 0 || eventIndex(after, "interactionResolved") < 0 {
		t.Fatalf("resumed events went to the abandoned stream: before=%+v after=%+v", before, after)
	}
}
