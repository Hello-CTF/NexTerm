package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/usage"
)

func (c *Client) Chat(ctx context.Context, request ChatRequest, handler StreamHandler) (Completion, error) {
	if handler == nil {
		handler = func(StreamItem) {}
	}
	if c.config.Stream {
		completion, err := c.chatStream(ctx, request, handler)
		if err == nil {
			return completion, nil
		}
		if ctx.Err() != nil || !safeStreamFallback(err) {
			return Completion{}, err
		}
	}
	return c.chatBlock(ctx, request, handler)
}

func (c *Client) ChatBlock(ctx context.Context, request ChatRequest) (Completion, error) {
	return c.chatBlock(ctx, request, nil)
}

func (c *Client) chatBlock(ctx context.Context, request ChatRequest, handler StreamHandler) (Completion, error) {
	requestContext, cancel := context.WithTimeout(ctx, c.timeouts.Block)
	defer cancel()
	httpRequest, err := c.newRequest(requestContext, http.MethodPost, "/chat/completions", c.chatBody(request, false))
	if err != nil {
		return Completion{}, err
	}
	response, err := c.http.Do(httpRequest)
	if err != nil {
		return Completion{}, fmt.Errorf("AI block chat request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Completion{}, responseError("AI block chat", response)
	}

	var payload blockResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return Completion{}, fmt.Errorf("decode AI block response: %w", err)
	}
	if len(payload.Choices) == 0 {
		return Completion{}, errors.New("AI block response has no choices")
	}
	choice := payload.Choices[0]
	content, err := responseText(choice.Message.Content)
	if err != nil {
		return Completion{}, fmt.Errorf("decode AI response content: %w", err)
	}
	reasoning := choice.Message.ReasoningContent
	if reasoning == "" {
		reasoning = choice.Message.Reasoning
	}
	calls := normalizeToolCalls(choice.Message.ToolCalls)
	completion := Completion{
		Content: content, Reasoning: reasoning, ToolCalls: calls,
		FinishReason: choice.FinishReason, Usage: usage.Parse(payload.Usage),
	}
	if handler != nil {
		if err := emitBlockItems(ctx, completion, handler); err != nil {
			return Completion{}, err
		}
	}
	return completion, nil
}

func emitBlockItems(ctx context.Context, completion Completion, handler StreamHandler) error {
	if completion.Reasoning != "" {
		if err := ctx.Err(); err != nil {
			return err
		}
		handler(StreamItem{Kind: StreamReasoning, Text: completion.Reasoning})
	}
	if completion.Content != "" {
		if err := ctx.Err(); err != nil {
			return err
		}
		handler(StreamItem{Kind: StreamDelta, Text: completion.Content})
	}
	for _, call := range completion.ToolCalls {
		if err := ctx.Err(); err != nil {
			return err
		}
		handler(StreamItem{Kind: StreamToolArgs, Name: call.Function.Name, Chars: len(call.Function.Arguments)})
	}
	return nil
}

func (c *Client) chatBody(request ChatRequest, stream bool) map[string]any {
	messages := request.Messages
	if messages == nil {
		messages = []ChatMessage{}
	}
	body := map[string]any{
		"model": c.config.Model, "messages": messages, "temperature": c.config.Temperature,
	}
	if stream {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if len(request.Tools) != 0 {
		tools := make([]map[string]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters,
				},
			})
		}
		body["tools"] = tools
		if stream && wantsToolStream(c.config.BaseURL) {
			body["tool_stream"] = true
		}
	}
	return body
}

type blockResponse struct {
	Choices []struct {
		Message struct {
			Content          json.RawMessage `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			Reasoning        string          `json:"reasoning"`
			ToolCalls        []ToolCall      `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
}

func responseText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", err
	}
	var result strings.Builder
	for _, part := range parts {
		if part.Type == "" || part.Type == "text" {
			result.WriteString(part.Text)
		}
	}
	return result.String(), nil
}

func normalizeToolCalls(calls []ToolCall) []ToolCall {
	result := make([]ToolCall, len(calls))
	copy(result, calls)
	for index := range result {
		result[index].Type = "function"
	}
	return result
}

func wantsToolStream(baseURL string) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if domainMatch(host, "bigmodel.cn") || domainMatch(host, "z.ai") {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if label == "zhipuai" {
			return true
		}
	}
	return false
}

func domainMatch(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}
