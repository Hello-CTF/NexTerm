package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStreamChatFinalizesCacheCreationUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		response := "" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":10,\"prompt_tokens_details\":{\"cached_tokens\":40,\"cache_creation_tokens\":25}}}\n\n" +
			"data: [DONE]\n\n"
		if _, err := writer.Write([]byte(response)); err != nil {
			return
		}
		flusher.Flush()
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, APIKey: "k", Model: "m", Stream: true, ContextWindow: 1000})
	if err != nil {
		t.Fatal(err)
	}
	completion, err := client.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if completion.Usage.CacheCreationTokens != 25 || completion.Usage.CachedTokens != 40 || completion.Usage.PromptTokens != 100 || completion.Usage.CompletionTokens != 10 {
		t.Fatalf("streamed final usage = %+v", completion.Usage)
	}
}
