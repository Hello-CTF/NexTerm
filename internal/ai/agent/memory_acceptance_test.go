package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/memory"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
)

func TestRealProviderMemoryAcceptance(t *testing.T) {
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
	memoryStore, err := memory.Open(context.Background(), filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	scope := memory.Scope{Tenant: "acceptance", Subject: "default"}
	if _, err := memoryStore.Create(ctx, scope, memory.CreateInput{Topic: "operations", Content: "web-01 的 nginx 每天 02:00 重启"}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := memoryStore.UpdateSettings(ctx, scope, memory.SettingsInput{InjectionEnabled: &enabled, ToolsEnabled: &enabled}, 0); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return chatModel, contextWindow, nil },
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage, MaxTurns: 4,
		Memory: memoryStore, MemoryScope: scope,
	})
	defer runner.Close()
	stream := &SliceStream{}
	_, err = runner.Start(ctx, ChatArgs{
		Message: "这是一次验收测试。先根据长期记忆回答：web-01 的 nginx 每天几点重启？然后调用 memory_save 工具，把「验收环境时区为 UTC+8」保存到主题 acceptance。最后只回复 OK。",
	}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	events := waitClosedTimeout(t, stream, 110*time.Second)
	done, failed := terminalCounts(events)
	if done != 1 || failed != 0 {
		t.Fatalf("real provider memory terminal events: done=%d error=%d events=%+v", done, failed, events)
	}

	var answer strings.Builder
	saved := false
	for _, event := range events {
		if event.Type == "delta" {
			answer.WriteString(event.Text)
		}
		if event.Type == "done" {
			answer.WriteString(event.Answer)
		}
		if event.Type == "toolResult" && event.OK && strings.Contains(event.Summary, "已保存记忆") {
			saved = true
		}
	}
	if !strings.Contains(answer.String(), "02:00") {
		t.Fatalf("injected memory canary never reached the assistant answer: %q", answer.String())
	}
	if !saved {
		t.Fatalf("memory_save never succeeded: %+v", events)
	}
	index, err := memoryStore.Index(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, topic := range index {
		if topic.Topic != "acceptance" {
			continue
		}
		for _, entry := range topic.Entries {
			content, err := memoryStore.Get(ctx, scope, entry.ID)
			if err != nil {
				t.Fatal(err)
			}
			found = found || strings.Contains(content.Content, "UTC+8")
		}
	}
	if !found {
		t.Fatalf("memory_save did not round-trip into the store: %+v", index)
	}
}
