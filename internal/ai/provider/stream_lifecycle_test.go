package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func sseServer(t *testing.T, frames []string, hold <-chan struct{}, released chan<- struct{}) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range frames {
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", frame)
			writer.(http.Flusher).Flush()
		}
		if hold == nil {
			_, _ = io.WriteString(writer, "data: [DONE]\n\n")
			return
		}
		select {
		case <-request.Context().Done():
			close(released)
		case <-hold:
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestStreamMergedFramesCarryExactMetadata(t *testing.T) {
	server := sseServer(t, []string{
		`{"model":"served-m","choices":[{"delta":{"content":"a"}}]}`,
		`{"model":"served-m","choices":[{"delta":{"content":"b"}}]}`,
		`{"model":"served-m","choices":[{"delta":{"content":"c"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
	}, nil, nil)
	client, err := NewClient(Config{BaseURL: server.URL, Model: "m", Stream: true, ContextWindow: 1000})
	if err != nil {
		t.Fatal(err)
	}
	chat, _ := client.ChatModel(context.Background())
	reader, err := chat.Stream(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var frames []*schema.Message
	for {
		frame, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
	}
	if len(frames) < 3 {
		t.Fatalf("frames = %d", len(frames))
	}
	merged, err := schema.ConcatMessages(frames)
	if err != nil {
		t.Fatal(err)
	}
	runID := extraString(frames[0].Extra, MessageExtraRunID)
	callID := extraString(frames[0].Extra, MessageExtraCallID)
	if merged.Content != "abc" || runID == "" || callID == "" ||
		extraString(merged.Extra, MessageExtraRunID) != runID ||
		extraString(merged.Extra, MessageExtraCallID) != callID ||
		extraString(merged.Extra, MessageExtraServingModel) != "served-m" ||
		extraUint64(merged.Extra, MessageExtraContextWindow) != 1000 {
		t.Fatalf("merged = %q %+v", merged.Content, merged.Extra)
	}
}

func TestStreamClosingReaderReleasesStalledUpstream(t *testing.T) {
	hold := make(chan struct{})
	released := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	server := sseServer(t, []string{`{"choices":[{"delta":{"content":"a"}}]}`}, hold, released)
	client, err := NewClient(Config{BaseURL: server.URL, Model: "m", Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	chat, _ := client.ChatModel(context.Background())
	reader, err := chat.Stream(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if frame, err := reader.Recv(); err != nil || frame.Content != "a" {
		t.Fatalf("first frame = %+v err=%v", frame, err)
	}
	reader.Close()
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream request was not cancelled after the reader closed")
	}
}

func TestStreamToolCallsWithoutIndex(t *testing.T) {
	call := func(id, name, args string) *schema.Message {
		fragment := schema.ToolCall{ID: id, Function: schema.FunctionCall{Name: name, Arguments: args}}
		return &schema.Message{ToolCalls: []schema.ToolCall{fragment}}
	}
	indexed := func(value int, fragment *schema.Message) *schema.Message {
		fragment.ToolCalls[0].Index = &value
		return fragment
	}
	finish := func(frames ...*schema.Message) ([]ToolCall, []*schema.Message) {
		state := streamState{pending: make(map[uint64]*pendingToolCall)}
		for _, frame := range frames {
			state.consumeMessage(frame)
		}
		state.finishTools()
		return state.completion.ToolCalls, frames
	}
	t.Run("distinct ids stay separate", func(t *testing.T) {
		calls, frames := finish(call("a", "read", `{"p`), call("", "", `":1}`), call("b", "write", `{"q":2}`))
		if len(calls) != 2 || calls[0].ID != "a" || calls[0].Function.Arguments != `{"p":1}` || calls[1].ID != "b" || calls[1].Function.Name != "write" {
			t.Fatalf("calls = %+v", calls)
		}
		merged, err := schema.ConcatMessages(frames)
		if err != nil || len(merged.ToolCalls) != 2 {
			t.Fatalf("merged = %+v err=%v", merged, err)
		}
	})
	t.Run("continuation without id appends", func(t *testing.T) {
		calls, _ := finish(call("a", "read", `{"a"`), call("", "", `:1`), call("", "", `}`))
		if len(calls) != 1 || calls[0].Function.Arguments != `{"a":1}` {
			t.Fatalf("calls = %+v", calls)
		}
	})
	t.Run("repeated id continues", func(t *testing.T) {
		calls, _ := finish(call("a", "read", `{`), call("a", "", `}`))
		if len(calls) != 1 || calls[0].Function.Arguments != `{}` {
			t.Fatalf("calls = %+v", calls)
		}
	})
	t.Run("indexed streams unchanged", func(t *testing.T) {
		calls, _ := finish(indexed(0, call("a", "read", `{`)), indexed(1, call("b", "write", `{`)), indexed(0, call("", "", `}`)), indexed(1, call("", "", `}`)))
		if len(calls) != 2 || calls[0].Function.Arguments != `{}` || calls[1].ID != "b" || calls[1].Function.Arguments != `{}` {
			t.Fatalf("calls = %+v", calls)
		}
	})
}
