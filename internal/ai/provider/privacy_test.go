package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/privacy"
	"github.com/cloudwego/eino/schema"
)

type privacyDetectorFunc func(context.Context, string) ([]privacy.Span, error)

func (f privacyDetectorFunc) Detect(ctx context.Context, text string) ([]privacy.Span, error) {
	return f(ctx, text)
}

func privateValues(values ...string) privacy.Detector {
	return privacyDetectorFunc(func(_ context.Context, text string) ([]privacy.Span, error) {
		var spans []privacy.Span
		for _, value := range values {
			for offset := 0; offset < len(text); {
				at := strings.Index(text[offset:], value)
				if at < 0 {
					break
				}
				at += offset
				spans = append(spans, privacy.Span{Start: at, End: at + len(value), Label: "common_secret", Score: 1})
				offset = at + len(value)
			}
		}
		return spans, nil
	})
}

func privacyOption(detector privacy.Detector) Option {
	return WithPrivacy(func(context.Context) (*privacy.Exchange, error) {
		return privacy.NewScopedExchange(detector, [32]byte{1, 2, 3, 4})
	})
}

func privacyClient(t *testing.T, handler http.HandlerFunc, options ...Option) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(Config{BaseURL: server.URL + "/v1", Model: "test", Stream: true}, options...)
	if err != nil {
		t.Fatal(err)
	}
	client.retry.MaxRetries = 0
	return client
}

func writePrivacyBlock(w http.ResponseWriter, content string, calls any) {
	w.Header().Set("Content-Type", "application/json")
	message := map[string]any{"role": "assistant", "content": content}
	if calls != nil {
		message["tool_calls"] = calls
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "response", "model": "test", "choices": []any{map[string]any{"message": message, "finish_reason": "stop"}}})
}

func TestPrivacyStableOutboundHistoryAcrossTurns(t *testing.T) {
	const secret = "synthetic-private-value"
	var mu sync.Mutex
	var requests []struct {
		Messages []json.RawMessage `json:"messages"`
		Tools    []json.RawMessage `json:"tools"`
	}
	client := privacyClient(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []json.RawMessage `json:"messages"`
			Tools    []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		var user struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(request.Messages[1], &user)
		writePrivacyBlock(w, user.Content, nil)
	}, privacyOption(privateValues(secret)))
	first := ChatRequest{Messages: []ChatMessage{{Role: "system", Content: "Fixed system prompt"}, UserMessage(secret)}, Tools: []ToolSchema{{Name: "read_file", Description: "Documentation https://example.org/tool", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "enum": []string{"https://example.org/tool"}}}}}}}
	original, _ := json.Marshal(first.Messages)
	answer, err := client.ChatBlock(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	if answer.Content != secret {
		t.Fatalf("restored reply = %q", answer.Content)
	}
	second := first
	second.Messages = append(append([]ChatMessage(nil), first.Messages...), ChatMessage{Role: "assistant", Content: answer.Content}, UserMessage("continue using "+secret))
	if _, err := client.ChatBlock(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	for i, message := range requests[0].Messages {
		if !reflect.DeepEqual(message, requests[1].Messages[i]) {
			t.Fatalf("historical prefix changed at %d", i)
		}
	}
	if !reflect.DeepEqual(requests[0].Tools, requests[1].Tools) {
		t.Fatal("tool schema changed across turns")
	}
	encoded, _ := json.Marshal(requests)
	if strings.Contains(string(encoded), secret) {
		t.Fatal("plaintext escaped upstream")
	}
	after, _ := json.Marshal(first.Messages)
	if string(original) != string(after) {
		t.Fatal("caller history mutated")
	}
	var tool map[string]any
	_ = json.Unmarshal(requests[0].Tools[0], &tool)
	function := tool["function"].(map[string]any)
	if function["description"] != first.Tools[0].Description {
		t.Fatal("built-in tool description changed")
	}
}

func TestPrivacyDisabledAndFailuresBeforeNetwork(t *testing.T) {
	for _, test := range []struct {
		name      string
		option    Option
		image     bool
		wantError bool
	}{
		{name: "disabled"},
		{name: "resolver-disabled", option: WithPrivacy(func(context.Context) (*privacy.Exchange, error) { return nil, nil })},
		{name: "unavailable", option: WithPrivacy(func(context.Context) (*privacy.Exchange, error) { return nil, errors.New("model missing") }), wantError: true},
		{name: "detector-error", option: privacyOption(privacyDetectorFunc(func(context.Context, string) ([]privacy.Span, error) { return nil, errors.New("inference failed") })), wantError: true},
		{name: "media", option: privacyOption(privateValues("secret")), image: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			var received string
			var opts []Option
			if test.option != nil {
				opts = append(opts, test.option)
			}
			client := privacyClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				data, _ := io.ReadAll(r.Body)
				received = string(data)
				writePrivacyBlock(w, "ok", nil)
			}, opts...)
			message := UserMessage("secret")
			if test.image {
				message.Content = []map[string]any{{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}}}
			}
			_, err := client.ChatBlock(t.Context(), ChatRequest{Messages: []ChatMessage{message}})
			if (err != nil) != test.wantError {
				t.Fatalf("err = %v", err)
			}
			if test.wantError {
				if calls.Load() != 0 {
					t.Fatal("failed-open network request")
				}
			} else if calls.Load() != 1 || !strings.Contains(received, "secret") || strings.Contains(received, "Privacy placeholders") {
				t.Fatal("disabled behavior changed")
			}
		})
	}
}

func TestPrivacyRetryReusesProtectedRequest(t *testing.T) {
	var requests []string
	var detections atomic.Int32
	detector := privacyDetectorFunc(func(ctx context.Context, text string) ([]privacy.Span, error) {
		detections.Add(1)
		return privateValues("secret").Detect(ctx, text)
	})
	client := privacyClient(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		requests = append(requests, string(data))
		if len(requests) == 1 {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"error":{"message":"temporarily unavailable"}}`)
			return
		}
		writePrivacyBlock(w, "ok", nil)
	}, privacyOption(detector), withRetryHooks(func(context.Context, time.Duration) error { return nil }, func() float64 { return 0 }))
	client.retry.MaxRetries = 1
	if _, err := client.ChatBlock(t.Context(), ChatRequest{Messages: []ChatMessage{UserMessage("secret")}}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0] != requests[1] || strings.Contains(requests[0], "secret") {
		t.Fatal("retry changed protected payload")
	}
	if detections.Load() != 1 {
		t.Fatalf("request was re-detected on retry: %d", detections.Load())
	}
}

func TestPrivacyStreamingTextReasoningAndToolJSON(t *testing.T) {
	const secret = "私密\"\\\nvalue"
	client := privacyClient(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		marker := request.Messages[len(request.Messages)-1].Content
		if strings.Contains(marker, secret) {
			t.Error("unmasked request")
		}
		args, _ := json.Marshal(map[string]any{"command": marker, "number": json.Number("9007199254740993")})
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(delta map[string]any) {
			data, _ := json.Marshal(map[string]any{"id": "response", "model": "test", "choices": []any{map[string]any{"index": 0, "delta": delta}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		}
		for _, char := range marker {
			send(map[string]any{"content": string(char), "reasoning_content": string(char)})
		}
		for i, char := range string(args) {
			call := map[string]any{"index": 0, "function": map[string]any{"arguments": string(char)}}
			if i == 0 {
				call["id"] = "call1"
				call["type"] = "function"
				call["function"].(map[string]any)["name"] = "exec_commands"
			}
			send(map[string]any{"tool_calls": []any{call}})
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}, privacyOption(privateValues(secret)))
	chat, _ := client.ChatModel(t.Context())
	reader, err := chat.Stream(t.Context(), []*schema.Message{schema.UserMessage(secret)})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var content, reasoning strings.Builder
	var calls []schema.ToolCall
	var runID string
	for {
		frame, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content.WriteString(frame.Content)
		reasoning.WriteString(frame.ReasoningContent)
		calls = append(calls, frame.ToolCalls...)
		if runID == "" {
			runID = extraString(frame.Extra, MessageExtraRunID)
		}
		if extraString(frame.Extra, MessageExtraRunID) != runID {
			t.Fatal("restored final frame lost metadata")
		}
	}
	if content.String() != secret || reasoning.String() != secret {
		t.Fatalf("restored content/reasoning differ: %q / %q", content.String(), reasoning.String())
	}
	if len(calls) != 1 || calls[0].ID != "call1" || calls[0].Function.Name != "exec_commands" {
		t.Fatalf("calls = %+v", calls)
	}
	var args struct {
		Command string      `json:"command"`
		Number  json.Number `json:"number"`
	}
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatal(err)
	}
	if args.Command != secret || args.Number.String() != "9007199254740993" {
		t.Fatalf("restored args = %+v", args)
	}
}

func TestPrivacyGenerateRestoresToolArgumentsWithoutMutatingHistory(t *testing.T) {
	const secret = "secret-with-\"quote\\slash"
	client := privacyClient(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		marker := request.Messages[len(request.Messages)-1].Content
		arguments, _ := json.Marshal(map[string]string{"value": marker})
		writePrivacyBlock(w, marker, []any{map[string]any{"id": "call", "type": "function", "function": map[string]any{"name": "write_file", "arguments": string(arguments)}}})
	}, privacyOption(privateValues(secret)))
	chat, err := client.ChatModel(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	input := schema.UserMessage(secret)
	response, err := chat.Generate(t.Context(), []*schema.Message{input})
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != secret || input.Content != secret || len(response.ToolCalls) != 1 {
		t.Fatalf("response = %+v", response)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(response.ToolCalls[0].Function.Arguments), &args); err != nil {
		t.Fatal(err)
	}
	if args["value"] != secret {
		t.Fatalf("tool argument = %q", args["value"])
	}
}

func TestPrivacyStopsAfterConsumerCloses(t *testing.T) {
	client := privacyClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"<SECRET_\"}}]}\n\ndata: [DONE]\n\n")
	}, privacyOption(privateValues("secret")))
	stopped := errors.New("consumer stopped")
	calls := 0
	_, err := client.runNative(t.Context(), []*schema.Message{schema.UserMessage("secret")}, nil, true, func(*schema.Message) error { calls++; return stopped })
	if !errors.Is(err, stopped) || calls != 1 {
		t.Fatalf("consumer callbacks = %d, err = %v", calls, err)
	}
}

func TestPrivacyNumericToolArgumentsRemainPrivateAndRestoreExactly(t *testing.T) {
	const number = "9007199254740993"
	client := privacyClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(string(body), number) {
			t.Error("numeric secret escaped upstream")
		}
		var request struct {
			Messages []struct {
				ToolCalls []ToolCall `json:"tool_calls"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Error(err)
			return
		}
		for _, message := range request.Messages {
			if len(message.ToolCalls) > 0 {
				writePrivacyBlock(w, "", message.ToolCalls)
				return
			}
		}
		t.Error("protected historical call missing")
	}, privacyOption(privateValues(number)))
	response, err := client.ChatBlock(t.Context(), ChatRequest{Messages: []ChatMessage{{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "call", Type: "function", Function: FunctionCall{Name: "lookup", Arguments: `{"account":9007199254740993}`}}}}, {Role: "tool", Content: "ready", ToolCallID: "call"}, UserMessage("repeat")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("calls=%+v", response.ToolCalls)
	}
	var args struct {
		Account json.Number `json:"account"`
	}
	if err := json.Unmarshal([]byte(response.ToolCalls[0].Function.Arguments), &args); err != nil {
		t.Fatal(err)
	}
	if args.Account.String() != number {
		t.Fatalf("numeric argument changed: %s", response.ToolCalls[0].Function.Arguments)
	}
}

func TestPrivacySensitiveToolArgumentKeysBlockNetwork(t *testing.T) {
	var calls atomic.Int32
	client := privacyClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); writePrivacyBlock(w, "unexpected", nil) }, privacyOption(privateValues("sensitive-key")))
	_, err := client.ChatBlock(t.Context(), ChatRequest{Messages: []ChatMessage{{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "call", Type: "function", Function: FunctionCall{Name: "lookup", Arguments: `{"sensitive-key":"value"}`}}}}}})
	if err == nil || calls.Load() != 0 {
		t.Fatalf("sensitive key sent: calls=%d err=%v", calls.Load(), err)
	}
}

func TestPrivacyKnownSecretRedactedInEveryField(t *testing.T) {
	const secret = "hunter22"
	// The detector only recognizes the value when it is labelled as a password.
	detector := privacyDetectorFunc(func(_ context.Context, text string) ([]privacy.Span, error) {
		const prefix = "my password: "
		at := strings.Index(text, prefix+secret)
		if at < 0 {
			return nil, nil
		}
		start := at + len(prefix)
		return []privacy.Span{{Start: start, End: start + len(secret), Label: "common_secret", Score: 1}}, nil
	})
	var body string
	client := privacyClient(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		writePrivacyBlock(w, "ok", nil)
	}, privacyOption(detector))
	_, err := client.ChatBlock(t.Context(), ChatRequest{Messages: []ChatMessage{
		UserMessage("my password: " + secret),
		{Role: "assistant", Content: "Running echo " + secret, ToolCalls: []ToolCall{{ID: "call", Type: "function", Function: FunctionCall{Name: "exec", Arguments: `{"command":"echo ` + secret + `"}`}}}},
		{Role: "tool", Content: secret + "\n", ToolCallID: "call"},
		UserMessage("thanks"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if body == "" || strings.Contains(body, secret) {
		t.Fatalf("known secret escaped upstream: %s", body)
	}
}

func TestPrivacyEmptyToolArgumentsDoNotFailConversation(t *testing.T) {
	client := privacyClient(t, func(w http.ResponseWriter, r *http.Request) {
		writePrivacyBlock(w, "", []any{map[string]any{"id": "next", "type": "function", "function": map[string]any{"name": "list_hosts", "arguments": ""}}})
	}, privacyOption(privateValues("secret")))
	response, err := client.ChatBlock(t.Context(), ChatRequest{Messages: []ChatMessage{
		UserMessage("secret"),
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call", Type: "function", Function: FunctionCall{Name: "list_hosts", Arguments: ""}}}},
		{Role: "tool", Content: "[]", ToolCallID: "call"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Function.Name != "list_hosts" {
		t.Fatalf("calls = %+v", response.ToolCalls)
	}
}

func TestPrivacyInstructionMergedOnlyWhenRedacted(t *testing.T) {
	type wireMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var received []wireMessage
	client := privacyClient(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []wireMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		received = request.Messages
		writePrivacyBlock(w, "ok", nil)
	}, privacyOption(privateValues("secret")))
	systemCount := func() int {
		count := 0
		for _, message := range received {
			if message.Role == "system" {
				count++
			}
		}
		return count
	}
	for _, test := range []struct {
		name     string
		messages []ChatMessage
		system   int
		marked   bool
	}{
		{name: "nothing-redacted", messages: []ChatMessage{{Role: "system", Content: "Fixed"}, UserMessage("hello")}, system: 1},
		{name: "merged", messages: []ChatMessage{{Role: "system", Content: "Fixed"}, UserMessage("secret")}, system: 1, marked: true},
		{name: "inserted", messages: []ChatMessage{UserMessage("secret")}, system: 1, marked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.ChatBlock(t.Context(), ChatRequest{Messages: test.messages}); err != nil {
				t.Fatal(err)
			}
			if systemCount() != test.system {
				t.Fatalf("system messages = %+v", received)
			}
			if strings.Contains(received[0].Content, "Privacy placeholders") != test.marked {
				t.Fatalf("instruction presence wrong: %+v", received)
			}
			if test.name == "merged" && !strings.HasSuffix(received[0].Content, "Fixed") {
				t.Fatalf("system prompt lost: %q", received[0].Content)
			}
		})
	}
}

func TestPrivacyRestoredExtraReasoning(t *testing.T) {
	extra := map[string]any{reasoningExtraKey: "<SECRET_0123456789abcdef>", MessageExtraRunID: "run"}
	restored := restoredExtra(extra, "plain")
	if restored[reasoningExtraKey] != "plain" || restored[MessageExtraRunID] != "run" || extra[reasoningExtraKey] != "<SECRET_0123456789abcdef>" {
		t.Fatalf("restored extra = %+v (source %+v)", restored, extra)
	}
	if _, ok := restoredExtra(extra, "")[reasoningExtraKey]; ok {
		t.Fatal("empty restored reasoning kept the placeholder mirror")
	}
}
