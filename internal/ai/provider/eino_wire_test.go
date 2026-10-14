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
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1", Function: FunctionCall{Name: "write", Arguments: `{}`}}}},
		{Role: "tool", Content: "done", ToolCallID: "call-1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestChatBlockProjectsToolResultWire(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body struct {
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 4 {
			t.Fatalf("wire messages = %+v", body.Messages)
		}
		if got := string(body.Messages[0]["content"]); got != `"用户数据保持中文"` {
			t.Errorf("user content = %s", got)
		}
		if got := string(body.Messages[3]["content"]); got != `"模型中文回答保持不变"` {
			t.Errorf("assistant content = %s", got)
		}
		var content string
		if err := json.Unmarshal(body.Messages[2]["content"], &content); err != nil {
			t.Fatal(err)
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(content), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope["text"] != "no tables" || envelope["ok"] != true || envelope["exitCode"] != float64(0) || envelope["truncated"] != false || envelope["panic"] != true {
			t.Errorf("projected envelope = %+v", envelope)
		}
		if _, exists := envelope["modelText"]; exists {
			t.Errorf("modelText leaked into wire envelope: %+v", envelope)
		}
		for _, field := range []string{"change", "question", "terminal"} {
			if _, exists := envelope[field]; !exists {
				t.Errorf("field %q was dropped: %+v", field, envelope)
			}
		}
		terminal, _ := envelope["terminal"].(map[string]any)
		if terminal["title"] != "构建机" {
			t.Errorf("real terminal title changed: %+v", terminal)
		}
		step := blockStep("m", "ok")
		return &http.Response{
			StatusCode: step.status, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(step.body)), Request: request,
		}, nil
	})
	client := newFakeClient(t, transport, Config{Model: "m"}, 0, nil, nil)
	content := `{"ok":true,"text":"无表","modelText":"no tables","exitCode":0,"truncated":false,"panic":true,"change":{"path":"中文路径"},"question":{"question":"继续？"},"terminal":{"title":"构建机"}}`
	_, err := client.ChatBlock(context.Background(), ChatRequest{Messages: []ChatMessage{
		{Role: "user", Content: "用户数据保持中文"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1", Function: FunctionCall{Name: "db_list_tables", Arguments: `{}`}}}},
		{Role: "tool", Content: content, ToolCallID: "call-1"},
		{Role: "assistant", Content: "模型中文回答保持不变"},
	}})
	if err != nil {
		t.Fatal(err)
	}
}
