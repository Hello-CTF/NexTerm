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
		{Event{Type: "confirmRequired", ID: "c", Tool: "write_file", Args: json.RawMessage(`{}`), Risk: "needs_confirm", Nonce: "n"}, `{"args":{},"confirmationNonce":"n","id":"c","preview":null,"reason":"","rendered":"","risk":"needs_confirm","tool":"write_file","type":"confirmRequired"}`},
		{Event{Type: "questionRequired", ID: "q", Nonce: "n", Question: &tools.Question{Question: "继续？"}}, `{"confirmationNonce":"n","id":"q","question":{"question":"继续？"},"type":"questionRequired"}`},
		{Event{Type: "screen", TabID: "t", Text: "screen"}, `{"tabId":"t","text":"screen","type":"screen"}`},
		{Event{Type: "fileChange", ID: "c", Path: "/a", Before: "a", After: "b"}, `{"after":"b","before":"a","id":"c","path":"/a","type":"fileChange"}`},
		{Event{Type: "usage", PromptTokens: 1, CompletionTokens: 2, CachedTokens: 3, ContextWindow: 4}, `{"cachedTokens":3,"completionTokens":2,"contextWindow":4,"promptTokens":1,"type":"usage"}`},
		{Event{Type: "usage", Model: "fallback", RunID: "run", CallID: "call", PromptTokens: 1, CompletionTokens: 2, CachedTokens: 3, ContextWindow: 4}, `{"cachedTokens":3,"callId":"call","completionTokens":2,"contextWindow":4,"model":"fallback","promptTokens":1,"runId":"run","type":"usage"}`},
		{Event{Type: "todos"}, `{"items":[],"type":"todos"}`},
		{Event{Type: "planSubmitted", Plan: "p"}, `{"plan":"p","type":"planSubmitted"}`},
		{Event{Type: "done", Answer: "a", Turns: 2, TokensIn: 3, TokensOut: 4}, `{"answer":"a","tokensIn":3,"tokensOut":4,"turns":2,"type":"done"}`},
		{Event{Type: "error", Message: "m", Retryable: true}, `{"message":"m","retryable":true,"type":"error"}`},
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
