// Package compact holds the message-budget helpers shared by the agent and
// its subagents. It is a leaf package so both can import it.
package compact

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// DefaultContextWindow applies when neither the profile nor the model reports
// a window. A too-large guess delays trimming until the provider rejects the
// request, so the guess is the common conservative window.
const DefaultContextWindow uint64 = 128000

// Window returns window, or DefaultContextWindow when it is zero.
func Window(window uint64) uint64 {
	if window == 0 {
		return DefaultContextWindow
	}
	return window
}

func FitMessageBudget(messages []*schema.Message, window uint64) ([]*schema.Message, bool, error) {
	if window == 0 {
		return messages, false, nil
	}
	messages = append([]*schema.Message(nil), messages...)
	limit := int(window * 3)
	compacted := false
	for EstimatedMessages(messages) > limit {
		changed := false
		kept := 0
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role != schema.Tool {
				continue
			}
			kept++
			if kept > 4 && messages[i].Content != "[Earlier tool output omitted]" {
				messages[i].Content = "[Earlier tool output omitted]"
				compacted = true
				changed = true
				break
			}
		}
		if !changed {
			break
		}
	}
	for EstimatedMessages(messages) > limit && len(messages) > 2 {
		var dropped bool
		messages, dropped = DropOldestMessageUnit(messages)
		if !dropped {
			break
		}
		compacted = true
	}
	if EstimatedMessages(messages) > limit {
		return nil, compacted, fmt.Errorf("当前输入与系统上下文超过模型上下文上限（%d > %d 字节），请缩短输入或增大 contextWindow", EstimatedMessages(messages), limit)
	}
	return messages, compacted, nil
}

func DropOldestMessageUnit(messages []*schema.Message) ([]*schema.Message, bool) {
	start := 0
	for start < len(messages) && messages[start].Role == schema.System {
		start++
	}
	latestUser := -1
	for index := len(messages) - 1; index >= start; index-- {
		if messages[index].Role == schema.User {
			latestUser = index
			break
		}
	}
	for index := start; index < len(messages); index++ {
		if messages[index].Role == schema.User && index == latestUser {
			continue
		}
		if messages[index].Role == schema.Assistant && len(messages[index].ToolCalls) > 0 {
			end := index + 1
			for end < len(messages) && messages[end].Role == schema.Tool {
				end++
			}
			return append(messages[:index], messages[end:]...), true
		}
		if messages[index].Role == schema.Tool && index > start && messages[index-1].Role == schema.Assistant && len(messages[index-1].ToolCalls) > 0 {
			start := index - 1
			end := index + 1
			for end < len(messages) && messages[end].Role == schema.Tool {
				end++
			}
			return append(messages[:start], messages[end:]...), true
		}
		return append(messages[:index], messages[index+1:]...), true
	}
	return messages, false
}

func EstimatedMessages(messages []*schema.Message) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content) + len(message.ReasoningContent)
		for _, call := range message.ToolCalls {
			total += len(call.Function.Arguments)
		}
		if len(message.UserInputMultiContent) != 0 {
			encoded, _ := json.Marshal(message.UserInputMultiContent)
			total += len(encoded)
		}
	}
	return total
}

// BudgetHandler trims the model input to the context window before each model
// call without summarizing: oldest tool outputs are blanked, then oldest whole
// call/result units are dropped, so tool calls stay paired with their results.
type BudgetHandler struct {
	*adk.BaseChatModelAgentMiddleware
	window uint64
}

// NewBudgetHandler returns a handler bounded by window (default when zero).
func NewBudgetHandler(window uint64) *BudgetHandler {
	return &BudgetHandler{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, window: Window(window)}
}

func (h *BudgetHandler) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	fitted, compacted, err := FitMessageBudget(state.Messages, h.window)
	if err != nil {
		return ctx, nil, err
	}
	if !compacted {
		return ctx, state, nil
	}
	final := *state
	final.Messages = fitted
	return ctx, &final, nil
}
