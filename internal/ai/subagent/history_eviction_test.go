package subagent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/steer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

func TestCompletedHistoryEvictionClearsBackingSlots(t *testing.T) {
	const (
		maxMessages = 3
		maxBytes    = 16384
	)
	secondModelCall := make(chan struct{})
	releaseFinal := make(chan struct{})
	var secondOnce sync.Once

	completedTool, err := utils.InferTool("complete", "complete a tool round", func(context.Context, testArgs) (string, error) {
		return "completed", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	chat := &testModel{step: func(ctx context.Context, _ []*schema.Message, call int) (*schema.Message, error) {
		switch call {
		case 1:
			return toolCall("completed-call", "complete"), nil
		case 2:
			secondOnce.Do(func() { close(secondModelCall) })
			select {
			case <-releaseFinal:
				return schema.AssistantMessage("final", nil), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		default:
			t.Errorf("unexpected model call %d", call)
			return schema.AssistantMessage("unexpected", nil), nil
		}
	}}
	manager, err := NewManager(Config{
		NewModel: func(context.Context) (model.BaseChatModel, error) { return chat, nil },
		NewTools: func(context.Context, Scope) ([]tool.BaseTool, error) {
			return []tool.BaseTool{completedTool}, nil
		},
		MaxHistoryMessages: maxMessages,
		MaxHistoryBytes:    maxBytes,
		MaxRunTime:         5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	handle, err := manager.Spawn(context.Background(), Request{
		Task:  strings.Repeat("oversized-user-input-", 300),
		Scope: &Scope{AllowedTools: []string{"complete"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, secondModelCall, "second model call did not start")

	manager.mu.Lock()
	current := manager.tasks[handle.ID]
	manager.mu.Unlock()
	if current == nil {
		t.Fatal("active task was not registered")
	}
	waitForHistoryLength(t, current, maxMessages)
	current.recorder.mu.Lock()
	expanded := make([]*schema.Message, len(current.recorder.history), len(current.recorder.history)+4)
	copy(expanded, current.recorder.history)
	current.recorder.history = expanded
	backing := expanded[:cap(expanded)]
	evicted := backing[0]
	current.recorder.mu.Unlock()
	if evicted == nil || !strings.Contains(evicted.Content, "oversized-user-input") {
		t.Fatal("pre-eviction slot did not contain the oversized user message")
	}
	close(releaseFinal)

	result, err := waitResult(t, manager, handle)
	if err != nil {
		t.Fatalf("finished task failed: %v (%s)", err, result.Error)
	}
	if backing[0] != nil {
		t.Fatalf("evicted backing slot still retains %q", backing[0].Content)
	}
	for index := 1; index <= 3; index++ {
		if backing[index] == nil {
			t.Fatalf("surviving backing slot %d was cleared", index)
		}
	}
	current.recorder.mu.Lock()
	retained := append([]*schema.Message(nil), current.recorder.history...)
	pending := current.recorder.pending
	remaining := current.recorder.remaining
	current.recorder.mu.Unlock()
	if len(retained) != maxMessages || historySize(retained) > maxBytes {
		t.Fatalf("retained history exceeds bounds: messages=%d bytes=%d", len(retained), historySize(retained))
	}
	for index := range retained {
		if retained[index] != backing[index+1] {
			t.Fatalf("retained message %d does not match its surviving backing slot", index)
		}
	}
	if pending != nil || remaining != nil {
		t.Fatal("terminal incomplete-round cleanup regressed")
	}
	if !result.HistoryTruncated || len(result.History) != maxMessages || result.History[2].Content != "final" {
		t.Fatalf("unexpected public result history: truncated=%v history=%#v", result.HistoryTruncated, result.History)
	}
	if err := steer.ValidateHistory(result.History); err != nil {
		t.Fatalf("surviving history is not paired: %v", err)
	}
	if managerTask := manager.tasks[handle.ID]; managerTask != current || !current.finished {
		t.Fatal("regression requires the same finished task to remain registered")
	}
}

func waitForHistoryLength(t *testing.T, current *task, expected int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current.recorder.mu.Lock()
		length := len(current.recorder.history)
		current.recorder.mu.Unlock()
		if length == expected {
			return
		}
		if length > expected {
			t.Fatalf("history length advanced past pre-eviction state: %d", length)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for pre-eviction history length %d", expected)
}
