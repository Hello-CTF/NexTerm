package subagent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

func eventRecorder() (Observer, func() []Event) {
	var mu sync.Mutex
	var events []Event
	return func(event Event) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		}, func() []Event {
			mu.Lock()
			defer mu.Unlock()
			return append([]Event(nil), events...)
		}
}

func timelineManager(t *testing.T, chat model.BaseChatModel, newTools ToolFactory) *Manager {
	t.Helper()
	manager, err := NewManager(Config{
		NewModel: func(context.Context) (model.BaseChatModel, error) { return chat, nil },
		NewTools: newTools,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func TestObserverReceivesOrderedTimelineEvents(t *testing.T) {
	readTool, err := utils.InferTool("read_tool", "read only", func(context.Context, testArgs) (string, error) {
		return `{"ok":true,"text":"read ok","exitCode":0}`, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	chat := &testModel{step: func(_ context.Context, _ []*schema.Message, call int) (*schema.Message, error) {
		if call == 1 {
			return toolCall("r1", "read_tool"), nil
		}
		return schema.AssistantMessage("child done", nil), nil
	}}
	manager := timelineManager(t, chat, func(context.Context, Scope) ([]tool.BaseTool, error) {
		return []tool.BaseTool{readTool}, nil
	})
	observer, recorded := eventRecorder()
	handle, err := manager.Spawn(context.Background(), Request{
		Task:     "child task",
		Scope:    &Scope{AllowedTools: []string{"read_tool"}},
		Observer: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := waitResult(t, manager, handle)
	if err != nil {
		t.Fatalf("subagent failed: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %s, want %s", result.Status, StatusCompleted)
	}
	events := recorded()
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4: %+v", len(events), events)
	}
	wantKinds := []EventKind{EventToolCall, EventToolResult, EventDelta, EventDone}
	for index, kind := range wantKinds {
		if events[index].Kind != kind {
			t.Fatalf("event %d kind = %s, want %s (%+v)", index, events[index].Kind, kind, events)
		}
		if events[index].TaskID != handle.ID {
			t.Fatalf("event %d task ID = %q, want %q", index, events[index].TaskID, handle.ID)
		}
	}
	if events[0].CallID != "r1" || events[0].Name != "read_tool" {
		t.Fatalf("toolCall event = %+v", events[0])
	}
	if events[1].CallID != "r1" || !events[1].OK || events[1].Summary != "read ok" {
		t.Fatalf("toolResult event = %+v", events[1])
	}
	if events[2].Text != "child done" {
		t.Fatalf("delta event = %+v", events[2])
	}
	if events[3].Status != StatusCompleted || events[3].Summary != "child done" || events[3].Err != "" {
		t.Fatalf("done event = %+v", events[3])
	}
}

func TestObserverDoneCanceledOnParentCancel(t *testing.T) {
	started := make(chan struct{})
	var startedOnce sync.Once
	chat := &testModel{step: func(ctx context.Context, _ []*schema.Message, _ int) (*schema.Message, error) {
		startedOnce.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	manager := timelineManager(t, chat, nil)
	observer, recorded := eventRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle, err := manager.Spawn(ctx, Request{Task: "child task", Scope: &Scope{}, Observer: observer})
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, started, "child model did not start")
	cancel()
	_, err = waitResult(t, manager, handle)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want context.Canceled", err)
	}
	events := recorded()
	if len(events) == 0 || events[len(events)-1].Kind != EventDone || events[len(events)-1].Status != StatusCanceled {
		t.Fatalf("done(canceled) event missing: %+v", events)
	}
}

func TestSpawnToolCancelsChildWhenParentContextEnds(t *testing.T) {
	started := make(chan struct{})
	var startedOnce sync.Once
	chat := &testModel{step: func(ctx context.Context, _ []*schema.Message, _ int) (*schema.Message, error) {
		startedOnce.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	manager := timelineManager(t, chat, nil)
	observer, recorded := eventRecorder()
	spawnTool, err := NewSpawnTool(manager, Scope{}, func(context.Context) Observer { return observer })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type invocation struct {
		output string
		err    error
	}
	finished := make(chan invocation, 1)
	go func() {
		output, err := spawnTool.InvokableRun(ctx, `{"task":"child task"}`)
		finished <- invocation{output: output, err: err}
	}()
	waitForSignal(t, started, "child model did not start")
	cancel()
	select {
	case result := <-finished:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("spawn error = %v, want context.Canceled", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("spawn tool did not return after parent cancellation")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		manager.mu.Lock()
		var current *task
		for _, candidate := range manager.tasks {
			current = candidate
		}
		finished := current != nil && current.finished
		status := Status("")
		if current != nil {
			status = current.result.Status
		}
		manager.mu.Unlock()
		if finished {
			if status != StatusCanceled {
				t.Fatalf("child status = %s, want %s", status, StatusCanceled)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child task did not settle as canceled")
		}
		time.Sleep(10 * time.Millisecond)
	}
	events := recorded()
	if len(events) == 0 || events[len(events)-1].Kind != EventDone || events[len(events)-1].Status != StatusCanceled {
		t.Fatalf("done(canceled) event missing: %+v", events)
	}
}

func TestObserverNilIsSafeAndSpawnStillBlocksUntilDone(t *testing.T) {
	var calls atomic.Int64
	chat := &testModel{step: func(_ context.Context, _ []*schema.Message, _ int) (*schema.Message, error) {
		calls.Add(1)
		return schema.AssistantMessage("child done", nil), nil
	}}
	manager := timelineManager(t, chat, nil)
	spawnTool, err := NewSpawnTool(manager, Scope{})
	if err != nil {
		t.Fatal(err)
	}
	output, err := spawnTool.InvokableRun(context.Background(), `{"task":"child task"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "child done") {
		t.Fatalf("spawn output = %q", output)
	}
	if calls.Load() != 1 {
		t.Fatalf("child model calls = %d, want 1", calls.Load())
	}
}
