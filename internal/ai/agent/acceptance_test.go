package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ai/provider"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
)

func acceptanceTemperature(t *testing.T) *float64 {
	t.Helper()
	raw := os.Getenv("NEXTERM_AI_TEMPERATURE")
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		t.Fatalf("NEXTERM_AI_TEMPERATURE: %v", err)
	}
	return &value
}

func acceptanceClient(t *testing.T, baseURL, modelName string) baseChatModelClient {
	t.Helper()
	client, err := provider.NewClient(provider.Config{BaseURL: baseURL, APIKey: os.Getenv("NEXTERM_AI_API_KEY"), Model: modelName, Temperature: acceptanceTemperature(t), ContextWindow: 32768, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := any(client).(baseChatModelClient)
	if !ok {
		t.Fatal("provider does not expose the final Eino BaseChatModel factory")
	}
	return adapter
}

func TestRealProviderAcceptance(t *testing.T) {
	if os.Getenv("NEXTERM_AI_ACCEPTANCE_PROVIDER") != "1" {
		t.Skip("set NEXTERM_AI_ACCEPTANCE_PROVIDER=1 and provider environment to run")
	}
	baseURL, modelName := os.Getenv("NEXTERM_AI_BASE_URL"), os.Getenv("NEXTERM_AI_MODEL")
	if baseURL == "" || modelName == "" {
		t.Fatal("NEXTERM_AI_BASE_URL and NEXTERM_AI_MODEL are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	chatModel, contextWindow, err := acceptanceClient(t, baseURL, modelName).BaseChatModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return chatModel, contextWindow, nil },
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage, MaxTurns: 2,
	})
	defer runner.Close()
	stream := &SliceStream{}
	_, err = runner.Start(ctx, ChatArgs{Message: "这是一次验收测试。不要调用任何工具，只回复 OK。"}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	events := waitClosedTimeout(t, stream, 90*time.Second)
	done, failed := terminalCounts(events)
	if done != 1 || failed != 0 {
		t.Fatalf("real provider terminal events: done=%d error=%d events=%+v", done, failed, events)
	}
}

func waitConfirmOrTerminal(t *testing.T, stream *SliceStream, timeout time.Duration) Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		events, closed := stream.Snapshot()
		for _, event := range events {
			if event.Type == "confirmRequired" {
				return event
			}
		}
		if closed {
			t.Fatalf("run finished without confirmRequired: %+v", events)
		}
		time.Sleep(10 * time.Millisecond)
	}
	events, _ := stream.Snapshot()
	t.Fatalf("timed out waiting for confirmRequired: %+v", events)
	return Event{}
}

func TestRealProviderHITLAcceptance(t *testing.T) {
	if os.Getenv("NEXTERM_AI_ACCEPTANCE_PROVIDER") != "1" {
		t.Skip("set NEXTERM_AI_ACCEPTANCE_PROVIDER=1 and provider environment to run")
	}
	baseURL, modelName := os.Getenv("NEXTERM_AI_BASE_URL"), os.Getenv("NEXTERM_AI_MODEL")
	if baseURL == "" || modelName == "" {
		t.Fatal("NEXTERM_AI_BASE_URL and NEXTERM_AI_MODEL are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	chatModel, contextWindow, err := acceptanceClient(t, baseURL, modelName).BaseChatModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	transport, directory := localTransport(t)
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return chatModel, contextWindow, nil },
		Tools: tools.NewRegistry(localDeps(transport)), Store: storage, MaxTurns: 4,
	})
	defer runner.Close()
	stream := &SliceStream{}
	target := filepath.Join(directory, "nexterm-hitl-acceptance.txt")
	response, err := runner.Start(ctx, ChatArgs{
		Message: "这是一次验收测试。请调用 write_file 工具将文件 " + target + " 的内容写为 ok，等待确认后，只回复 OK。",
		Scope:   tools.Scope{SessionID: "session"},
	}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitConfirmOrTerminal(t, stream, 110*time.Second)
	if confirmation.RequestID == "" || confirmation.Nonce == "" {
		t.Fatalf("confirmRequired missing request binding: %+v", confirmation)
	}
	snapshot, err := runner.HITLSnapshot(response.JobID)
	if err != nil || len(snapshot.Pending) != 1 || snapshot.Pending[0].ID != confirmation.RequestID {
		t.Fatalf("snapshot = %+v err=%v", snapshot, err)
	}
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosedTimeout(t, stream, 120*time.Second)
	done, failed := terminalCounts(events)
	if done != 1 || failed != 0 {
		t.Fatalf("real provider HITL terminal events: done=%d error=%d events=%+v", done, failed, events)
	}
	content, err := os.ReadFile(target)
	if err != nil || strings.TrimSpace(string(content)) != "ok" {
		t.Fatalf("confirmed write did not land: content=%q err=%v", content, err)
	}
	replay, err := runner.HITLEvents(response.JobID, 0)
	if err != nil || len(replay) != 3 || replay[0].Kind != hitl.EventInterrupted || replay[1].Kind != hitl.EventResumed || replay[2].Kind != hitl.EventTerminal {
		t.Fatalf("HITL replay = %+v err=%v", replay, err)
	}
}
