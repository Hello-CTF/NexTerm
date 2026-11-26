package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type anthropicExchange struct {
	path    string
	headers http.Header
	body    map[string]any
	raw     []byte
}

// fakeAnthropic answers each Messages request with the next script; a script
// is a status and either an SSE event list or a raw body.
type fakeAnthropic struct {
	t       *testing.T
	mu      sync.Mutex
	seen    []anthropicExchange
	scripts []func(http.ResponseWriter, *http.Request)
}

func (f *fakeAnthropic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	index := len(f.seen)
	f.seen = append(f.seen, anthropicExchange{path: r.URL.Path, headers: r.Header.Clone(), body: body, raw: raw})
	var script func(http.ResponseWriter, *http.Request)
	if index < len(f.scripts) {
		script = f.scripts[index]
	}
	f.mu.Unlock()
	if script == nil {
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"unexpected request"}}`, http.StatusInternalServerError)
		return
	}
	script(w, r)
}

func (f *fakeAnthropic) requests() []anthropicExchange {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]anthropicExchange(nil), f.seen...)
}

func sseScript(events ...string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, event := range events {
			var head struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal([]byte(event), &head)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", head.Type, event)
		}
	}
}

func errorScript(status int, kind, message string, header http.Header) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		for key, values := range header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		encoded, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": message}})
		_, _ = w.Write(encoded)
	}
}

func messageStart(model string, input, cacheRead, cacheWrite int) string {
	return fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"output_tokens":1}}}`, model, input, cacheRead, cacheWrite)
}

func blockStart(index int, block string) string {
	return fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":%s}`, index, block)
}

func blockDelta(index int, delta string) string {
	return fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":%s}`, index, delta)
}

func blockStop(index int) string {
	return fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, index)
}

func messageDelta(stop string, output int) string {
	return fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":%d}}`, stop, output)
}

const messageStop = `{"type":"message_stop"}`

// thinkingToolTurn streams a thinking block, a note and two parallel tool calls.
func thinkingToolTurn() func(http.ResponseWriter, *http.Request) {
	return sseScript(
		messageStart("claude-opus-5-5", 10, 100, 20),
		blockStart(0, `{"type":"thinking","thinking":"","signature":""}`),
		blockDelta(0, `{"type":"thinking_delta","thinking":"Need the file "}`),
		blockDelta(0, `{"type":"thinking_delta","thinking":"and the date."}`),
		blockDelta(0, `{"type":"signature_delta","signature":"sig-one"}`),
		blockStop(0),
		blockStart(1, `{"type":"text","text":""}`),
		blockDelta(1, `{"type":"text_delta","text":"Checking both."}`),
		blockStop(1),
		blockStart(2, `{"type":"tool_use","id":"toolu_a","name":"read_file","input":{}}`),
		blockDelta(2, `{"type":"input_json_delta","partial_json":"{\"path\":"}`),
		blockDelta(2, `{"type":"input_json_delta","partial_json":"\"/etc/hosts\"}"}`),
		blockStop(2),
		blockStart(3, `{"type":"tool_use","id":"toolu_b","name":"now","input":{}}`),
		blockStop(3),
		messageDelta("tool_use", 42),
		messageStop,
	)
}

func textTurn(text string) func(http.ResponseWriter, *http.Request) {
	return sseScript(
		messageStart("claude-opus-5-5", 5, 0, 0),
		blockStart(0, `{"type":"text","text":""}`),
		blockDelta(0, fmt.Sprintf(`{"type":"text_delta","text":%q}`, text)),
		blockStop(0),
		messageDelta("end_turn", 3),
		messageStop,
	)
}

func newAnthropicTestClient(t *testing.T, fake *fakeAnthropic, mutate func(*Config), options ...Option) *Client {
	t.Helper()
	fake.t = t
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	config := Config{Kind: KindAnthropic, BaseURL: server.URL + "/v1", APIKey: "sk-test", Model: "claude-opus-5-5", ContextWindow: 200_000, Stream: true}
	if mutate != nil {
		mutate(&config)
	}
	options = append([]Option{withRetryHooks(func(context.Context, time.Duration) error { return nil }, func() float64 { return 0.5 })}, options...)
	client, err := NewClient(config, options...)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func testTools() []*schema.ToolInfo {
	return []*schema.ToolInfo{
		{Name: "read_file", Desc: "Read a file", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path": {Type: schema.String, Desc: "Path", Required: true},
		})},
		{Name: "now", Desc: "Current time"},
	}
}

func streamConcat(t *testing.T, chatModel model.ToolCallingChatModel, input []*schema.Message) (*schema.Message, []*schema.Message, error) {
	t.Helper()
	reader, err := chatModel.Stream(context.Background(), input)
	if err != nil {
		return nil, nil, err
	}
	defer reader.Close()
	var frames []*schema.Message
	for {
		frame, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, frames, err
		}
		frames = append(frames, frame)
	}
	message, err := schema.ConcatMessages(frames)
	return message, frames, err
}

func boundModel(t *testing.T, client *Client) model.ToolCallingChatModel {
	t.Helper()
	chatModel, err := client.ChatModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bound, err := chatModel.WithTools(testTools())
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func firstTurnInput() []*schema.Message {
	return []*schema.Message{schema.SystemMessage("You are the stable instruction."), schema.UserMessage("check hosts and time")}
}

func toolResults() []*schema.Message {
	return []*schema.Message{
		schema.ToolMessage(`{"ok":true,"text":"127.0.0.1 localhost"}`, "toolu_a"),
		schema.ToolMessage(`{"ok":false,"text":"clock unavailable"}`, "toolu_b"),
	}
}

func asList(value any) []any {
	list, _ := value.([]any)
	return list
}

func asMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func TestAnthropicStreamMapsThinkingTextParallelToolsAndUsage(t *testing.T) {
	fake := &fakeAnthropic{scripts: []func(http.ResponseWriter, *http.Request){thinkingToolTurn()}}
	client := newAnthropicTestClient(t, fake, nil)
	message, frames, err := streamConcat(t, boundModel(t, client), firstTurnInput())
	if err != nil {
		t.Fatal(err)
	}
	if message.ReasoningContent != "Need the file and the date." || message.Content != "Checking both." {
		t.Fatalf("message text = %q / %q", message.ReasoningContent, message.Content)
	}
	if len(message.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v", message.ToolCalls)
	}
	first, second := message.ToolCalls[0], message.ToolCalls[1]
	if first.ID != "toolu_a" || first.Function.Name != "read_file" || first.Function.Arguments != `{"path":"/etc/hosts"}` {
		t.Fatalf("first call = %+v", first)
	}
	if second.ID != "toolu_b" || second.Function.Name != "now" || second.Function.Arguments != "{}" {
		t.Fatalf("second call = %+v", second)
	}
	meta := message.ResponseMeta
	if meta == nil || meta.FinishReason != "tool_calls" || meta.Usage == nil {
		t.Fatalf("response meta = %+v", meta)
	}
	if meta.Usage.PromptTokens != 130 || meta.Usage.PromptTokenDetails.CachedTokens != 100 || meta.Usage.CompletionTokens != 42 {
		t.Fatalf("usage = %+v", meta.Usage)
	}
	completion := completionFromMessage(message)
	if completion.Usage.PromptTokens != 130 || completion.Usage.CachedTokens != 100 || completion.Usage.CacheCreationTokens != 20 || completion.Usage.CompletionTokens != 42 {
		t.Fatalf("completion usage = %+v", completion.Usage)
	}
	if extraString(message.Extra, MessageExtraServingModel) != "claude-opus-5-5" {
		t.Fatalf("serving model extra = %+v", message.Extra)
	}
	carriers := 0
	for _, frame := range frames {
		if _, ok := frame.Extra[anthropicContentKey]; ok {
			carriers++
		}
	}
	if carriers != 1 {
		t.Fatalf("content carrier rode on %d frames", carriers)
	}

	requests := fake.requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d", len(requests))
	}
	request := requests[0]
	if request.path != "/v1/messages" || request.headers.Get("Authorization") != "Bearer sk-test" || request.headers.Get("Anthropic-Version") == "" {
		t.Fatalf("request path %q headers %v", request.path, request.headers)
	}
	body := request.body
	if body["stream"] != true || body["model"] != "claude-opus-5-5" || body["max_tokens"] != float64(32_768) {
		t.Fatalf("request body = %s", request.raw)
	}
	if _, ok := body["temperature"]; ok {
		t.Fatalf("request sent a temperature: %s", request.raw)
	}
	thinking := asMap(body["thinking"])
	if thinking["type"] != "adaptive" || thinking["display"] != "summarized" {
		t.Fatalf("thinking = %v", thinking)
	}
	if asMap(body["cache_control"])["type"] != "ephemeral" {
		t.Fatalf("top-level cache_control = %v", body["cache_control"])
	}
	system := asList(body["system"])
	if len(system) != 1 || asMap(system[0])["text"] != "You are the stable instruction." || asMap(asMap(system[0])["cache_control"])["type"] != "ephemeral" {
		t.Fatalf("system = %v", system)
	}
	tools := asList(body["tools"])
	if len(tools) != 2 || asMap(tools[0])["name"] != "read_file" {
		t.Fatalf("tools = %v", tools)
	}
	schemaObject := asMap(asMap(tools[0])["input_schema"])
	if schemaObject["type"] != "object" || asMap(schemaObject["properties"])["path"] == nil || len(asList(schemaObject["required"])) != 1 {
		t.Fatalf("input schema = %v", schemaObject)
	}
	if asMap(asMap(tools[1])["input_schema"])["type"] != "object" {
		t.Fatalf("parameterless tool schema = %v", tools[1])
	}
}

func TestAnthropicToolRoundTripReplaysThinkingAndGroupsResults(t *testing.T) {
	fake := &fakeAnthropic{scripts: []func(http.ResponseWriter, *http.Request){thinkingToolTurn(), textTurn("done")}}
	client := newAnthropicTestClient(t, fake, nil)
	chatModel := boundModel(t, client)
	input := firstTurnInput()
	reply, _, err := streamConcat(t, chatModel, input)
	if err != nil {
		t.Fatal(err)
	}
	input = append(append(input, reply), toolResults()...)
	final, _, err := streamConcat(t, chatModel, input)
	if err != nil || final.Content != "done" {
		t.Fatalf("final = %+v, %v", final, err)
	}
	requests := fake.requests()
	first, second := requests[0], requests[1]
	if string(mustJSON(t, first.body["tools"])) != string(mustJSON(t, second.body["tools"])) ||
		string(mustJSON(t, first.body["system"])) != string(mustJSON(t, second.body["system"])) {
		t.Fatalf("tools or system changed inside the run:\n%s\n%s", first.raw, second.raw)
	}
	messages := asList(second.body["messages"])
	if len(messages) != 3 {
		t.Fatalf("messages = %s", mustJSON(t, messages))
	}
	assistant := asList(asMap(messages[1])["content"])
	if len(assistant) != 4 {
		t.Fatalf("assistant content = %s", mustJSON(t, assistant))
	}
	thinking := asMap(assistant[0])
	if thinking["type"] != "thinking" || thinking["signature"] != "sig-one" || thinking["thinking"] != "Need the file and the date." {
		t.Fatalf("thinking block = %v", thinking)
	}
	if asMap(assistant[1])["type"] != "text" || asMap(assistant[2])["id"] != "toolu_a" || asMap(assistant[3])["id"] != "toolu_b" {
		t.Fatalf("assistant blocks = %s", mustJSON(t, assistant))
	}
	if input := asMap(asMap(assistant[2])["input"]); input["path"] != "/etc/hosts" {
		t.Fatalf("tool input = %v", assistant[2])
	}
	results := asList(asMap(messages[2])["content"])
	if asMap(messages[2])["role"] != "user" || len(results) != 2 {
		t.Fatalf("tool results = %s", mustJSON(t, messages[2]))
	}
	okResult, failedResult := asMap(results[0]), asMap(results[1])
	if okResult["type"] != "tool_result" || okResult["tool_use_id"] != "toolu_a" || okResult["is_error"] == true {
		t.Fatalf("first result = %v", okResult)
	}
	if failedResult["tool_use_id"] != "toolu_b" || failedResult["is_error"] != true {
		t.Fatalf("second result = %v", failedResult)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assistantBlockTypes(t *testing.T, exchange anthropicExchange) []string {
	t.Helper()
	var types []string
	for _, message := range asList(exchange.body["messages"]) {
		if asMap(message)["role"] != "assistant" {
			continue
		}
		for _, block := range asList(asMap(message)["content"]) {
			types = append(types, fmt.Sprint(asMap(block)["type"]))
		}
	}
	return types
}

// A change before a reply invalidates its thinking, so the reply is sent
// without it instead of being rejected.
func TestAnthropicDropsThinkingWhenItsPrefixChanged(t *testing.T) {
	fake := &fakeAnthropic{scripts: []func(http.ResponseWriter, *http.Request){thinkingToolTurn(), textTurn("done")}}
	client := newAnthropicTestClient(t, fake, nil)
	chatModel := boundModel(t, client)
	input := firstTurnInput()
	reply, _, err := streamConcat(t, chatModel, input)
	if err != nil {
		t.Fatal(err)
	}
	changed := []*schema.Message{schema.SystemMessage("You are the stable instruction."), schema.UserMessage("[History Summary]\nearlier work"), schema.UserMessage("check hosts and time")}
	changed = append(append(changed, reply), toolResults()...)
	if _, _, err := streamConcat(t, chatModel, changed); err != nil {
		t.Fatal(err)
	}
	if types := assistantBlockTypes(t, fake.requests()[1]); strings.Join(types, ",") != "text,tool_use,tool_use" {
		t.Fatalf("assistant blocks after a prefix change = %v", types)
	}
}

// A rejection naming a thinking block bound to a different conversation is
// retried once with every earlier thinking block left out.
func TestAnthropicRetriesWithoutThinkingAfterPrefixMismatch(t *testing.T) {
	mismatch := errorScript(http.StatusBadRequest, "invalid_request_error", "messages.1.content.0: Invalid `signature` in `thinking` block. The block is bound to a different conversation. Remove the block.", nil)
	fake := &fakeAnthropic{scripts: []func(http.ResponseWriter, *http.Request){thinkingToolTurn(), mismatch, textTurn("done")}}
	client := newAnthropicTestClient(t, fake, nil)
	chatModel := boundModel(t, client)
	input := firstTurnInput()
	reply, _, err := streamConcat(t, chatModel, input)
	if err != nil {
		t.Fatal(err)
	}
	input = append(append(input, reply), toolResults()...)
	final, _, err := streamConcat(t, chatModel, input)
	if err != nil || final.Content != "done" {
		t.Fatalf("final = %+v, %v", final, err)
	}
	requests := fake.requests()
	if len(requests) != 3 {
		t.Fatalf("requests = %d", len(requests))
	}
	if types := assistantBlockTypes(t, requests[1]); types[0] != "thinking" {
		t.Fatalf("first attempt blocks = %v", types)
	}
	if types := assistantBlockTypes(t, requests[2]); strings.Join(types, ",") != "text,tool_use,tool_use" {
		t.Fatalf("retry blocks = %v", types)
	}
}

// History rebuilt from storage has reasoning text but no signature, so it is
// never sent as a thinking block.
func TestAnthropicHistoryWithoutCarrierSendsNoThinking(t *testing.T) {
	fake := &fakeAnthropic{scripts: []func(http.ResponseWriter, *http.Request){textTurn("ok")}}
	client := newAnthropicTestClient(t, fake, nil)
	stored := schema.AssistantMessage("", []schema.ToolCall{{ID: "toolu_old", Type: "function", Function: schema.FunctionCall{Name: "now", Arguments: ""}}})
	stored.ReasoningContent = "old reasoning"
	input := []*schema.Message{
		schema.SystemMessage("stable"), schema.UserMessage("earlier"), stored,
		schema.UserMessage("again"),
	}
	if _, _, err := streamConcat(t, boundModel(t, client), input); err != nil {
		t.Fatal(err)
	}
	request := fake.requests()[0]
	if types := assistantBlockTypes(t, request); strings.Join(types, ",") != "tool_use" {
		t.Fatalf("assistant blocks = %v", types)
	}
	messages := asList(request.body["messages"])
	results := asList(asMap(messages[2])["content"])
	if asMap(results[0])["type"] != "tool_result" || asMap(results[0])["is_error"] != true || asMap(results[1])["text"] != "again" {
		t.Fatalf("unanswered call was not closed: %s", mustJSON(t, messages[2]))
	}
	if input := asMap(asMap(asList(asMap(messages[1])["content"])[0])["input"]); len(input) != 0 {
		t.Fatalf("empty arguments input = %v", input)
	}
}

func TestAnthropicKeepsCachedSystemPrefixStableAroundVolatileSystemText(t *testing.T) {
	request, err := buildAnthropicRequest(Config{Model: "m", ContextWindow: 200_000}, "m", []*schema.Message{
		schema.SystemMessage(privacyInstruction + "\n\nstable instruction"),
		schema.SystemMessage("memory for this run"),
		schema.UserMessage("hello"),
	}, &model.Options{}, false)
	if err != nil {
		t.Fatal(err)
	}
	system := request.params.System
	if len(system) != 3 || system[0].Text != "stable instruction" || system[1].Text != privacyInstruction || system[2].Text != "memory for this run" {
		t.Fatalf("system = %+v", system)
	}
	if system[0].CacheControl.Type == "" || system[1].CacheControl.Type != "" || system[2].CacheControl.Type != "" {
		t.Fatalf("cache breakpoints = %+v", system)
	}
}

func TestAnthropicThinkingSettings(t *testing.T) {
	cases := []struct {
		name   string
		config Config
		want   string
	}{
		{"adaptive", Config{ReasoningEffort: "high"}, `{"display":"summarized","type":"adaptive"}`},
		{"budget", Config{Thinking: ThinkingBudget, ThinkingBudget: new(4096)}, `{"budget_tokens":4096,"type":"enabled"}`},
		{"budget capped", Config{Thinking: ThinkingBudget, ThinkingBudget: new(30_000), MaxTokens: new(8_000)}, `{"budget_tokens":7999,"type":"enabled"}`},
		{"off", Config{Thinking: ThinkingOff}, `{"type":"disabled"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := tc.config
			config.ContextWindow = 200_000
			request, err := buildAnthropicRequest(config.Normalized(), "m", []*schema.Message{schema.UserMessage("hi")}, &model.Options{}, false)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(request.params.Thinking)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != tc.want {
				t.Fatalf("thinking = %s, want %s", encoded, tc.want)
			}
		})
	}
	request, err := buildAnthropicRequest(Config{ReasoningEffort: "minimal", ContextWindow: 200_000}, "m", []*schema.Message{schema.UserMessage("hi")}, &model.Options{}, false)
	if err != nil || request.params.OutputConfig.Effort != "low" {
		t.Fatalf("effort = %q, %v", request.params.OutputConfig.Effort, err)
	}
}

func TestAnthropicSendsImages(t *testing.T) {
	data := "aGVsbG8="
	address := "https://example.test/a.png"
	message := &schema.Message{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{
		{Type: schema.ChatMessagePartTypeText, Text: "what is this"},
		{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: "image/png"}}},
		{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &address}}},
	}}
	request, err := buildAnthropicRequest(Config{ContextWindow: 200_000}, "m", []*schema.Message{message}, &model.Options{}, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(mustJSON(t, request.params.Messages))
	if !strings.Contains(encoded, `"source":{"data":"aGVsbG8=","media_type":"image/png","type":"base64"}`) ||
		!strings.Contains(encoded, `"source":{"url":"https://example.test/a.png","type":"url"}`) {
		t.Fatalf("messages = %s", encoded)
	}
	bad := "aGVsbG8="
	_, err = buildAnthropicRequest(Config{ContextWindow: 200_000}, "m", []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{
		{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &bad, MIMEType: "image/bmp"}}},
	}}}, &model.Options{}, false)
	if err == nil {
		t.Fatal("an unsupported image type was accepted")
	}
}

func TestAnthropicErrorsUseTheSharedVocabulary(t *testing.T) {
	retryAfter := http.Header{"Retry-After": {"7"}}
	fake := &fakeAnthropic{scripts: []func(http.ResponseWriter, *http.Request){
		errorScript(http.StatusTooManyRequests, "rate_limit_error", "slow down", retryAfter),
		textTurn("after limit"),
	}}
	var delays []time.Duration
	var mu sync.Mutex
	client := newAnthropicTestClient(t, fake, nil, withRetryHooks(func(_ context.Context, delay time.Duration) error {
		mu.Lock()
		delays = append(delays, delay)
		mu.Unlock()
		return nil
	}, nil))
	completion, err := client.ChatBlock(context.Background(), ChatRequest{Messages: []ChatMessage{UserMessage("hi")}})
	if err != nil || completion.Content != "after limit" {
		t.Fatalf("completion = %+v, %v", completion, err)
	}
	mu.Lock()
	if len(delays) != 1 || delays[0] != 7*time.Second {
		t.Fatalf("retry delays = %v", delays)
	}
	mu.Unlock()

	auth := &fakeAnthropic{scripts: []func(http.ResponseWriter, *http.Request){
		errorScript(http.StatusUnauthorized, "authentication_error", "invalid x-api-key", nil),
	}}
	client = newAnthropicTestClient(t, auth, nil)
	_, err = client.ChatBlock(context.Background(), ChatRequest{Messages: []ChatMessage{UserMessage("hi")}})
	if ClassifyError(err) != ErrorClassClient || len(auth.requests()) != 1 {
		t.Fatalf("auth error = %v (%s), requests %d", err, ClassifyError(err), len(auth.requests()))
	}
	if status, detail := ErrorDetail(err); status != http.StatusUnauthorized || detail != "invalid x-api-key" {
		t.Fatalf("error detail = %d %q", status, detail)
	}

	overloaded := sseScript(
		messageStart("claude-opus-5-5", 5, 0, 0),
		blockStart(0, `{"type":"text","text":""}`),
		blockDelta(0, `{"type":"text_delta","text":"partial"}`),
		`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
	)
	midStream := &fakeAnthropic{scripts: []func(http.ResponseWriter, *http.Request){overloaded}}
	client = newAnthropicTestClient(t, midStream, nil)
	_, frames, err := streamConcat(t, boundModel(t, client), firstTurnInput())
	if ClassifyError(err) != ErrorClassServer || len(frames) == 0 || len(midStream.requests()) != 1 {
		t.Fatalf("mid-stream error = %v (%s), frames %d, requests %d", err, ClassifyError(err), len(frames), len(midStream.requests()))
	}
}

func TestAnthropicRefusalAndLengthStops(t *testing.T) {
	refusal := sseScript(
		messageStart("claude-opus-5-5", 5, 0, 0),
		`{"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"cyber","explanation":"declined"}},"usage":{"output_tokens":1}}`,
		messageStop,
	)
	fake := &fakeAnthropic{scripts: []func(http.ResponseWriter, *http.Request){refusal}}
	client := newAnthropicTestClient(t, fake, nil)
	_, err := client.ChatBlock(context.Background(), ChatRequest{Messages: []ChatMessage{UserMessage("hi")}})
	var refused *RefusalError
	if !errors.As(err, &refused) || refused.Category != "cyber" || len(fake.requests()) != 1 {
		t.Fatalf("refusal error = %v", err)
	}

	cut := sseScript(
		messageStart("claude-opus-5-5", 5, 0, 0),
		blockStart(0, `{"type":"tool_use","id":"toolu_cut","name":"read_file","input":{}}`),
		blockDelta(0, `{"type":"input_json_delta","partial_json":"{\"path\":\"/et"}`),
		messageDelta("max_tokens", 9),
		messageStop,
	)
	fake = &fakeAnthropic{scripts: []func(http.ResponseWriter, *http.Request){cut}}
	client = newAnthropicTestClient(t, fake, nil)
	message, _, err := streamConcat(t, boundModel(t, client), firstTurnInput())
	if err != nil || message.ResponseMeta.FinishReason != "length" || json.Valid([]byte(message.ToolCalls[0].Function.Arguments)) {
		t.Fatalf("cut message = %+v, %v", message, err)
	}
}

func TestAnthropicStreamCancellationStopsTheRequest(t *testing.T) {
	released := make(chan struct{})
	gone := make(chan struct{})
	fake := &fakeAnthropic{scripts: []func(http.ResponseWriter, *http.Request){func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message_start\ndata: %s\n\n", messageStart("claude-opus-5-5", 1, 0, 0))
		fmt.Fprintf(w, "event: content_block_start\ndata: %s\n\n", blockStart(0, `{"type":"text","text":""}`))
		fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\n", blockDelta(0, `{"type":"text_delta","text":"first"}`))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(gone)
		case <-released:
		}
	}}}
	t.Cleanup(func() { close(released) })
	client := newAnthropicTestClient(t, fake, nil)
	chatModel := boundModel(t, client)
	ctx, cancel := context.WithCancel(context.Background())
	reader, err := chatModel.Stream(ctx, firstTurnInput())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	frame, err := reader.Recv()
	if err != nil || frame.Content != "first" {
		t.Fatalf("first frame = %+v, %v", frame, err)
	}
	cancel()
	for {
		_, err = reader.Recv()
		if err != nil {
			break
		}
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error after cancel = %v", err)
	}
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream request was not cancelled")
	}
}

func TestAnthropicListModelsAndProviderTest(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			errorScript(http.StatusUnauthorized, "authentication_error", "bad key", nil)(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/models" && r.URL.Query().Get("after_id") == "":
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-opus-5-5","type":"model"}],"has_more":true,"first_id":"claude-opus-5-5","last_id":"claude-opus-5-5"}`)
		case r.URL.Path == "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-haiku-5-5","type":"model"}],"has_more":false,"first_id":"claude-haiku-5-5","last_id":"claude-haiku-5-5"}`)
		case r.URL.Path == "/v1/messages":
			textTurn("pong")(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(Config{Kind: KindAnthropic, BaseURL: server.URL, APIKey: "sk-test", Model: "claude-opus-5-5", ContextWindow: 200_000, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	models, err := client.ListModels(context.Background())
	if err != nil || strings.Join(models, ",") != "claude-opus-5-5,claude-haiku-5-5" {
		t.Fatalf("models = %v, %v", models, err)
	}
	mu.Lock()
	if len(paths) != 2 || !strings.Contains(paths[1], "after_id=claude-opus-5-5") {
		t.Fatalf("model list requests = %v", paths)
	}
	mu.Unlock()
	result := client.Test(context.Background())
	if !result.ModelsOK || !result.ChatOK {
		t.Fatalf("test result = %+v", result)
	}
}

func TestAnthropicBaseURLAcceptsVersionSuffix(t *testing.T) {
	for input, want := range map[string]string{
		"":                              "https://api.anthropic.com/",
		"https://api.anthropic.com":     "https://api.anthropic.com/",
		"https://api.anthropic.com/v1/": "https://api.anthropic.com/",
		"https://gateway.test/claude":   "https://gateway.test/claude/",
	} {
		if got := anthropicAPIBase(input); got != want {
			t.Fatalf("anthropicAPIBase(%q) = %q, want %q", input, got, want)
		}
	}
}

// With redaction on, the carrier keeps the placeholders the model saw and
// still arrives exactly once, so the replayed reply matches what was produced.
func TestAnthropicCarrierSurvivesPrivacyRestore(t *testing.T) {
	const secret = "synthetic-private-value"
	var placeholder string
	fake := &fakeAnthropic{}
	fake.scripts = []func(http.ResponseWriter, *http.Request){
		func(w http.ResponseWriter, r *http.Request) {
			messages := asList(fake.requests()[0].body["messages"])
			text := fmt.Sprint(asMap(asList(asMap(messages[0])["content"])[0])["text"])
			start := strings.Index(text, "<")
			placeholder = text[start : strings.Index(text, ">")+1]
			arguments, _ := json.Marshal(map[string]string{"path": placeholder})
			encodedArgs, _ := json.Marshal(string(arguments))
			sseScript(
				messageStart("claude-opus-5-5", 10, 0, 0),
				blockStart(0, `{"type":"thinking","thinking":"","signature":""}`),
				blockDelta(0, fmt.Sprintf(`{"type":"thinking_delta","thinking":%q}`, "look at "+placeholder)),
				blockDelta(0, `{"type":"signature_delta","signature":"sig-private"}`),
				blockStop(0),
				blockStart(1, `{"type":"tool_use","id":"toolu_p","name":"read_file","input":{}}`),
				blockDelta(1, fmt.Sprintf(`{"type":"input_json_delta","partial_json":%s}`, encodedArgs)),
				blockStop(1),
				messageDelta("tool_use", 5),
				messageStop,
			)(w, r)
		},
		textTurn("done"),
	}
	client := newAnthropicTestClient(t, fake, nil, privacyOption(privateValues(secret)))
	chatModel := boundModel(t, client)
	input := []*schema.Message{schema.SystemMessage("stable"), schema.UserMessage("read " + secret)}
	reply, frames, err := streamConcat(t, chatModel, input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply.ReasoningContent, secret) || !strings.Contains(reply.ToolCalls[0].Function.Arguments, secret) {
		t.Fatalf("restored reply = %+v", reply)
	}
	carriers := 0
	for _, frame := range frames {
		if _, ok := frame.Extra[anthropicContentKey]; ok {
			carriers++
		}
	}
	raw, _ := reply.Extra[anthropicContentKey].(string)
	var carrier anthropicCarrier
	if carriers != 1 || json.Unmarshal([]byte(raw), &carrier) != nil || len(carrier.Content) != 2 {
		t.Fatalf("carrier frames %d, carrier %q", carriers, raw)
	}
	var thinking struct {
		Thinking string `json:"thinking"`
	}
	if json.Unmarshal(carrier.Content[0], &thinking) != nil || thinking.Thinking != "look at "+placeholder {
		t.Fatalf("carried thinking = %s", carrier.Content[0])
	}
	input = append(input, reply, schema.ToolMessage(`{"ok":true,"text":"contents"}`, "toolu_p"))
	if _, _, err := streamConcat(t, chatModel, input); err != nil {
		t.Fatal(err)
	}
	second := fake.requests()[1]
	if strings.Contains(string(second.raw), secret) {
		t.Fatalf("second request leaked the secret: %s", second.raw)
	}
	if types := assistantBlockTypes(t, second); strings.Join(types, ",") != "thinking,tool_use" {
		t.Fatalf("replayed blocks = %v (%s)", types, second.raw)
	}
}
