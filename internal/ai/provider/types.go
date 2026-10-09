package provider

import (
	"github.com/Hello-CTF/NexTerm/internal/ai/usage"
)

type ChatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

func UserMessage(text string) ChatMessage {
	return ChatMessage{Role: "user", Content: text}
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
	Messages []ChatMessage
	Tools    []ToolSchema
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

type TestResult struct {
	ModelsOK    bool    `json:"modelsOk"`
	ModelsError *string `json:"modelsError"`
	ChatOK      bool    `json:"chatOk"`
	ChatError   *string `json:"chatError"`
}
