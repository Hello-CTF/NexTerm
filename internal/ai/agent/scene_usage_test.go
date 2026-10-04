package agent

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/subagent"
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

func TestSubagentRunPersistsUsageWithExplicitProfile(t *testing.T) {
	parent := sequenceModel(
		toolCallMessage(namedToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task","modelProfileId":"profile-42"}`)),
		schema.AssistantMessage("parent done", nil),
	)
	childMessage := schema.AssistantMessage("child done", nil)
	childMessage.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 50, CompletionTokens: 20}}
	childMessage.Extra = map[string]any{provider.MessageExtraCacheCreation: uint64(3)}
	child := sequenceModel(childMessage)
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	var profileCalls atomic.Int64
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return parent, 32768, nil },
		ModelForProfile: func(_ context.Context, profileID string) (model.BaseChatModel, uint64, error) {
			if profileID != "profile-42" {
				t.Fatalf("unexpected subagent profile %q", profileID)
			}
			profileCalls.Add(1)
			return child, 32768, nil
		},
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage, Runs: storage,
		Checkpoints: NewStoreCheckpoints(storage),
		Subagents: &tools.SubagentConfig{
			Model: func(context.Context) (model.BaseChatModel, error) { return child, nil },
			ModelForProfile: func(_ context.Context, profileID string) (model.BaseChatModel, error) {
				profileCalls.Add(1)
				return child, nil
			},
		},
	})
	t.Cleanup(func() { _ = runner.Close() })
	stream := &SliceStream{}
	startTestJob(t, runner, stream, "go")
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
	rows, err := storage.RunList(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var subagentRow *store.RunRow
	for index := range rows {
		if rows[index].Source == "subagent" {
			subagentRow = &rows[index]
		}
	}
	if subagentRow == nil {
		t.Fatalf("no subagent run persisted: %+v", rows)
	}
	if subagentRow.ProfileID != "profile-42" || subagentRow.Status != store.RunStatusCompleted || subagentRow.FinishedAt == nil {
		t.Fatalf("subagent row = %+v", *subagentRow)
	}
	if subagentRow.TokensIn != 50 || subagentRow.TokensOut != 20 || subagentRow.CacheCreationTokens != 3 || subagentRow.Turns != 1 {
		t.Fatalf("subagent usage = %+v", *subagentRow)
	}
	if profileCalls.Load() == 0 {
		t.Fatal("subagent profile factory was not used")
	}
}

func TestSubagentRunPersistsWithDefaultProfile(t *testing.T) {
	parent := sequenceModel(
		toolCallMessage(namedToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`)),
		schema.AssistantMessage("parent done", nil),
	)
	child := sequenceModel(schema.AssistantMessage("child done", nil))
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return parent, 32768, nil },
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage, Runs: storage,
		Checkpoints: NewStoreCheckpoints(storage),
		Subagents: &tools.SubagentConfig{
			Model:           func(context.Context) (model.BaseChatModel, error) { return child, nil },
			ActiveProfileID: func() string { return "active-profile" },
		},
	})
	t.Cleanup(func() { _ = runner.Close() })
	stream := &SliceStream{}
	startTestJob(t, runner, stream, "go")
	waitClosed(t, stream)
	rows, err := storage.RunList(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Source == "subagent" {
			if row.ProfileID != "active-profile" {
				t.Fatalf("subagent row profile = %q", row.ProfileID)
			}
			return
		}
	}
	t.Fatalf("no subagent run persisted: %+v", rows)
}

func TestHITLResumeRestoresProfileAndMergesUsage(t *testing.T) {
	storage := restartStore(t)
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	confirmCall := toolCallMessage(namedToolCall("call", "docker_control", `{"container_id":"web","action":"start"}`))
	confirmCall.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 100, CompletionTokens: 10}}
	confirmCall.Extra = map[string]any{provider.MessageExtraCacheCreation: uint64(4)}
	var defaultCalls, profileCalls atomic.Int64
	newConfiguredRunner := func(chat model.BaseChatModel) *Runner {
		runner := NewRunner(Config{
			Model: func(context.Context) (model.BaseChatModel, uint64, error) {
				defaultCalls.Add(1)
				return chat, 32768, nil
			},
			ModelForProfile: func(_ context.Context, profileID string) (model.BaseChatModel, uint64, error) {
				if profileID != "profile-42" {
					t.Fatalf("unexpected profile %q", profileID)
				}
				profileCalls.Add(1)
				return chat, 32768, nil
			},
			Tools: tools.NewRegistry(deps), Store: storage, Runs: storage,
			Checkpoints: NewStoreCheckpoints(storage),
		})
		t.Cleanup(func() { _ = runner.Close() })
		return runner
	}
	first := newConfiguredRunner(sequenceModel(confirmCall, schema.AssistantMessage("done", nil)))
	stream := &SliceStream{}
	response, err := first.Start(context.Background(), ChatArgs{Message: "go", Scope: tools.Scope{SessionID: "session"}, ModelProfileID: "profile-42"}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")

	restarted := newConfiguredRunner(sequenceModel(assistantWithUsage("done", 50, 5)))
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusInterrupted)

	resumed := &SliceStream{}
	if err := restarted.ConfirmStream(context.Background(), Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	if done, failed := terminalCounts(waitClosed(t, resumed)); done != 1 || failed != 0 {
		t.Fatalf("resumed run did not complete")
	}
	row := waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
	if row.ProfileID != "profile-42" {
		t.Fatalf("resumed run profile = %q", row.ProfileID)
	}
	if row.TokensIn != 150 || row.TokensOut != 15 || row.CacheCreationTokens != 4 {
		t.Fatalf("merged usage = %+v, want pre 100/10/4 + post 50/5", row)
	}
	if defaultCalls.Load() != 0 {
		t.Fatalf("default factory used %d times, want 0", defaultCalls.Load())
	}
	if profileCalls.Load() < 2 {
		t.Fatalf("profile factory calls = %d, want initial + resume", profileCalls.Load())
	}
}

func TestRecoveredUsageSaturatesInsteadOfWrapping(t *testing.T) {
	storage := restartStore(t)
	conversation, err := storage.ConvCreate(context.Background(), "saturate", map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.RunInsert(context.Background(), store.RunRow{ID: "run-saturate", ConversationID: conversation.ID, Status: store.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	huge := uint64(math.MaxUint64 - 10)
	for i := 0; i < 3; i++ {
		if _, err := storage.RunAppendEvent(context.Background(), "run-saturate", "usage", func(seq uint64) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"type":"usage","seq":%d,"promptTokens":%d,"completionTokens":%d,"cacheCreationTokens":%d,"latencyMs":%d}`, seq, huge, huge, huge, huge)), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewRunner(Config{Store: storage, Runs: storage})
	t.Cleanup(func() { _ = runner.Close() })
	runner.finishRecoveredRun(context.Background(), "run-saturate", store.RunStatusInterrupted, "interrupted", true)
	row, err := storage.RunGet(context.Background(), "run-saturate")
	if err != nil {
		t.Fatal(err)
	}
	maxInt64 := int64(math.MaxInt64)
	if row.TokensIn != maxInt64 || row.TokensOut != maxInt64 || row.CacheCreationTokens != maxInt64 || row.LatencyMS != maxInt64 {
		t.Fatalf("saturated row = %+v", row)
	}
}
