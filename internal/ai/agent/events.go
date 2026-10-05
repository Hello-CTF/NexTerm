package agent

import (
	"encoding/json"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
)

type Event struct {
	Type                string
	Seq                 uint64
	Phase               string
	Detail              *string
	Turn                int
	Text                string
	Tool                string
	Chars               int
	ID                  string
	Name                string
	Args                json.RawMessage
	Nonce               string
	Display             string
	OK                  bool
	Summary             string
	Truncated           bool
	ExitCode            int
	Panic               bool
	Risk                string
	Rendered            string
	Reason              string
	Preview             *tools.Preview
	Question            *tools.Question
	RequestID           string
	Attempt             uint64
	TabID               string
	Path                string
	Before              string
	After               string
	PromptTokens        uint64
	CompletionTokens    uint64
	CachedTokens        uint64
	CacheCreationTokens uint64
	LatencyMS           int64
	ContextWindow       uint64
	Model               string
	RunID               string
	CallID              string
	Items               []tools.TodoItem
	Plan                string
	Answer              string
	Turns               int
	TokensIn            uint64
	TokensOut           uint64
	Message             string
	Retryable           bool
	ParentCallID        string
	SubagentID          string
	Depth               int
	Status              string
}

func (e Event) MarshalJSON() ([]byte, error) {
	payload := map[string]any{"type": e.Type, "seq": e.Seq}
	switch e.Type {
	case "status":
		payload["phase"], payload["detail"], payload["turn"] = e.Phase, e.Detail, e.Turn
	case "delta", "reasoning":
		payload["text"] = e.Text
	case "steered", "steerDropped":
		payload["text"] = e.Text
	case "toolArgs":
		payload["tool"], payload["chars"] = e.Tool, e.Chars
	case "toolCall":
		payload["id"], payload["name"], payload["display"] = e.ID, e.Name, e.Display
		payload["args"] = rawObject(e.Args)
	case "toolResult":
		payload["id"], payload["ok"], payload["summary"], payload["text"] = e.ID, e.OK, e.Summary, e.Text
		payload["truncated"], payload["exitCode"], payload["panic"] = e.Truncated, e.ExitCode, e.Panic
	case "confirmRequired":
		payload["id"], payload["tool"], payload["args"], payload["risk"] = e.ID, e.Tool, rawObject(e.Args), e.Risk
		payload["rendered"], payload["reason"], payload["preview"] = e.Rendered, e.Reason, e.Preview
		payload["confirmationNonce"] = e.Nonce
		payload["requestId"], payload["attempt"] = e.RequestID, e.Attempt
	case "questionRequired":
		payload["id"], payload["question"], payload["confirmationNonce"] = e.ID, e.Question, e.Nonce
		payload["requestId"], payload["attempt"] = e.RequestID, e.Attempt
	case "screen":
		payload["tabId"], payload["text"] = e.TabID, e.Text
	case "fileChange":
		payload["id"], payload["path"], payload["before"], payload["after"] = e.ID, e.Path, e.Before, e.After
	case "usage":
		payload["promptTokens"], payload["completionTokens"] = e.PromptTokens, e.CompletionTokens
		payload["cachedTokens"], payload["contextWindow"] = e.CachedTokens, e.ContextWindow
		if e.CacheCreationTokens != 0 {
			payload["cacheCreationTokens"] = e.CacheCreationTokens
		}
		if e.LatencyMS != 0 {
			payload["latencyMs"] = e.LatencyMS
		}
		if e.Model != "" {
			payload["model"] = e.Model
		}
		if e.RunID != "" {
			payload["runId"] = e.RunID
		}
		if e.CallID != "" {
			payload["callId"] = e.CallID
		}
	case "todos":
		if e.Items == nil {
			e.Items = []tools.TodoItem{}
		}
		payload["items"] = e.Items
	case "planSubmitted":
		payload["plan"] = e.Plan
	case "done":
		payload["answer"], payload["turns"], payload["tokensIn"], payload["tokensOut"] = e.Answer, e.Turns, e.TokensIn, e.TokensOut
	case "error":
		payload["message"], payload["retryable"] = e.Message, e.Retryable
	case "subagentDelta":
		payload["parentCallId"], payload["subagentId"], payload["depth"] = e.ParentCallID, e.SubagentID, e.Depth
		payload["text"] = e.Text
	case "subagentToolCall":
		payload["parentCallId"], payload["subagentId"], payload["depth"] = e.ParentCallID, e.SubagentID, e.Depth
		payload["id"], payload["name"] = e.ID, e.Name
	case "subagentToolResult":
		payload["parentCallId"], payload["subagentId"], payload["depth"] = e.ParentCallID, e.SubagentID, e.Depth
		payload["id"], payload["ok"], payload["summary"], payload["text"] = e.ID, e.OK, e.Summary, e.Text
		payload["truncated"], payload["exitCode"], payload["panic"] = e.Truncated, e.ExitCode, e.Panic
	case "subagentDone":
		payload["parentCallId"], payload["subagentId"], payload["depth"] = e.ParentCallID, e.SubagentID, e.Depth
		payload["status"], payload["summary"], payload["error"] = e.Status, e.Summary, e.Message
	}
	return json.Marshal(payload)
}

func rawObject(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return map[string]any{}
	}
	return value
}

func statusEvent(phase string, turn int) Event {
	return Event{Type: "status", Phase: phase, Turn: turn}
}

func doneEvent(answer string, turns int, tokensIn, tokensOut uint64) Event {
	return Event{Type: "done", Answer: answer, Turns: turns, TokensIn: tokensIn, TokensOut: tokensOut}
}

func errorEvent(err error, retryable bool) Event {
	message := "未知 AI 错误"
	if err != nil {
		message = err.Error()
	}
	return Event{Type: "error", Message: message, Retryable: retryable}
}
