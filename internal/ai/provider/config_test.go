package provider

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"reflect"
	"testing"
)

func TestAllPresets(t *testing.T) {
	presets := Presets()
	got := make([]string, 0, len(presets))
	for _, preset := range presets {
		got = append(got, preset.ID)
	}
	want := []string{"moonshot", "deepseek", "zhipu", "ollama"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("preset order = %v, want %v", got, want)
	}
	checks := map[string]struct {
		baseURL string
		model   string
		window  uint64
	}{
		"moonshot": {"https://api.moonshot.cn/v1", "kimi-k3", 1000000},
		"deepseek": {"https://api.deepseek.com/v1", "deepseek-flash", 1000000},
		"ollama":   {"http://127.0.0.1:11434/v1", "qwen2.5:7b", 32000},
		"zhipu":    {"https://open.bigmodel.cn/api/paas/v4", "glm-4.7-flash", 200000},
	}
	for id, check := range checks {
		config, err := FromPreset(id, "")
		if err != nil {
			t.Fatal(err)
		}
		if config.BaseURL != check.baseURL || config.Model != check.model || config.ContextWindow != check.window || config.Temperature != nil || config.ReasoningEffort != "" || !config.Stream || config.Proxy != nil {
			t.Fatalf("preset %s = %+v", id, config)
		}
	}
	config, err := FromPreset("moonshot", " custom-model ")
	if err != nil || config.Model != "custom-model" {
		t.Fatalf("model override = %+v, %v", config, err)
	}
	if _, err := FromPreset("unknown", ""); err == nil {
		t.Fatal("unknown preset was accepted")
	}
}

func TestConfigNormalizationAndJSONDefaults(t *testing.T) {
	emptyProxy := "  "
	config := Config{BaseURL: " https://example.test/v1/// ", APIKey: " key ", Model: " model ", FallbackModel: " fallback ", Temperature: ptr(math.NaN()), ContextWindow: 0, Proxy: &emptyProxy}.Normalized()
	if config.BaseURL != "https://example.test/v1" || config.APIKey != "key" || config.Model != "model" || config.FallbackModel != "fallback" || config.Temperature != nil || config.ContextWindow != 1000 || config.Proxy != nil {
		t.Fatalf("normalized config = %+v", config)
	}
	config.Temperature = ptr(math.Inf(1))
	config.ContextWindow = 3_000_000
	config = config.Normalized()
	if config.Temperature != nil || config.ContextWindow != 2_000_000 {
		t.Fatalf("non-finite/clamped config = %+v", config)
	}
	if same := (Config{Model: "same", FallbackModel: " same "}).Normalized(); same.FallbackModel != "" {
		t.Fatalf("non-alternate fallback = %+v", same)
	}
	var decoded Config
	if err := json.Unmarshal([]byte(`{"model":"m","fallbackModel":"f"}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Temperature != nil || decoded.ReasoningEffort != "" || decoded.ContextWindow != 32768 || !decoded.Stream || decoded.FallbackModel != "f" {
		t.Fatalf("JSON defaults = %+v", decoded)
	}
	if err := json.Unmarshal([]byte(`{"temperature":0,"stream":false}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Temperature == nil || *decoded.Temperature != 0 || decoded.Stream || decoded.FallbackModel != "" {
		t.Fatalf("explicit zero/false were lost: %+v", decoded)
	}
}

func TestConfigTemperatureAndReasoningEffortJSONCompat(t *testing.T) {
	var legacy Config
	if err := json.Unmarshal([]byte(`{"baseUrl":"https://a.test/v1","model":"m","temperature":0.3}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Temperature == nil || *legacy.Temperature != 0.3 {
		t.Fatalf("explicit 0.3 must stay explicit: %+v", legacy)
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["temperature"] != 0.3 {
		t.Fatalf("round-trip lost explicit temperature: %s", encoded)
	}

	unset := DefaultConfig()
	encoded, err = json.Marshal(unset)
	if err != nil {
		t.Fatal(err)
	}
	wire = nil
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if _, exists := wire["temperature"]; exists {
		t.Fatalf("unset temperature must be omitted: %s", encoded)
	}
	if _, exists := wire["reasoningEffort"]; exists {
		t.Fatalf("unset reasoning effort must be omitted: %s", encoded)
	}

	var effort Config
	if err := json.Unmarshal([]byte(`{"model":"m","reasoningEffort":"high"}`), &effort); err != nil {
		t.Fatal(err)
	}
	if effort.ReasoningEffort != ReasoningEffortHigh {
		t.Fatalf("reasoning effort = %q", effort.ReasoningEffort)
	}
	for _, valid := range []ReasoningEffort{ReasoningEffortMinimal, ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh} {
		if !valid.Valid() {
			t.Fatalf("%q must be valid", valid)
		}
	}
	if ReasoningEffort("").Valid() || ReasoningEffort("max").Valid() {
		t.Fatal("empty/unknown effort must be invalid")
	}
	if normalized := (Config{ReasoningEffort: "max"}).Normalized(); normalized.ReasoningEffort != "" {
		t.Fatalf("unknown effort must normalize to unset: %+v", normalized)
	}
	if normalized := (Config{ReasoningEffort: ReasoningEffortMinimal}).Normalized(); normalized.ReasoningEffort != ReasoningEffortMinimal {
		t.Fatalf("minimal effort must survive normalization: %+v", normalized)
	}
	clamped := (Config{Temperature: ptr(9.0)}).Normalized()
	if clamped.Temperature == nil || *clamped.Temperature != 2 {
		t.Fatalf("temperature must clamp to [0,2]: %+v", clamped)
	}
}

func TestChatMessageWireContent(t *testing.T) {
	message := ChatMessage{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "look"},
		map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64,aGVsbG8="}},
		map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:image/jpeg;base64,AAAA"}},
	}}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Content) != 3 || decoded.Content[0].Text != "look" || decoded.Content[1].ImageURL.URL != "data:image/png;base64,aGVsbG8=" || decoded.Content[2].ImageURL.URL != "data:image/jpeg;base64,AAAA" {
		t.Fatalf("multipart message = %s", encoded)
	}
	plain := UserMessage("text")
	encoded, _ = json.Marshal(plain)
	if string(encoded) != `{"role":"user","content":"text"}` {
		t.Fatalf("image-free message should use string content: %s", encoded)
	}
	tool := ChatMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: "1", Type: "function"}}}
	encoded, _ = json.Marshal(tool)
	var wire map[string]any
	_ = json.Unmarshal(encoded, &wire)
	if value, exists := wire["content"]; !exists || value != nil {
		t.Fatalf("assistant tool history must have null content: %s", encoded)
	}
}

func TestToolStreamHostBoundaries(t *testing.T) {
	for _, baseURL := range []string{
		"https://open.bigmodel.cn/api/paas/v4", "https://bigmodel.cn/v1", "https://open.zhipuai.cn/v1", "https://api.z.ai/v1", "https://z.ai/v1",
	} {
		if !wantsToolStream(baseURL) {
			t.Errorf("wantsToolStream(%q) = false", baseURL)
		}
	}
	for _, baseURL := range []string{
		"https://example.com/z.ai/v1", "https://bigmodel.cn.evil.test/v1", "https://notzhipuai.com/v1", "https://z.ai.evil.test/v1",
	} {
		if wantsToolStream(baseURL) {
			t.Errorf("wantsToolStream(%q) = true", baseURL)
		}
	}
}

func TestChatRequestProviderSpecificFields(t *testing.T) {
	normalize := func(baseURL, raw string) map[string]any {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, baseURL+"/chat/completions", bytes.NewBufferString(raw))
		if err != nil {
			t.Fatal(err)
		}
		normalized, err := normalizeChatRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.NewDecoder(normalized.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	stream := normalize("https://open.bigmodel.cn/api/paas/v4", `{"stream":true,"stream_options":{},"tools":[{}],"messages":[]}`)
	if stream["stream"] != true || stream["tool_stream"] != true || stream["stream_options"] == nil || stream["tools"] == nil {
		t.Fatalf("stream body = %+v", stream)
	}
	block := normalize("https://open.bigmodel.cn/api/paas/v4", `{"tools":[{}],"messages":[]}`)
	for _, key := range []string{"stream", "stream_options", "tool_stream"} {
		if _, exists := block[key]; exists {
			t.Fatalf("block body unexpectedly contains %s: %+v", key, block)
		}
	}
	withoutTools := normalize("https://open.bigmodel.cn/api/paas/v4", `{"stream":true,"messages":[]}`)
	if _, exists := withoutTools["tool_stream"]; exists {
		t.Fatal("tool_stream was sent without tools")
	}
	openAI := normalize("https://api.openai.com/v1", `{"stream":true,"tools":[{}],"messages":[]}`)
	if _, exists := openAI["tool_stream"]; exists {
		t.Fatal("tool_stream leaked into an OpenAI request")
	}
	assistant := normalize("https://api.openai.com/v1", `{"messages":[{"role":"assistant","tool_calls":[]},{"role":"assistant"}]}`)
	messages := assistant["messages"].([]any)
	if content, exists := messages[0].(map[string]any)["content"]; !exists || content != nil {
		t.Fatalf("assistant tool content = %#v, exists=%v", content, exists)
	}
	if content := messages[1].(map[string]any)["content"]; content != "" {
		t.Fatalf("empty assistant content = %#v", content)
	}
}
