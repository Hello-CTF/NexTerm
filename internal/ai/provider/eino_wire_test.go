package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAssistantToolHistoryWireCompatibility(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body struct {
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
			return nil, err
		}
		if len(body.Messages) != 2 {
			t.Fatalf("messages = %+v", body.Messages)
		}
		if content, exists := body.Messages[0]["content"]; !exists || string(content) != "null" {
			t.Errorf("assistant tool history content = %s, exists=%v", content, exists)
		}
		if callID := body.Messages[1]["tool_call_id"]; string(callID) != `"call-1"` {
			t.Errorf("tool result call ID = %s", callID)
		}
		step := blockStep("m", "ok")
		return &http.Response{
			StatusCode: step.status, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(step.body)), Request: request,
		}, nil
	})
	client := newFakeClient(t, transport, Config{Model: "m"}, 0, nil, nil)
	_, err := client.ChatBlock(context.Background(), ChatRequest{Messages: []ChatMessage{
		AssistantToolCallsMessage("", []ToolCall{{ID: "call-1", Function: FunctionCall{Name: "write", Arguments: `{}`}}}),
		ToolResultMessage("call-1", "done"),
	}})
	if err != nil {
		t.Fatal(err)
	}
}
