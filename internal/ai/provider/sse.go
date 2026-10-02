package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/usage"
)

func (c *Client) chatStream(ctx context.Context, request ChatRequest, handler StreamHandler) (Completion, error) {
	requestContext, cancel := context.WithTimeout(ctx, c.timeouts.Stream)
	defer cancel()
	httpRequest, err := c.newRequest(requestContext, http.MethodPost, "/chat/completions", c.chatBody(request, true))
	if err != nil {
		return Completion{}, err
	}
	httpRequest.Header.Set("Accept", "text/event-stream")
	response, err := c.http.Do(httpRequest)
	if err != nil {
		return Completion{}, fmt.Errorf("AI stream chat request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Completion{}, responseError("AI stream chat", response)
	}

	state := streamState{pending: make(map[uint64]*pendingToolCall), handler: handler}
	err = consumeSSE(response.Body, func(data string) (bool, error) {
		return state.consume(requestContext, data)
	})
	if requestContext.Err() != nil {
		return Completion{}, requestContext.Err()
	}
	if err != nil {
		return Completion{}, err
	}
	if !state.sawPayload {
		return Completion{}, errors.New("AI stream ended without a response payload")
	}
	state.finishTools()
	return state.completion, nil
}

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
	handler    StreamHandler
	sawPayload bool
}

type streamResponse struct {
	Choices []struct {
		Delta struct {
			Content          *string `json:"content"`
			ReasoningContent *string `json:"reasoning_content"`
			Reasoning        *string `json:"reasoning"`
			ToolCalls        []struct {
				Index    uint64  `json:"index"`
				ID       *string `json:"id"`
				Function *struct {
					Name      *string `json:"name"`
					Arguments *string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
	Error json.RawMessage `json:"error"`
}

func (s *streamState) consume(ctx context.Context, data string) (bool, error) {
	data = strings.TrimSpace(data)
	if data == "" {
		return false, nil
	}
	if data == "[DONE]" {
		return true, nil
	}
	var payload streamResponse
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return false, fmt.Errorf("decode AI stream payload: %w", err)
	}
	if len(payload.Error) != 0 && string(payload.Error) != "null" {
		return false, fmt.Errorf("AI stream returned an error: %s", truncate(string(payload.Error), 500))
	}
	s.sawPayload = true
	if len(payload.Usage) != 0 {
		parsed := usage.Parse(payload.Usage)
		if parsed.HasData() {
			s.completion.Usage = parsed
		}
	}
	if len(payload.Choices) == 0 {
		return false, nil
	}
	choice := payload.Choices[0]
	if choice.FinishReason != "" {
		s.completion.FinishReason = choice.FinishReason
	}
	reasoning := choice.Delta.ReasoningContent
	if reasoning == nil {
		reasoning = choice.Delta.Reasoning
	}
	if reasoning != nil && *reasoning != "" {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		s.reasoning.WriteString(*reasoning)
		s.handler(StreamItem{Kind: StreamReasoning, Text: *reasoning})
	}
	if choice.Delta.Content != nil && *choice.Delta.Content != "" {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		s.content.WriteString(*choice.Delta.Content)
		s.handler(StreamItem{Kind: StreamDelta, Text: *choice.Delta.Content})
	}
	for _, fragment := range choice.Delta.ToolCalls {
		pending := s.pending[fragment.Index]
		if pending == nil {
			pending = &pendingToolCall{}
			s.pending[fragment.Index] = pending
		}
		if fragment.ID != nil && *fragment.ID != "" {
			pending.id = *fragment.ID
		}
		if fragment.Function == nil {
			continue
		}
		if fragment.Function.Name != nil {
			pending.name.WriteString(*fragment.Function.Name)
		}
		if fragment.Function.Arguments != nil {
			pending.arguments.WriteString(*fragment.Function.Arguments)
			if err := ctx.Err(); err != nil {
				return false, err
			}
			s.handler(StreamItem{Kind: StreamToolArgs, Name: pending.name.String(), Chars: pending.arguments.Len()})
		}
	}
	return false, nil
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

func consumeSSE(reader io.Reader, handle func(string) (bool, error)) error {
	buffered := bufio.NewReader(reader)
	var dataLines []string
	dispatch := func() (bool, error) {
		if len(dataLines) == 0 {
			return false, nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		return handle(data)
	}
	processLine := func(line string) (bool, error) {
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			return dispatch()
		}
		if strings.HasPrefix(line, ":") {
			return false, nil
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			field = line
			value = ""
		}
		if field == "data" {
			value = strings.TrimPrefix(value, " ")
			dataLines = append(dataLines, value)
		}
		return false, nil
	}

	for {
		line, readErr := buffered.ReadString('\n')
		if len(line) != 0 {
			done, err := processLine(line)
			if err != nil || done {
				return err
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return fmt.Errorf("read AI event stream: %w", readErr)
			}
			_, err := dispatch()
			return err
		}
	}
}
