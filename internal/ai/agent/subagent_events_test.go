package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/subagent"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func subagentRunner(t *testing.T, parent model.BaseChatModel, child model.BaseChatModel) *Runner {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	runner := NewRunner(Config{
		Model:     func(context.Context) (model.BaseChatModel, uint64, error) { return parent, 32768, nil },
		Tools:     tools.NewRegistry(tools.Dependencies{}),
		Store:     storage,
		Subagents: &tools.SubagentConfig{Model: func(context.Context) (model.BaseChatModel, error) { return child, nil }},
	})
	t.Cleanup(func() { _ = runner.Close() })
	return runner
}

func subagentEventOf(events []Event, kind string) []Event {
	var matched []Event
	for _, event := range events {
		if event.Type == kind {
			matched = append(matched, event)
		}
	}
	return matched
}

func indexOfEvent(events []Event, match func(Event) bool) int {
	for index, event := range events {
		if match(event) {
			return index
		}
	}
	return -1
}

func TestSubagentEventsMergeIntoParentStreamWithOrderedSeq(t *testing.T) {
	parent := sequenceModel(
		toolCallMessage(namedToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`)),
		schema.AssistantMessage("parent done", nil),
	)
	child := sequenceModel(schema.AssistantMessage("child done", nil))
	runner := subagentRunner(t, parent, child)
	stream := &SliceStream{}
	startTestJob(t, runner, stream, "go")
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("terminal counts done=%d error=%d events=%+v", done, failed, events)
	}
	deltas := subagentEventOf(events, "subagentDelta")
	dones := subagentEventOf(events, "subagentDone")
	if len(deltas) != 1 || deltas[0].Text != "child done" {
		t.Fatalf("subagentDelta events = %+v", deltas)
	}
	if len(dones) != 1 || dones[0].Status != string(subagent.StatusCompleted) || dones[0].Summary != "child done" {
		t.Fatalf("subagentDone events = %+v", dones)
	}
	for _, event := range append(deltas, dones...) {
		if event.ParentCallID != "sp-1" {
			t.Fatalf("event %+v parent call ID = %q, want sp-1", event, event.ParentCallID)
		}
		if event.SubagentID == "" || event.Depth != 1 {
			t.Fatalf("event %+v missing subagent attribution", event)
		}
	}
	toolCallAt := indexOfEvent(events, func(event Event) bool { return event.Type == "toolCall" && event.ID == "sp-1" })
	deltaAt := indexOfEvent(events, func(event Event) bool { return event.Type == "subagentDelta" })
	doneAt := indexOfEvent(events, func(event Event) bool { return event.Type == "subagentDone" })
	resultAt := indexOfEvent(events, func(event Event) bool { return event.Type == "toolResult" && event.ID == "sp-1" })
	terminalAt := indexOfEvent(events, func(event Event) bool { return event.Type == "done" })
	if !(toolCallAt < deltaAt && deltaAt < doneAt && doneAt < resultAt && resultAt < terminalAt) {
		t.Fatalf("unexpected event order: toolCall=%d delta=%d subagentDone=%d toolResult=%d done=%d", toolCallAt, deltaAt, doneAt, resultAt, terminalAt)
	}
	for index := 1; index < len(events); index++ {
		if events[index].Seq <= events[index-1].Seq {
			t.Fatalf("seq not strictly increasing at %d: %d <= %d", index, events[index].Seq, events[index-1].Seq)
		}
	}
}

func TestSubagentEventsIsolatedPerRun(t *testing.T) {
	parentOne := sequenceModel(
		toolCallMessage(namedToolCall("sp-1", subagent.SpawnToolName, `{"task":"child one"}`)),
		schema.AssistantMessage("parent one done", nil),
	)
	parentTwo := sequenceModel(
		toolCallMessage(namedToolCall("sp-2", subagent.SpawnToolName, `{"task":"child two"}`)),
		schema.AssistantMessage("parent two done", nil),
	)
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	parents := []model.BaseChatModel{parentOne, parentTwo}
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) {
			parent := parents[0]
			parents = parents[1:]
			return parent, 32768, nil
		},
		Tools: tools.NewRegistry(tools.Dependencies{}),
		Store: storage,
		Subagents: &tools.SubagentConfig{Model: func(context.Context) (model.BaseChatModel, error) {
			return sequenceModel(schema.AssistantMessage("child done", nil)), nil
		}},
	})
	defer runner.Close()
	first := &SliceStream{}
	startTestJob(t, runner, first, "go one")
	eventsOne := waitClosed(t, first)
	second := &SliceStream{}
	startTestJob(t, runner, second, "go two")
	eventsTwo := waitClosed(t, second)
	assertRun := func(events []Event, wantParent string) {
		t.Helper()
		var subagentEvents []Event
		for _, event := range events {
			if strings.HasPrefix(event.Type, "subagent") {
				subagentEvents = append(subagentEvents, event)
			}
		}
		if len(subagentEvents) == 0 {
			t.Fatal("no subagent events merged")
		}
		for _, event := range subagentEvents {
			if event.ParentCallID != wantParent {
				t.Fatalf("cross-run leakage: %+v", event)
			}
		}
	}
	assertRun(eventsOne, "sp-1")
	assertRun(eventsTwo, "sp-2")
}

func TestSubagentCancelPropagatesAndRunTerminates(t *testing.T) {
	started := make(chan struct{})
	parent := sequenceModel(
		toolCallMessage(namedToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`)),
	)
	child := &fakeModel{}
	child.stream = func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	runner := subagentRunner(t, parent, child)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("child model did not start")
	}
	if err := runner.Cancel(response.JobID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	events := waitClosedTimeout(t, stream, 5*time.Second)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("terminal counts done=%d error=%d events=%+v", done, failed, events)
	}
}
