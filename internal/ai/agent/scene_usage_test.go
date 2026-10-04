package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func sceneRunner(t *testing.T, chat model.BaseChatModel, configure func(*Config)) (*Runner, *store.Store) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	config := Config{
		Model:           func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		ModelForProfile: func(context.Context, string) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools:           tools.NewRegistry(tools.Dependencies{}), Store: storage, Runs: storage,
		Checkpoints: NewStoreCheckpoints(storage),
	}
	if configure != nil {
		configure(&config)
	}
	runner := NewRunner(config)
	t.Cleanup(func() {
		_ = runner.Close()
		_ = storage.Close()
	})
	return runner, storage
}

func TestRunWithExplicitProfileUsesProfileFactoryAndRecordsProfile(t *testing.T) {
	message := assistantWithUsage("完成", 30, 12)
	message.Extra = map[string]any{provider.MessageExtraCacheCreation: uint64(7)}
	chat := &fakeModel{steps: []fakeStep{{chunks: []*schema.Message{message}}}}
	var profileCalls atomic.Int64
	runner, storage := sceneRunner(t, chat, func(config *Config) {
		config.ModelForProfile = func(_ context.Context, profileID string) (model.BaseChatModel, uint64, error) {
			if profileID != "profile-42" {
				t.Fatalf("unexpected profile ID %q", profileID)
			}
			profileCalls.Add(1)
			return chat, 32768, nil
		}
	})
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "hi", Scope: tools.Scope{}, ModelProfileID: "profile-42"}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	waitClosed(t, stream)
	row, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if row.ProfileID != "profile-42" {
		t.Fatalf("run profile = %q, want profile-42", row.ProfileID)
	}
	if row.TokensIn != 30 || row.TokensOut != 12 || row.CacheCreationTokens != 7 {
		t.Fatalf("run usage = %+v", row)
	}
	if row.LatencyMS < 0 {
		t.Fatalf("latency = %d", row.LatencyMS)
	}
	if profileCalls.Load() == 0 {
		t.Fatal("profile model factory was not used")
	}
}

func TestRunWithExplicitProfileRequiresFactory(t *testing.T) {
	chat := &fakeModel{steps: []fakeStep{{chunks: []*schema.Message{schema.AssistantMessage("完成", nil)}}}}
	runner, _ := sceneRunner(t, chat, func(config *Config) {
		config.ModelForProfile = nil
	})
	stream := &SliceStream{}
	_, err := runner.Start(context.Background(), ChatArgs{Message: "hi", Scope: tools.Scope{}, ModelProfileID: "profile-42"}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
	if !strings.Contains(events[len(events)-1].Message, "未配置") {
		t.Fatalf("error event = %+v", events[len(events)-1])
	}
}

func TestRecoveredRunKeepsJournaledUsage(t *testing.T) {
	storage := restartStore(t)
	conversation, err := storage.ConvCreate(context.Background(), "recover", map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.RunInsert(context.Background(), store.RunRow{ID: "run-recover", ConversationID: conversation.ID, Status: store.RunStatusRunning, Source: "cron", ProfileID: "profile-1"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := storage.RunAppendEvent(context.Background(), "run-recover", "usage", func(seq uint64) ([]byte, error) {
			return []byte(`{"type":"usage","seq":1,"promptTokens":100,"completionTokens":40,"cachedTokens":10,"cacheCreationTokens":5,"latencyMs":300}`), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewRunner(Config{Store: storage, Runs: storage, Model: func(context.Context) (model.BaseChatModel, uint64, error) {
		return &fakeModel{steps: []fakeStep{{chunks: []*schema.Message{schema.AssistantMessage("x", nil)}}}}, 32768, nil
	}})
	t.Cleanup(func() { _ = runner.Close() })
	runner.finishRecoveredRun(context.Background(), "run-recover", store.RunStatusInterrupted, "AI 任务因应用重启而中断", true)
	row, err := storage.RunGet(context.Background(), "run-recover")
	if err != nil {
		t.Fatal(err)
	}
	if row.TokensIn != 200 || row.TokensOut != 80 || row.CacheCreationTokens != 10 || row.LatencyMS != 600 {
		t.Fatalf("recovered usage = %+v", row)
	}
	if row.ProfileID != "profile-1" {
		t.Fatalf("recovered profile = %q", row.ProfileID)
	}
}

func TestUsageEventCarriesCacheCreationAndLatency(t *testing.T) {
	message := assistantWithUsage("完成", 30, 12)
	message.Extra = map[string]any{provider.MessageExtraCacheCreation: uint64(9)}
	chat := &fakeModel{steps: []fakeStep{{chunks: []*schema.Message{message}}}}
	runner, _ := sceneRunner(t, chat, nil)
	stream := &SliceStream{}
	startTestJob(t, runner, stream, "hi")
	waitClosed(t, stream)
	usage := waitEvent(t, stream, "usage")
	if usage.CacheCreationTokens != 9 {
		t.Fatalf("usage event = %+v", usage)
	}
}
