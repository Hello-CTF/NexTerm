package provider

import (
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/usage"
	"github.com/eino-contrib/jsonschema"
)

type ChatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

func SystemMessage(text string) ChatMessage {
	return ChatMessage{Role: "system", Content: text}
}

func UserMessage(text string) ChatMessage {
	return ChatMessage{Role: "user", Content: text}
}

func UserMessageWithImages(text string, images []string) ChatMessage {
	if len(images) == 0 {
		return UserMessage(text)
	}
	parts := make([]any, 0, len(images)+1)
	parts = append(parts, map[string]any{"type": "text", "text": text})
	for _, image := range images {
		url := image
		if !strings.HasPrefix(url, "data:") {
			url = "data:image/png;base64," + url
		}
		parts = append(parts, map[string]any{
			"type": "image_url", "image_url": map[string]string{"url": url},
		})
	}
	return ChatMessage{Role: "user", Content: parts}
}

func AssistantMessage(text string) ChatMessage {
	return ChatMessage{Role: "assistant", Content: text}
}

func AssistantToolCallsMessage(content string, calls []ToolCall) ChatMessage {
	var body any = content
	if content == "" {
		body = nil
	}
	return ChatMessage{Role: "assistant", Content: body, ToolCalls: calls}
}

func ToolResultMessage(callID, text string) ChatMessage {
	return ChatMessage{Role: "tool", Content: text, ToolCallID: callID}
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type ToolSchema struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type ChatRequest struct {
	Messages       []ChatMessage
	Tools          []ToolSchema
	ResponseFormat *ResponseFormat
}

type ResponseFormat struct {
	Name        string
	Description string
	Schema      *jsonschema.Schema
	Strict      bool
}

type Completion struct {
	RunID        string
	CallID       string
	Model        string
	Content      string
	Reasoning    string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        usage.Usage
}

type StreamKind string

const (
	StreamDelta     StreamKind = "delta"
	StreamReasoning StreamKind = "reasoning"
	StreamToolArgs  StreamKind = "toolArgs"
)

type StreamItem struct {
	Kind  StreamKind
	Text  string
	Name  string
	Chars int
}

type StreamHandler func(StreamItem)

type TestResult struct {
	ModelsOK    bool    `json:"modelsOk"`
	ModelsError *string `json:"modelsError"`
	ChatOK      bool    `json:"chatOk"`
	ChatError   *string `json:"chatError"`
}
