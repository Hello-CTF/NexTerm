package subagent

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/steer"
	"github.com/cloudwego/eino/schema"
)

type historyRecorder struct {
	mu          sync.Mutex
	maxMessages int
	maxBytes    int
	history     []*schema.Message
	pending     []*schema.Message
	remaining   map[string]struct{}
	truncated   bool
}

func newHistoryRecorder(task string, maxMessages, maxBytes int) *historyRecorder {
	recorder := &historyRecorder{maxMessages: maxMessages, maxBytes: maxBytes}
	recorder.appendUnit([]*schema.Message{schema.UserMessage(task)})
	return recorder
}

func (r *historyRecorder) record(role schema.RoleType, message *schema.Message) error {
	if message == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch role {
	case schema.Assistant:
		if len(r.pending) != 0 {
			return errors.New("subagent received a new assistant message before its tool round completed")
		}
		if len(message.ToolCalls) == 0 {
			r.appendUnit([]*schema.Message{message})
			return nil
		}
		remaining := make(map[string]struct{}, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			if call.ID == "" {
				return errors.New("subagent received an empty tool call ID")
			}
			if _, exists := remaining[call.ID]; exists {
				return errors.New("subagent received a duplicate tool call ID")
			}
			remaining[call.ID] = struct{}{}
		}
		r.pending = []*schema.Message{steer.CloneHistory([]*schema.Message{message})[0]}
		r.remaining = remaining
		return nil
	case schema.Tool:
		if len(r.pending) == 0 {
			return errors.New("subagent received an unpaired tool result")
		}
		if _, ok := r.remaining[message.ToolCallID]; !ok {
			return errors.New("subagent received a mismatched or duplicate tool result")
		}
		delete(r.remaining, message.ToolCallID)
		r.pending = append(r.pending, steer.CloneHistory([]*schema.Message{message})[0])
		if len(r.remaining) == 0 {
			unit := r.pending
			r.pending = nil
			r.remaining = nil
			r.appendUnit(unit)
		}
		return nil
	default:
		return nil
	}
}

func (r *historyRecorder) discardIncomplete() {
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.pending)
	r.pending = nil
	clear(r.remaining)
	r.remaining = nil
}

func (r *historyRecorder) appendUnit(unit []*schema.Message) {
	r.history = append(r.history, steer.CloneHistory(unit)...)
	r.cap()
}

func (r *historyRecorder) cap() {
	for len(r.history) > 0 && (len(r.history) > r.maxMessages || historySize(r.history) > r.maxBytes) {
		drop := firstUnitLen(r.history)
		clear(r.history[:drop])
		r.history = r.history[drop:]
		r.truncated = true
	}
}

func (r *historyRecorder) snapshot() ([]*schema.Message, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return steer.CloneHistory(r.history), r.truncated
}

func firstUnitLen(messages []*schema.Message) int {
	if len(messages) == 0 || messages[0] == nil || messages[0].Role != schema.Assistant || len(messages[0].ToolCalls) == 0 {
		return 1
	}
	unitLen := 1 + len(messages[0].ToolCalls)
	if unitLen > len(messages) {
		return len(messages)
	}
	return unitLen
}

func historySize(messages []*schema.Message) int {
	total := 0
	for _, message := range messages {
		encoded, err := json.Marshal(message)
		if err == nil {
			total += len(encoded)
			continue
		}
		total += len(message.Content) + len(message.ReasoningContent)
		for _, call := range message.ToolCalls {
			total += len(call.ID) + len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	return total
}
