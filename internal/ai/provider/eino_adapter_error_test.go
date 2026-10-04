package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestBaseChatModelStreamDeliversFramesBeforeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4}}\n\ndata: invalid\n\n"))
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, Model: "m", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	chatModel, _, err := client.BaseChatModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reader, err := chatModel.Stream(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	frame, err := reader.Recv()
	if err != nil {
		t.Fatalf("first Recv() = %v", err)
	}
	if frame.Content != "partial" {
		t.Fatalf("frame content = %q", frame.Content)
	}
	if frame.ResponseMeta == nil || frame.ResponseMeta.Usage == nil ||
		frame.ResponseMeta.Usage.PromptTokens != 9 || frame.ResponseMeta.Usage.CompletionTokens != 4 {
		t.Fatalf("frame usage = %+v", frame.ResponseMeta)
	}
	if _, err := reader.Recv(); err == nil {
		t.Fatal("malformed frame produced no error")
	}
}
