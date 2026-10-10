package takeover

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func panicSnapshotOnCall(h *harness, target int64) {
	var calls atomic.Int64
	h.snapshot = func(ctx context.Context, tabID string) (tools.Screen, error) {
		if calls.Add(1) == target {
			panic("终端快照崩溃 password=hunter2")
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.screen, nil
	}
}

func requireSafePanicText(t *testing.T, text string) {
	t.Helper()
	if !strings.Contains(text, "执行时崩溃") {
		t.Errorf("missing crash diagnostics: %q", text)
	}
	if strings.Contains(text, "hunter2") || !strings.Contains(text, "<redacted>") {
		t.Errorf("panic text not redacted: %q", text)
	}
	if strings.Contains(text, "goroutine") || strings.Contains(text, ".go:") || strings.ContainsAny(text, "\n\r") {
		t.Errorf("panic text carries a stack or multiple lines: %q", text)
	}
}

func TestReadScreenPanicIsolatedAndRunContinues(t *testing.T) {
	var calls atomic.Int64
	var modelSaw atomic.Bool
	chat := &fakeModel{stream: func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("scr", "read_screen", `{}`))}), nil
		}
		for _, message := range messages {
			if message.Role != schema.Tool || message.ToolName != "read_screen" {
				continue
			}
			var output tools.Output
			if err := json.Unmarshal([]byte(message.Content), &output); err != nil {
				t.Errorf("unmarshal tool output: %v", err)
				continue
			}
			if !output.OK && output.Panic && strings.Contains(output.Text, "read_screen") {
				requireSafePanicText(t, output.Text)
				modelSaw.Store(true)
			}
		}
		return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(doneCall("recovered"))}), nil
	}}
	h := newHarness(t, chat)
	panicSnapshotOnCall(h, 3)
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	events := waitClosed(t, stream)
	done, failed := counts(events)
	if done != 1 || failed != 0 {
		t.Fatalf("tool panic killed the takeover run: done=%d failed=%d events=%+v", done, failed, events)
	}
	if !modelSaw.Load() {
		t.Fatal("model never received the structured panic failure")
	}
	for _, event := range events {
		if event.Type == "error" {
			requireSafePanicText(t, event.Message)
		}
	}
}

func TestSendKeysPanicEmitsStructuredToolResult(t *testing.T) {
	var calls atomic.Int64
	chat := &fakeModel{stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("keys", "send_keys", `{"keys":"ls","enter":true}`))}), nil
		}
		return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(doneCall("recovered"))}), nil
	}}
	h := newHarness(t, chat)
	panicSnapshotOnCall(h, 3)
	stream := &agent.SliceStream{}
	h.run(t, stream, RunArgs{})
	events := waitClosed(t, stream)
	var result agent.Event
	for _, event := range events {
		if event.Type == "toolResult" && event.ID == "keys" {
			result = event
		}
		if event.Type == "error" {
			t.Fatalf("panic surfaced as run error: %+v", event)
		}
	}
	if result.ID == "" {
		t.Fatalf("missing toolResult event: %+v", events)
	}
	if result.OK || !result.Panic {
		t.Fatalf("toolResult = %+v, want structured panic failure", result)
	}
	requireSafePanicText(t, result.Text)
	requireSafePanicText(t, result.Summary)
	done, failed := counts(events)
	if done != 1 || failed != 0 {
		t.Fatalf("run did not complete cleanly: done=%d failed=%d", done, failed)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.aiWrites) != 0 {
		t.Fatal("keys were written after the snapshot panic")
	}
}
