package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func ptr[T any](value T) *T { return &value }

func TestChatBlockRequestTemperatureAndReasoningEffort(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"chatcmpl-1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer server.Close()

	cases := []struct {
		name            string
		config          Config
		wantTemperature any
		wantEffort      any
	}{
		{name: "unset by default", config: Config{BaseURL: server.URL + "/v1", Model: "m", Stream: true}},
		{name: "explicit zero stays", config: Config{BaseURL: server.URL + "/v1", Model: "m", Stream: true, Temperature: ptr(0.0)}, wantTemperature: 0.0},
		{name: "explicit values sent", config: Config{BaseURL: server.URL + "/v1", Model: "m", Stream: true, Temperature: ptr(0.3), ReasoningEffort: ReasoningEffortHigh}, wantTemperature: 0.3, wantEffort: "high"},
		{name: "minimal effort sent", config: Config{BaseURL: server.URL + "/v1", Model: "m", Stream: true, ReasoningEffort: ReasoningEffortMinimal}, wantEffort: "minimal"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			client, err := NewClient(testCase.config)
			if err != nil {
				t.Fatal(err)
			}
			completion, err := client.ChatBlock(context.Background(), ChatRequest{Messages: []ChatMessage{UserMessage("hi")}})
			if err != nil {
				t.Fatal(err)
			}
			if completion.Content != "OK" {
				t.Fatalf("completion = %+v", completion)
			}
			body := bodies[len(bodies)-1]
			temperature, hasTemperature := body["temperature"]
			if testCase.wantTemperature == nil && hasTemperature {
				t.Fatalf("unset temperature must not be sent: %v", body)
			}
			if testCase.wantTemperature != nil && (!hasTemperature || temperature != testCase.wantTemperature) {
				t.Fatalf("temperature = %v (sent=%v), want %v: %v", temperature, hasTemperature, testCase.wantTemperature, body)
			}
			effort, hasEffort := body["reasoning_effort"]
			if testCase.wantEffort == nil && hasEffort {
				t.Fatalf("unset reasoning effort must not be sent: %v", body)
			}
			if testCase.wantEffort != nil && (!hasEffort || effort != testCase.wantEffort) {
				t.Fatalf("reasoning_effort = %v (sent=%v), want %v: %v", effort, hasEffort, testCase.wantEffort, body)
			}
		})
	}
}
