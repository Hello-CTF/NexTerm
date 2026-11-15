package provider

import (
	"slices"
	"sort"
	"strings"

	"github.com/cloudwego/eino/schema"
)

type pendingToolCall struct {
	id        string
	name      strings.Builder
	arguments strings.Builder
}

type streamState struct {
	completion Completion
	content    strings.Builder
	reasoning  strings.Builder
	pending    map[uint64]*pendingToolCall
	sawPayload bool
	// lastIndex is the call that fragments without an index continue.
	lastIndex uint64
	hasLast   bool
}

// resolveToolIndex picks the call a fragment belongs to. Gateways that omit
// the index on parallel calls are told apart by ID: a new non-empty ID opens
// a call, while a fragment without an ID continues the latest one.
func (s *streamState) resolveToolIndex(fragment schema.ToolCall) uint64 {
	if fragment.Index != nil {
		return uint64(max(*fragment.Index, 0))
	}
	if s.hasLast {
		current := s.pending[s.lastIndex]
		if fragment.ID == "" || current == nil || current.id == "" || current.id == fragment.ID {
			return s.lastIndex
		}
	}
	next := uint64(0)
	for index := range s.pending {
		next = max(next, index+1)
	}
	return next
}

func (s *streamState) consumeMessage(message *schema.Message) {
	s.sawPayload = true
	if message.ResponseMeta != nil {
		if message.ResponseMeta.FinishReason != "" {
			s.completion.FinishReason = message.ResponseMeta.FinishReason
		}
		if parsed := usageFromNative(message.ResponseMeta.Usage); parsed.HasData() {
			s.completion.Usage = parsed
		}
	}
	if message.ReasoningContent != "" {
		s.reasoning.WriteString(message.ReasoningContent)
	}
	if message.Content != "" {
		s.content.WriteString(message.Content)
	}
	cloned := false
	for position, fragment := range message.ToolCalls {
		index := s.resolveToolIndex(fragment)
		if fragment.Index == nil {
			// Downstream schema.ConcatMessages keeps index-less chunks as
			// separate calls, so publish the resolved index.
			if !cloned {
				message.ToolCalls = slices.Clone(message.ToolCalls)
				cloned = true
			}
			resolved := int(index)
			message.ToolCalls[position].Index = &resolved
		}
		s.lastIndex, s.hasLast = index, true
		pending := s.pending[index]
		if pending == nil {
			pending = &pendingToolCall{}
			s.pending[index] = pending
		}
		if fragment.ID != "" {
			pending.id = fragment.ID
		}
		pending.name.WriteString(fragment.Function.Name)
		pending.arguments.WriteString(fragment.Function.Arguments)
	}
}

func (s *streamState) partialCompletion() Completion {
	s.completion.Content = s.content.String()
	s.completion.Reasoning = s.reasoning.String()
	return s.completion
}

func (s *streamState) finishTools() {
	indexes := make([]uint64, 0, len(s.pending))
	for index := range s.pending {
		indexes = append(indexes, index)
	}
	sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })
	s.completion.ToolCalls = make([]ToolCall, 0, len(indexes))
	for _, index := range indexes {
		pending := s.pending[index]
		s.completion.ToolCalls = append(s.completion.ToolCalls, ToolCall{
			ID: pending.id, Type: "function",
			Function: FunctionCall{Name: pending.name.String(), Arguments: pending.arguments.String()},
		})
	}
	s.completion.Content = s.content.String()
	s.completion.Reasoning = s.reasoning.String()
}
