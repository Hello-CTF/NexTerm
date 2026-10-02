package provider

import (
	"context"
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
	completion     Completion
	content        strings.Builder
	reasoning      strings.Builder
	pending        map[uint64]*pendingToolCall
	handler        StreamHandler
	requestedModel string
	sawPayload     bool
}

func (s *streamState) consumeMessage(ctx context.Context, message *schema.Message) error {
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
		if err := ctx.Err(); err != nil {
			return err
		}
		s.reasoning.WriteString(message.ReasoningContent)
		s.handler(StreamItem{Kind: StreamReasoning, Text: message.ReasoningContent})
	}
	if message.Content != "" {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.content.WriteString(message.Content)
		s.handler(StreamItem{Kind: StreamDelta, Text: message.Content})
	}
	for _, fragment := range message.ToolCalls {
		index := uint64(0)
		if fragment.Index != nil {
			index = uint64(max(*fragment.Index, 0))
		}
		pending := s.pending[index]
		if pending == nil {
			pending = &pendingToolCall{}
			s.pending[index] = pending
		}
		if fragment.ID != "" {
			pending.id = fragment.ID
		}
		pending.name.WriteString(fragment.Function.Name)
		if fragment.Function.Arguments != "" {
			pending.arguments.WriteString(fragment.Function.Arguments)
			if err := ctx.Err(); err != nil {
				return err
			}
			s.handler(StreamItem{Kind: StreamToolArgs, Name: pending.name.String(), Chars: pending.arguments.Len()})
		}
	}
	return nil
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
	if s.completion.Model == "" {
		s.completion.Model = s.requestedModel
	}
	s.completion.Usage.Model = s.completion.Model
}
