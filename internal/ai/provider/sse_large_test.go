package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestLargeStreamAggregationPreservesOrderingAndUTF8Progress(t *testing.T) {
	const fragments = 4096
	const fragment = "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"r\",\"content\":\"x\",\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"n\",\"arguments\":\"你\"}}]}}]}\n\n"
	var response strings.Builder
	for range fragments {
		response.WriteString(fragment)
	}
	response.WriteString("data: [DONE]\n\n")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, response.String())
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, Model: "m", Stream: true})
	if err != nil {
		t.Fatal(err)
	}

	frames := 0
	result, err := client.runNative(context.Background(), nil, nil, true, func(message *schema.Message) error {
		if message.ReasoningContent != "r" {
			t.Errorf("reasoning fragment = %q", message.ReasoningContent)
		}
		if message.Content != "x" {
			t.Errorf("content fragment = %q", message.Content)
		}
		if len(message.ToolCalls) != 1 {
			t.Fatalf("tool fragments = %+v", message.ToolCalls)
		}
		if message.ToolCalls[0].Function.Name != "n" || message.ToolCalls[0].Function.Arguments != "你" {
			t.Errorf("tool fragment = %+v", message.ToolCalls[0].Function)
		}
		frames++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if frames != fragments {
		t.Fatalf("frames = %d, want %d", frames, fragments)
	}
	completion := result.completion
	if completion.Content != strings.Repeat("x", fragments) || completion.Reasoning != strings.Repeat("r", fragments) {
		t.Fatal("large content or reasoning aggregation mismatch")
	}
	if len(completion.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", completion.ToolCalls)
	}
	call := completion.ToolCalls[0]
	if call.Function.Name != strings.Repeat("n", fragments) || call.Function.Arguments != strings.Repeat("你", fragments) {
		t.Fatal("large tool aggregation mismatch")
	}
}

var allocationCompletionSink Completion

func TestStreamAggregationAllocationsDoNotGrowPerFragment(t *testing.T) {
	const fragments = 512
	allocations := testing.AllocsPerRun(20, func() {
		state := streamState{pending: make(map[uint64]*pendingToolCall)}
		pending := &pendingToolCall{}
		state.pending[0] = pending
		for range fragments {
			state.content.WriteString("content-fragment")
			state.reasoning.WriteString("reasoning-fragment")
			pending.name.WriteString("name")
			pending.arguments.WriteString("你")
			_ = pending.arguments.Len()
		}
		state.finishTools()
		allocationCompletionSink = state.completion
	})
	if allocations > 100 {
		t.Fatalf("aggregation allocations = %.0f for %d fragments, want <= 100", allocations, fragments)
	}
}
