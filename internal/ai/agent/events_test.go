package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
)

func TestEventGoldenJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		event Event
		json  string
	}{
		{Event{Type: "status", Phase: "thinking", Turn: 2}, `{"detail":null,"phase":"thinking","turn":2,"type":"status"}`},
		{Event{Type: "delta", Text: "a"}, `{"text":"a","type":"delta"}`},
		{Event{Type: "reasoning", Text: "r"}, `{"text":"r","type":"reasoning"}`},
		{Event{Type: "toolArgs", Tool: "write_file", Chars: 42}, `{"chars":42,"tool":"write_file","type":"toolArgs"}`},
		{Event{Type: "toolCall", ID: "c", Name: "edit_file", Args: json.RawMessage(`{"old_string":"a","replace_all":false}`), Display: "edit"}, `{"args":{"old_string":"a","replace_all":false},"display":"edit","id":"c","name":"edit_file","type":"toolCall"}`},
		{Event{Type: "toolResult", ID: "c", OK: true, Summary: "s", Text: "full", ExitCode: 0}, `{"exitCode":0,"id":"c","ok":true,"summary":"s","text":"full","truncated":false,"type":"toolResult"}`},
		{Event{Type: "confirmRequired", ID: "c", Tool: "write_file", Args: json.RawMessage(`{}`), Risk: "needs_confirm", Nonce: "n", RequestID: "itr_1", Attempt: 2}, `{"args":{},"attempt":2,"confirmationNonce":"n","id":"c","preview":null,"reason":"","rendered":"","requestId":"itr_1","risk":"needs_confirm","tool":"write_file","type":"confirmRequired"}`},
		{Event{Type: "questionRequired", ID: "q", Nonce: "n", Question: &tools.Question{Question: "继续？"}, RequestID: "itr_2", Attempt: 1}, `{"attempt":1,"confirmationNonce":"n","id":"q","question":{"question":"继续？"},"requestId":"itr_2","type":"questionRequired"}`},
		{Event{Type: "screen", TabID: "t", Text: "screen"}, `{"tabId":"t","text":"screen","type":"screen"}`},
		{Event{Type: "fileChange", ID: "c", Path: "/a", Before: "a", After: "b"}, `{"after":"b","before":"a","id":"c","path":"/a","type":"fileChange"}`},
		{Event{Type: "usage", PromptTokens: 1, CompletionTokens: 2, CachedTokens: 3, ContextWindow: 4}, `{"cachedTokens":3,"completionTokens":2,"contextWindow":4,"promptTokens":1,"type":"usage"}`},
		{Event{Type: "usage", Model: "fallback", RunID: "run", CallID: "call", PromptTokens: 1, CompletionTokens: 2, CachedTokens: 3, ContextWindow: 4}, `{"cachedTokens":3,"callId":"call","completionTokens":2,"contextWindow":4,"model":"fallback","promptTokens":1,"runId":"run","type":"usage"}`},
		{Event{Type: "todos"}, `{"items":[],"type":"todos"}`},
		{Event{Type: "planSubmitted", Plan: "p"}, `{"plan":"p","type":"planSubmitted"}`},
		{Event{Type: "done", Answer: "a", Turns: 2, TokensIn: 3, TokensOut: 4}, `{"answer":"a","tokensIn":3,"tokensOut":4,"turns":2,"type":"done"}`},
		{Event{Type: "error", Message: "m", Retryable: true}, `{"message":"m","retryable":true,"type":"error"}`},
		{Event{Type: "subagentDelta", ParentCallID: "sp", SubagentID: "sub", Depth: 1, Text: "d"}, `{"depth":1,"parentCallId":"sp","subagentId":"sub","text":"d","type":"subagentDelta"}`},
		{Event{Type: "subagentToolCall", ParentCallID: "sp", SubagentID: "sub", Depth: 1, ID: "c", Name: "list_assets"}, `{"depth":1,"id":"c","name":"list_assets","parentCallId":"sp","subagentId":"sub","type":"subagentToolCall"}`},
		{Event{Type: "subagentToolResult", ParentCallID: "sp", SubagentID: "sub", Depth: 1, ID: "c", OK: true, Summary: "s", Text: "t", ExitCode: 0}, `{"depth":1,"exitCode":0,"id":"c","ok":true,"parentCallId":"sp","subagentId":"sub","summary":"s","text":"t","truncated":false,"type":"subagentToolResult"}`},
		{Event{Type: "subagentDone", ParentCallID: "sp", SubagentID: "sub", Depth: 1, Status: "completed", Summary: "s"}, `{"depth":1,"error":"","parentCallId":"sp","status":"completed","subagentId":"sub","summary":"s","type":"subagentDone"}`},
	}
	for _, test := range tests {
		test.event.Seq = 1
		encoded, err := json.Marshal(test.event)
		if err != nil {
			t.Fatal(err)
		}
		var got, want map[string]any
		if json.Unmarshal(encoded, &got) != nil || json.Unmarshal([]byte(test.json), &want) != nil {
			t.Fatal("invalid golden JSON")
		}
		want["seq"] = float64(1)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %s\nwant %s", test.event.Type, encoded, test.json)
		}
	}
}
