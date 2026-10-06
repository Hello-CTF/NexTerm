package steer

import (
	"errors"
	"fmt"

	"github.com/cloudwego/eino/schema"
)

var ErrInvalidHistory = errors.New("invalid steered conversation history")

func ValidateHistory(messages []*schema.Message) error {
	prefix := validPrefix(messages)
	if len(prefix) != len(messages) {
		return fmt.Errorf("%w at message %d: tool calls and results must be complete, adjacent, and matched by ID", ErrInvalidHistory, len(prefix))
	}
	return nil
}

func CloneHistory(messages []*schema.Message) []*schema.Message {
	cloned := make([]*schema.Message, len(messages))
	for i, message := range messages {
		cloned[i] = cloneMessage(message)
	}
	return cloned
}

func validPrefix(messages []*schema.Message) []*schema.Message {
	valid := make([]*schema.Message, 0, len(messages))
	for i := 0; i < len(messages); {
		message := messages[i]
		if message == nil {
			break
		}
		if message.Role == schema.Tool {
			break
		}
		if message.Role != schema.Assistant && len(message.ToolCalls) != 0 {
			break
		}
		if message.Role != schema.Assistant || len(message.ToolCalls) == 0 {
			valid = append(valid, message)
			i++
			continue
		}

		remaining := make(map[string]struct{}, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			if call.ID == "" {
				return valid
			}
			if _, duplicate := remaining[call.ID]; duplicate {
				return valid
			}
			remaining[call.ID] = struct{}{}
		}
		unit := []*schema.Message{message}
		i++
		for len(remaining) != 0 && i < len(messages) {
			result := messages[i]
			if result == nil || result.Role != schema.Tool {
				return valid
			}
			if _, ok := remaining[result.ToolCallID]; !ok {
				return valid
			}
			delete(remaining, result.ToolCallID)
			unit = append(unit, result)
			i++
		}
		if len(remaining) != 0 {
			return valid
		}
		valid = append(valid, unit...)
	}
	return valid
}

func cloneMessage(message *schema.Message) *schema.Message {
	if message == nil {
		return nil
	}
	cloned := *message
	cloned.MultiContent = append([]schema.ChatMessagePart(nil), message.MultiContent...)
	cloned.UserInputMultiContent = append([]schema.MessageInputPart(nil), message.UserInputMultiContent...)
	cloned.AssistantGenMultiContent = append([]schema.MessageOutputPart(nil), message.AssistantGenMultiContent...)
	cloned.ToolCalls = append([]schema.ToolCall(nil), message.ToolCalls...)
	if message.Extra != nil {
		cloned.Extra = make(map[string]any, len(message.Extra))
		for key, value := range message.Extra {
			cloned.Extra[key] = value
		}
	}
	return &cloned
}
