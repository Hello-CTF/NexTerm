package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamFragmentedCRLFReasoningToolsAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["stream"] != true || body["stream_options"] == nil || body["tools"] == nil {
			t.Errorf("stream request body = %+v", body)
		}
		if _, exists := body["tool_stream"]; exists {
			t.Error("non-Zhipu endpoint received tool_stream")
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		response := "" +
			"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\",\"content\":\"Hel\"}}]}\r\n\r\n" +
			"data:{\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\r\n\r\n" +
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call_2\",\"function\":{\"name\":\"second\",\"arguments\":\"{}\"}},{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"wr\",\"arguments\":\"{\\\"path\\\":\"}}]}}]}\r\n\r\n" +
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"ite\",\"arguments\":\"\\\"a\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\r\n\r\n" +
			"data: {\"usage\":{\"prompt_tokens\":3,\r\n" +
			"data: \"completion_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":1}}}\r\n\r\n" +
			"data: [DONE]\r\n\r\n"
		for offset := 0; offset < len(response); offset += 7 {
			end := min(offset+7, len(response))
			if _, err := writer.Write([]byte(response[offset:end])); err != nil {
				return
			}
			flusher.Flush()
		}
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL + "/v1", Model: "m", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	var items []StreamItem
	completion, err := client.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{UserMessage("hello")},
		Tools:    []ToolSchema{{Name: "write", Parameters: map[string]any{"type": "object"}}},
	}, func(item StreamItem) { items = append(items, item) })
	if err != nil {
		t.Fatal(err)
	}
	if completion.Content != "Hello" || completion.Reasoning != "thinking" || completion.FinishReason != "tool_calls" {
		t.Fatalf("completion = %+v", completion)
	}
	usage := completion.Usage
	if usage.Model != "m" || usage.PromptTokens != 3 || usage.CompletionTokens != 2 || usage.CachedTokens != 1 || usage.ContextWindow != 1000 || usage.RunID == "" || usage.CallID == "" || usage.RunID != completion.RunID || usage.CallID != completion.CallID {
		t.Fatalf("usage = %+v, completion correlation = %s/%s", usage, completion.RunID, completion.CallID)
	}
	wantCalls := []ToolCall{
		{ID: "call_1", Type: "function", Function: FunctionCall{Name: "write", Arguments: `{"path":"a"}`}},
		{ID: "call_2", Type: "function", Function: FunctionCall{Name: "second", Arguments: `{}`}},
	}
	if !reflect.DeepEqual(completion.ToolCalls, wantCalls) {
		t.Fatalf("tool calls = %#v, want %#v", completion.ToolCalls, wantCalls)
	}
	wantItems := []StreamItem{
		{Kind: StreamReasoning, Text: "thinking"},
		{Kind: StreamDelta, Text: "Hel"},
		{Kind: StreamDelta, Text: "lo"},
		{Kind: StreamToolArgs, Name: "second", Chars: 2},
		{Kind: StreamToolArgs, Name: "wr", Chars: 8},
		{Kind: StreamToolArgs, Name: "write", Chars: 12},
	}
	if !reflect.DeepEqual(items, wantItems) {
		t.Fatalf("stream items = %#v, want %#v", items, wantItems)
	}
}

func TestStreamProcessesFinalDataAtEOFWithoutDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`data: {"choices":[{"delta":{"content":"end"}}]}`))
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Model: "m", Stream: true})
	var deltas []string
	completion, err := client.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{UserMessage("hi")}}, func(item StreamItem) {
		if item.Kind == StreamDelta {
			deltas = append(deltas, item.Text)
		}
	})
	if err != nil || completion.Content != "end" || !reflect.DeepEqual(deltas, []string{"end"}) {
		t.Fatalf("Chat() = %+v, %v, deltas=%v", completion, err, deltas)
	}
}

func TestStreamRejectsEmptyOrMalformedPayloads(t *testing.T) {
	for name, response := range map[string]string{
		"done only":      "data: [DONE]\n\n",
		"malformed JSON": "data: nope\n\n",
		"provider error": "data: {\"error\":{\"message\":\"bad generation\"}}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(response))
			}))
			defer server.Close()
			client, _ := NewClient(Config{BaseURL: server.URL, Model: "m", Stream: true})
			if _, err := client.Chat(context.Background(), ChatRequest{}, nil); err == nil {
				t.Fatal("invalid stream succeeded")
			}
		})
	}
}

func TestBlockChatWithReasoningImagesToolsAndEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for _, key := range []string{"stream", "stream_options", "tool_stream"} {
			if _, exists := body[key]; exists {
				t.Errorf("block body contains %s", key)
			}
		}
		messages := body["messages"].([]any)
		parts := messages[0].(map[string]any)["content"].([]any)
		if len(parts) != 2 || parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,AA==" {
			t.Errorf("image parts = %+v", parts)
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"answer","reasoning_content":"because","tool_calls":[{"id":"call","type":"ignored","function":{"name":"write","arguments":"{\"x\":1}"}}]},"finish_reason":"tool_calls"}],"usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":5}}`))
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Model: "m", Stream: false})
	var items []StreamItem
	completion, err := client.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{UserMessageWithImages("look", []string{"AA=="})},
		Tools:    []ToolSchema{{Name: "write"}},
	}, func(item StreamItem) { items = append(items, item) })
	if err != nil {
		t.Fatal(err)
	}
	if completion.Content != "answer" || completion.Reasoning != "because" || completion.FinishReason != "tool_calls" || completion.Usage.CachedTokens != 5 {
		t.Fatalf("completion = %+v", completion)
	}
	if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].Type != "function" {
		t.Fatalf("tool calls = %+v", completion.ToolCalls)
	}
	wantItems := []StreamItem{
		{Kind: StreamReasoning, Text: "because"},
		{Kind: StreamDelta, Text: "answer"},
		{Kind: StreamToolArgs, Name: "write", Chars: 7},
	}
	if !reflect.DeepEqual(items, wantItems) {
		t.Fatalf("block items = %#v, want %#v", items, wantItems)
	}
}

func TestSafeStreamFallbackEmitsOnlyBlockOutput(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		if call == 1 {
			if body["stream"] != true {
				t.Error("first request was not streaming")
			}
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"message":"stream unsupported"}}`))
			return
		}
		if _, exists := body["stream"]; exists {
			t.Error("fallback request was streaming")
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"fallback","reasoning_content":"block reason"}}]}`))
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Model: "m", Stream: true})
	var items []StreamItem
	completion, err := client.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{UserMessage("hi")}}, func(item StreamItem) {
		items = append(items, item)
	})
	if err != nil || completion.Content != "fallback" || calls.Load() != 2 {
		t.Fatalf("Chat() = %+v, %v, calls=%d", completion, err, calls.Load())
	}
	want := []StreamItem{{Kind: StreamReasoning, Text: "block reason"}, {Kind: StreamDelta, Text: "fallback"}}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("fallback items = %#v, want %#v", items, want)
	}
}

func TestServerRetryAndAmbiguousStreamSafety(t *testing.T) {
	t.Run("server failure retries same model", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = writer.Write([]byte(`{"error":{"message":"maybe generated"}}`))
		}))
		defer server.Close()
		client, _ := NewClient(Config{BaseURL: server.URL, Model: "m", Stream: true}, withRetryHooks(
			func(context.Context, time.Duration) error { return nil }, func() float64 { return 0.5 },
		))
		_, err := client.Chat(context.Background(), ChatRequest{}, nil)
		if status, ok := statusCodeForError(err); !ok || status != 500 || calls.Load() != 3 {
			t.Fatalf("error = %v (status=%d, ok=%v), calls = %d", err, status, ok, calls.Load())
		}
	})
	t.Run("partial output then protocol error", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\ndata: invalid\n\n"))
		}))
		defer server.Close()
		client, _ := NewClient(Config{BaseURL: server.URL, Model: "m", Stream: true})
		var items []StreamItem
		_, err := client.Chat(context.Background(), ChatRequest{}, func(item StreamItem) { items = append(items, item) })
		if err == nil || calls.Load() != 1 {
			t.Fatalf("error = %v, calls = %d", err, calls.Load())
		}
		if !reflect.DeepEqual(items, []StreamItem{{Kind: StreamDelta, Text: "partial"}}) {
			t.Fatalf("partial items were duplicated: %#v", items)
		}
	})
}

func TestStreamCancellationStopsRequestWithoutFallback(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
		writer.(http.Flusher).Flush()
		select {
		case <-request.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Model: "m", Stream: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var items []StreamItem
	_, err := client.Chat(ctx, ChatRequest{}, func(item StreamItem) {
		items = append(items, item)
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("cancellation triggered %d requests", calls.Load())
	}
	if !reflect.DeepEqual(items, []StreamItem{{Kind: StreamDelta, Text: "partial"}}) {
		t.Fatalf("items after cancellation = %#v", items)
	}
}

func TestStreamToolArgumentSizeUsesUTF8Bytes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"write\",\"arguments\":\"{\\\"text\\\":\\\"你\"}}]}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	client, _ := NewClient(Config{BaseURL: server.URL, Model: "m", Stream: true})
	var progress []StreamItem
	_, err := client.Chat(context.Background(), ChatRequest{}, func(item StreamItem) {
		if item.Kind == StreamToolArgs {
			progress = append(progress, item)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := len(`{"text":"你`)
	if len(progress) != 1 || progress[0].Chars != wantBytes {
		t.Fatalf("tool progress = %+v, want %d bytes", progress, wantBytes)
	}
	if !strings.Contains(string(mustJSON(progress[0].Name)), "write") {
		t.Fatal("missing tool name")
	}
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}
