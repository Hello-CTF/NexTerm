package subagent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/steer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

func TestTerminalDiscardsIncompleteToolRound(t *testing.T) {
	for _, mode := range []string{"cancellation", "failure", "interaction"} {
		t.Run(mode, func(t *testing.T) {
			const (
				maxMessages = 3
				maxBytes    = 1024
			)
			release := make(chan struct{})
			toolStarted := make(chan struct{})
			var startedOnce sync.Once
			var neverCalls atomic.Int64

			completedTool, err := utils.InferTool("complete", "complete a tool round", func(context.Context, testArgs) (string, error) {
				return "completed", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			neverTool, err := utils.InferTool("never", "must not finish the incomplete round", func(context.Context, testArgs) (string, error) {
				neverCalls.Add(1)
				return "unexpected", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var terminalTool tool.InvokableTool
			switch mode {
			case "cancellation":
				terminalTool, err = utils.InferTool("terminal", "block until cancellation", func(ctx context.Context, _ testArgs) (string, error) {
					startedOnce.Do(func() { close(toolStarted) })
					<-ctx.Done()
					return "", ctx.Err()
				})
			case "failure":
				terminalTool, err = utils.InferTool("terminal", "fail during a tool round", func(ctx context.Context, _ testArgs) (string, error) {
					startedOnce.Do(func() { close(toolStarted) })
					select {
					case <-release:
						return "", errors.New("terminal tool failure")
					case <-ctx.Done():
						return "", ctx.Err()
					}
				})
			case "interaction":
				terminalTool, err = utils.InferTool("terminal", "interrupt during a tool round", func(ctx context.Context, _ testArgs) (string, error) {
					startedOnce.Do(func() { close(toolStarted) })
					select {
					case <-release:
						return "", tool.StatefulInterrupt(ctx, "interaction required", "state")
					case <-ctx.Done():
						return "", ctx.Err()
					}
				})
			}
			if err != nil {
				t.Fatal(err)
			}

			chat := &testModel{step: func(_ context.Context, _ []*schema.Message, call int) (*schema.Message, error) {
				switch call {
				case 1:
					return toolCall("completed-call", "complete"), nil
				case 2:
					return schema.AssistantMessage(strings.Repeat("x", 4096), []schema.ToolCall{
						{ID: "unfinished-call", Type: "function", Function: schema.FunctionCall{Name: "terminal", Arguments: `{}`}},
						{ID: "never-call", Type: "function", Function: schema.FunctionCall{Name: "never", Arguments: `{}`}},
					}), nil
				default:
					t.Errorf("model reached an unexpected call %d after terminal %s", call, mode)
					return schema.AssistantMessage("unexpected", nil), nil
				}
			}}
			manager, err := NewManager(Config{
				NewModel: func(context.Context) (model.BaseChatModel, error) { return chat, nil },
				NewTools: func(context.Context, Scope) ([]tool.BaseTool, error) {
					return []tool.BaseTool{completedTool, terminalTool, neverTool}, nil
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
				Task:  "task-" + mode,
				Scope: &Scope{AllowedTools: []string{"complete", "terminal", "never"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			waitForSignal(t, toolStarted, "terminal tool did not start")
			pendingMessages, retainedMessages, retainedBytes := waitForIncompleteRound(t, manager, handle, maxMessages, maxBytes)
			if pendingMessages == 0 || retainedMessages <= maxMessages || retainedBytes <= maxBytes {
				t.Fatalf("test did not enter an oversized incomplete round: pending=%d messages=%d bytes=%d", pendingMessages, retainedMessages, retainedBytes)
			}

			switch mode {
			case "cancellation":
				if err := manager.Cancel(handle); err != nil {
					t.Fatal(err)
				}
			default:
				close(release)
			}
			result, waitErr := waitResult(t, manager, handle)
			switch mode {
			case "cancellation":
				if !errors.Is(waitErr, context.Canceled) || result.Status != StatusCanceled {
					t.Fatalf("cancellation result=%+v err=%v", result, waitErr)
				}
			case "interaction":
				if !errors.Is(waitErr, ErrInteractionUnsupported) || result.Status != StatusFailed {
					t.Fatalf("interaction result=%+v err=%v", result, waitErr)
				}
			default:
				if waitErr == nil || result.Status != StatusFailed {
					t.Fatalf("failure result=%+v err=%v", result, waitErr)
				}
			}
			assertTerminalHistoryBounded(t, manager, handle, result, maxMessages, maxBytes)
		})
	}
}

func waitForIncompleteRound(t *testing.T, manager *Manager, handle Handle, minMessages, minBytes int) (int, int, int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		current := manager.tasks[handle.ID]
		manager.mu.Unlock()
		if current == nil {
			t.Fatal("task disappeared before entering the tool round")
		}
		current.recorder.mu.Lock()
		pending := len(current.recorder.pending)
		messages := len(current.recorder.history) + pending
		bytes := historySize(current.recorder.history) + historySize(current.recorder.pending)
		oversizedSecondRound := false
		if pending > 0 && len(current.recorder.history) == minMessages {
			calls := current.recorder.pending[0].ToolCalls
			oversizedSecondRound = len(calls) == 2 && calls[0].ID == "unfinished-call" && messages > minMessages && bytes > minBytes
		}
		current.recorder.mu.Unlock()
		if oversizedSecondRound {
			return pending, messages, bytes
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for the oversized second tool round")
	return 0, 0, 0
}

func assertTerminalHistoryBounded(t *testing.T, manager *Manager, handle Handle, result Result, maxMessages, maxBytes int) {
	t.Helper()
	manager.mu.Lock()
	current := manager.tasks[handle.ID]
	manager.mu.Unlock()
	if current == nil {
		t.Fatal("finished task was not retained for inspection")
	}
	current.recorder.mu.Lock()
	pending := len(current.recorder.pending)
	remaining := len(current.recorder.remaining)
	pendingRetained := current.recorder.pending != nil
	remainingRetained := current.recorder.remaining != nil
	messages := len(current.recorder.history) + pending
	bytes := historySize(current.recorder.history) + historySize(current.recorder.pending)
	current.recorder.mu.Unlock()
	if pending != 0 || remaining != 0 || pendingRetained || remainingRetained {
		t.Fatalf("terminal recorder retained incomplete state: pending=%d remaining=%d pendingBuffer=%v remainingBuffer=%v", pending, remaining, pendingRetained, remainingRetained)
	}
	if messages > maxMessages || bytes > maxBytes {
		t.Fatalf("terminal recorder exceeds bounds: messages=%d/%d bytes=%d/%d", messages, maxMessages, bytes, maxBytes)
	}
	if len(result.History) != 3 || result.History[1].Role != schema.Assistant || result.History[2].Role != schema.Tool || !strings.Contains(result.History[2].Content, "completed") {
		t.Fatalf("completed tool round was not preserved: %#v", result.History)
	}
	if err := steer.ValidateHistory(result.History); err != nil {
		t.Fatalf("result history is invalid: %v", err)
	}
	snapshot, err := manager.Snapshot(handle)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.History) > maxMessages || historySize(snapshot.History) > maxBytes {
		t.Fatalf("snapshot exceeds bounds: messages=%d bytes=%d", len(snapshot.History), historySize(snapshot.History))
	}
	if err := steer.ValidateHistory(snapshot.History); err != nil {
		t.Fatalf("snapshot history is invalid: %v", err)
	}
}
