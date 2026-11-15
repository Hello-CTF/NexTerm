package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
)

func TestMultiFrameStreamUsageEventCarriesExactMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range []string{
			`{"model":"served-m","choices":[{"delta":{"content":"a"}}]}`,
			`{"model":"served-m","choices":[{"delta":{"content":"b"}}]}`,
			`{"model":"served-m","choices":[{"delta":{"content":"c"}}]}`,
			`{"model":"served-m","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
		} {
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", frame)
			writer.(http.Flusher).Flush()
		}
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client, err := provider.NewClient(provider.Config{BaseURL: server.URL, Model: "m", Stream: true, ContextWindow: 32768})
	if err != nil {
		t.Fatal(err)
	}
	chat, err := client.ChatModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage,
	})
	t.Cleanup(func() {
		_ = runner.Close()
		_ = storage.Close()
	})
	stream := &SliceStream{}
	startTestJob(t, runner, stream, "go")
	events := waitClosed(t, stream)
	var usageEvents []Event
	for _, event := range events {
		if event.Type == "usage" {
			usageEvents = append(usageEvents, event)
		}
	}
	if len(usageEvents) != 1 {
		t.Fatalf("usage events = %+v", usageEvents)
	}
	usage := usageEvents[0]
	// Merged identifiers must keep the width of a single generated id.
	if usage.Model != "served-m" || usage.RunID == "" || len(usage.RunID) != len(ids.New()) ||
		usage.PromptTokens != 7 || usage.CompletionTokens != 3 {
		t.Fatalf("usage event = %+v", usage)
	}
}
