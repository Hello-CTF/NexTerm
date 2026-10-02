package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

	events := 0
	toolFragments := 0
	completion, err := client.Chat(context.Background(), ChatRequest{}, func(item StreamItem) {
		expectedKinds := []StreamKind{StreamReasoning, StreamDelta, StreamToolArgs}
		if item.Kind != expectedKinds[events%len(expectedKinds)] {
			t.Errorf("event %d kind = %s, want %s", events, item.Kind, expectedKinds[events%len(expectedKinds)])
		}
		events++
		switch item.Kind {
		case StreamReasoning:
			if item.Text != "r" {
				t.Errorf("reasoning fragment = %q", item.Text)
			}
		case StreamDelta:
			if item.Text != "x" {
				t.Errorf("content fragment = %q", item.Text)
			}
		case StreamToolArgs:
			toolFragments++
			if item.Chars != toolFragments*len("你") {
				t.Errorf("tool fragment %d bytes = %d", toolFragments, item.Chars)
			}
			if len(item.Name) != toolFragments {
				t.Errorf("tool fragment %d name length = %d", toolFragments, len(item.Name))
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if events != fragments*3 || toolFragments != fragments {
		t.Fatalf("events = %d, tool fragments = %d", events, toolFragments)
	}
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
		state := streamState{pending: make(map[uint64]*pendingToolCall), handler: func(StreamItem) {}}
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
