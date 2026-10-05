package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/guard"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/profiles"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/provider"
	"github.com/ProbiusOfficial/NexTerm/internal/ai/steer"
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
			ModelForProfile: func(context.Context, string) (model.BaseChatModel, error) { return child, nil },
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

func subagentPersistenceRunner(t *testing.T, parent, child model.BaseChatModel, configure func(*tools.SubagentConfig)) (*Runner, *store.Store) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	config := &tools.SubagentConfig{
		Model:           func(context.Context) (model.BaseChatModel, error) { return child, nil },
		ModelForProfile: func(context.Context, string) (model.BaseChatModel, error) { return child, nil },
	}
	if configure != nil {
		configure(config)
	}
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return parent, 32768, nil },
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage, Runs: storage,
		Checkpoints: NewStoreCheckpoints(storage),
		Subagents:   config,
	})
	t.Cleanup(func() { _ = runner.Close() })
	return runner, storage
}

func findSubagentRow(t *testing.T, storage *store.Store) *store.RunRow {
	t.Helper()
	rows, err := storage.RunList(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for index := range rows {
		if rows[index].Source == "subagent" {
			return &rows[index]
		}
	}
	t.Fatalf("no subagent run persisted: %+v", rows)
	return nil
}

func TestSubagentRunPersistsFailureAfterUsage(t *testing.T) {
	usageCall := toolCallMessage(namedToolCall("t-1", "todo_write", `{"todos":[]}`))
	usageCall.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 100, CompletionTokens: 10}}
	usageCall.Extra = map[string]any{provider.MessageExtraCacheCreation: uint64(2)}
	var calls atomic.Int64
	child := &fakeModel{stream: func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{usageCall}), nil
		}
		return nil, errors.New("model exploded")
	}}
	parent := sequenceModel(
		toolCallMessage(namedToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`)),
		schema.AssistantMessage("parent done", nil),
	)
	runner, storage := subagentPersistenceRunner(t, parent, child, nil)
	stream := &SliceStream{}
	startTestJob(t, runner, stream, "go")
	waitClosed(t, stream)
	row := findSubagentRow(t, storage)
	if row.Status != store.RunStatusFailed || row.FinishedAt == nil {
		t.Fatalf("failed subagent row = %+v", *row)
	}
	if row.TokensIn != 100 || row.TokensOut != 10 || row.CacheCreationTokens != 2 || row.Turns != 1 {
		t.Fatalf("failed subagent usage = %+v", *row)
	}
}

func TestSubagentRunPersistsCancelAfterUsage(t *testing.T) {
	usageCall := toolCallMessage(namedToolCall("t-1", "todo_write", `{"todos":[]}`))
	usageCall.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 60, CompletionTokens: 5}}
	var calls atomic.Int64
	blocking := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{usageCall}), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	parent := sequenceModel(
		toolCallMessage(namedToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`)),
		schema.AssistantMessage("parent done", nil),
	)
	runner, storage := subagentPersistenceRunner(t, parent, blocking, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	_ = waitEvent(t, stream, "subagentToolCall")
	if err := runner.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, stream)
	row := findSubagentRow(t, storage)
	if row.Status != store.RunStatusCanceled || row.FinishedAt == nil {
		t.Fatalf("canceled subagent row = %+v", *row)
	}
	if row.TokensIn != 60 || row.TokensOut != 5 || row.Turns != 1 {
		t.Fatalf("canceled subagent usage = %+v", *row)
	}
}

func TestSubagentDefaultProfilePinnedAcrossActiveSwitch(t *testing.T) {
	release := make(chan struct{})
	child := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		select {
		case <-release:
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("child done", nil)}), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	parent := sequenceModel(
		toolCallMessage(namedToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`)),
		schema.AssistantMessage("parent done", nil),
	)
	var active atomic.Value
	active.Store("profile-a")
	pinned := make(chan struct{})
	var pinnedOnce sync.Once
	runner, storage := subagentPersistenceRunner(t, parent, child, func(config *tools.SubagentConfig) {
		config.ActiveProfileID = func() string {
			pinnedOnce.Do(func() { close(pinned) })
			return active.Load().(string)
		}
	})
	stream := &SliceStream{}
	startTestJob(t, runner, stream, "go")
	<-pinned
	active.Store("profile-b")
	close(release)
	waitClosed(t, stream)
	row := findSubagentRow(t, storage)
	if row.ProfileID != "profile-a" {
		t.Fatalf("subagent profile = %q, want spawn-time profile-a", row.ProfileID)
	}
}

func TestStartPinsActiveProfileForFactoryAndPersistence(t *testing.T) {
	ctx := context.Background()
	storage, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	manager, err := profiles.NewManager(ctx, storage)
	if err != nil {
		t.Fatal(err)
	}
	overviewA, err := manager.Save(ctx, profiles.Profile{Name: "A", BaseURL: "https://a.example/v1", APIKey: "ka", Model: "ma"})
	if err != nil {
		t.Fatal(err)
	}
	overviewB, err := manager.Save(ctx, profiles.Profile{Name: "B", BaseURL: "https://b.example/v1", APIKey: "kb", Model: "mb"})
	if err != nil {
		t.Fatal(err)
	}
	profileA := overviewA.Profiles[0].ID
	profileB := overviewB.Profiles[len(overviewB.Profiles)-1].ID
	entered := make(chan string, 4)
	release := make(chan struct{})
	runner := NewRunner(Config{
		Profiles: manager,
		Model: func(context.Context) (model.BaseChatModel, uint64, error) {
			t.Fatal("default factory must not be used when profiles are configured")
			return nil, 0, nil
		},
		ModelForProfile: func(_ context.Context, profileID string) (model.BaseChatModel, uint64, error) {
			entered <- profileID
			<-release
			return sequenceModel(schema.AssistantMessage("done", nil)), 32768, nil
		},
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage, Runs: storage,
		Checkpoints: NewStoreCheckpoints(storage),
	})
	t.Cleanup(func() { _ = runner.Close() })
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "go", Scope: tools.Scope{}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if _, err := manager.Activate(ctx, profileB); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitClosed(t, stream)
	row, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if row.ProfileID != profileA {
		t.Fatalf("run profile = %q, want spawn-time active %q", row.ProfileID, profileA)
	}
	for {
		select {
		case id := <-entered:
			if id != profileA {
				t.Fatalf("model factory resolved switched profile %q, want %q", id, profileA)
			}
		default:
			return
		}
	}
}

func TestHITLResumeKeepsSaturatedLatency(t *testing.T) {
	storage := restartStore(t)
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	confirmCall := toolCallMessage(namedToolCall("call", "docker_control", `{"container_id":"web","action":"start"}`))
	confirmCall.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 1}}
	newRunner := func(chat model.BaseChatModel) *Runner {
		runner := NewRunner(Config{
			Model:           func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
			ModelForProfile: func(context.Context, string) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
			Tools:           tools.NewRegistry(deps), Store: storage, Runs: storage,
			Checkpoints: NewStoreCheckpoints(storage),
		})
		t.Cleanup(func() { _ = runner.Close() })
		return runner
	}
	first := newRunner(sequenceModel(confirmCall, schema.AssistantMessage("done", nil)))
	stream := &SliceStream{}
	response, err := first.Start(context.Background(), ChatArgs{Message: "go", Scope: tools.Scope{SessionID: "session"}, ModelProfileID: "profile-42"}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")
	if _, err := storage.RunAppendEvent(context.Background(), response.JobID, "usage", func(seq uint64) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"type":"usage","seq":%d,"promptTokens":5,"completionTokens":1,"latencyMs":%d}`, seq, int64(math.MaxInt64))), nil
	}); err != nil {
		t.Fatal(err)
	}

	restarted := newRunner(sequenceModel(assistantWithUsage("done", 50, 5)))
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
	if row.LatencyMS != math.MaxInt64 {
		t.Fatalf("resumed latency = %d, want saturated %d", row.LatencyMS, int64(math.MaxInt64))
	}
	if row.TokensIn <= 10 {
		t.Fatalf("resumed tokens = %d, want merged pre+post", row.TokensIn)
	}
}

func TestParentRunSalvagesUsageOnStreamError(t *testing.T) {
	usageFrame := assistantWithUsage("partial", 70, 30)
	usageFrame.Extra = map[string]any{provider.MessageExtraCacheCreation: uint64(6)}
	chat := &fakeModel{stream: func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		reader, writer := schema.Pipe[*schema.Message](2)
		go func() {
			writer.Send(usageFrame, nil)
			writer.Send(nil, errors.New("connection lost"))
		}()
		return reader, nil
	}}
	runner, storage := sceneRunner(t, chat, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
	row := waitRunStatus(t, storage, response.JobID, store.RunStatusFailed)
	if row.TokensIn != 70 || row.TokensOut != 30 || row.CacheCreationTokens != 6 {
		t.Fatalf("salvaged usage = %+v", row)
	}
	found := false
	for _, event := range runEventsOf(t, storage, response.JobID) {
		if event.Type == "usage" && strings.Contains(event.PayloadJSON, `"promptTokens":70`) {
			found = true
		}
	}
	if !found {
		t.Fatal("salvaged usage event missing from journal")
	}
}

func TestSubagentRunSalvagesUsageOnStreamError(t *testing.T) {
	usageFrame := schema.AssistantMessage("partial", nil)
	usageFrame.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 55, CompletionTokens: 20}}
	usageFrame.Extra = map[string]any{provider.MessageExtraCacheCreation: uint64(4)}
	child := &fakeModel{stream: func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		reader, writer := schema.Pipe[*schema.Message](2)
		go func() {
			writer.Send(usageFrame, nil)
			writer.Send(nil, errors.New("connection lost"))
		}()
		return reader, nil
	}}
	parent := sequenceModel(
		toolCallMessage(namedToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`)),
		schema.AssistantMessage("parent done", nil),
	)
	runner, storage := subagentPersistenceRunner(t, parent, child, nil)
	stream := &SliceStream{}
	startTestJob(t, runner, stream, "go")
	waitClosed(t, stream)
	row := findSubagentRow(t, storage)
	if row.Status != store.RunStatusFailed || row.FinishedAt == nil {
		t.Fatalf("salvaged subagent row = %+v", *row)
	}
	if row.TokensIn != 55 || row.TokensOut != 20 || row.CacheCreationTokens != 4 {
		t.Fatalf("salvaged subagent usage = %+v", *row)
	}
}

type gatedRunStore struct {
	*store.Store
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (g *gatedRunStore) RunInsert(ctx context.Context, row store.RunRow) error {
	if row.Source == "subagent" {
		g.once.Do(func() { close(g.entered) })
		select {
		case <-g.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return g.Store.RunInsert(ctx, row)
}

func TestSubagentPersistencePrecedesParentStreamClose(t *testing.T) {
	childMessage := schema.AssistantMessage("child done", nil)
	childMessage.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 40, CompletionTokens: 8}}
	child := sequenceModel(childMessage)
	parent := sequenceModel(
		toolCallMessage(namedToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`)),
		schema.AssistantMessage("parent done", nil),
	)
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	gated := &gatedRunStore{Store: storage, gate: make(chan struct{}), entered: make(chan struct{})}
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return parent, 32768, nil },
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage, Runs: gated,
		Checkpoints: NewStoreCheckpoints(storage),
		Subagents: &tools.SubagentConfig{
			Model:           func(context.Context) (model.BaseChatModel, error) { return child, nil },
			ModelForProfile: func(context.Context, string) (model.BaseChatModel, error) { return child, nil },
		},
	})
	t.Cleanup(func() { _ = runner.Close() })
	stream := &SliceStream{}
	startTestJob(t, runner, stream, "go")
	<-gated.entered
	if _, closed := stream.Snapshot(); closed {
		t.Fatal("parent stream closed before subagent persistence finished")
	}
	close(gated.gate)
	waitClosed(t, stream)
	row := findSubagentRow(t, storage)
	if row.TokensIn != 40 || row.Status != store.RunStatusCompleted {
		t.Fatalf("subagent row = %+v", *row)
	}
}

func TestEarlyCancelDuringInitializationClosesSubagents(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	runner, _ := sceneRunner(t, &fakeModel{stream: func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		once.Do(func() { close(entered) })
		<-release
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
	}}, func(config *Config) {
		config.Subagents = &tools.SubagentConfig{
			Model: func(context.Context) (model.BaseChatModel, error) {
				return sequenceModel(schema.AssistantMessage("child", nil)), nil
			},
			ModelForProfile: func(context.Context, string) (model.BaseChatModel, error) {
				return sequenceModel(schema.AssistantMessage("child", nil)), nil
			},
		}
	})
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "go", Scope: tools.Scope{}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := runner.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, stream)
	close(release)
	row, err := runner.runs.RunGet(context.Background(), response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.RunStatusCanceled {
		t.Fatalf("row status = %q, want canceled", row.Status)
	}
}

type hangingSubagentStore struct {
	*store.Store
	entered chan struct{}
	once    sync.Once
}

func (h *hangingSubagentStore) RunInsert(ctx context.Context, row store.RunRow) error {
	if row.Source == "subagent" {
		h.once.Do(func() { close(h.entered) })
		<-ctx.Done()
		return ctx.Err()
	}
	return h.Store.RunInsert(ctx, row)
}

type failingSubagentStore struct {
	*store.Store
}

func (f *failingSubagentStore) RunInsert(ctx context.Context, row store.RunRow) error {
	if row.Source == "subagent" {
		return errors.New("disk exploded")
	}
	return f.Store.RunInsert(ctx, row)
}

func TestSubagentSlowWriteFailurePropagatesAndStoreClosesClean(t *testing.T) {
	childMessage := schema.AssistantMessage("child done", nil)
	childMessage.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 40, CompletionTokens: 8}}
	child := sequenceModel(childMessage)
	parent := sequenceModel(
		toolCallMessage(namedToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`)),
		schema.AssistantMessage("parent done", nil),
	)
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	hanging := &hangingSubagentStore{Store: storage, entered: make(chan struct{})}
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return parent, 32768, nil },
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage, Runs: hanging,
		Checkpoints: NewStoreCheckpoints(storage),
		Subagents: &tools.SubagentConfig{
			Model:           func(context.Context) (model.BaseChatModel, error) { return child, nil },
			ModelForProfile: func(context.Context, string) (model.BaseChatModel, error) { return child, nil },
		},
	})
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	<-hanging.entered
	events := waitClosedTimeout(t, stream, 20*time.Second)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
	row, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.RunStatusCanceled || !strings.Contains(row.Error, "context deadline exceeded") {
		t.Fatalf("parent row = %+v", row)
	}
	rows, err := storage.RunList(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range rows {
		if candidate.Source == "subagent" {
			t.Fatalf("subagent row persisted despite failed write: %+v", candidate)
		}
	}
	if err := runner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("store closed with a live writer: %v", err)
	}
}

func TestSubagentWriteFailureJoinsCancellation(t *testing.T) {
	usageCall := toolCallMessage(namedToolCall("t-1", "todo_write", `{"todos":[]}`))
	usageCall.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 60, CompletionTokens: 5}}
	var calls atomic.Int64
	blocking := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{usageCall}), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	parent := sequenceModel(
		toolCallMessage(namedToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`)),
		schema.AssistantMessage("parent done", nil),
	)
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	failing := &failingSubagentStore{Store: storage}
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return parent, 32768, nil },
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage, Runs: failing,
		Checkpoints: NewStoreCheckpoints(storage),
		Subagents: &tools.SubagentConfig{
			Model:           func(context.Context) (model.BaseChatModel, error) { return blocking, nil },
			ModelForProfile: func(context.Context, string) (model.BaseChatModel, error) { return blocking, nil },
		},
	})
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	_ = waitEvent(t, stream, "subagentToolCall")
	if err := runner.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, stream)
	row, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.RunStatusCanceled {
		t.Fatalf("parent row status = %q, want canceled", row.Status)
	}
	if !strings.Contains(row.Error, "context canceled") || !strings.Contains(row.Error, "disk exploded") {
		t.Fatalf("parent row error must retain cancellation and write failure: %q", row.Error)
	}
	rows, err := storage.RunList(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range rows {
		if candidate.Source == "subagent" {
			t.Fatalf("subagent row persisted despite failed write: %+v", candidate)
		}
	}
	if err := runner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("store closed with a live writer: %v", err)
	}
}

func TestCloseContextDeadlineStillDrainsWritersBeforeStoreClose(t *testing.T) {
	childMessage := schema.AssistantMessage("child done", nil)
	childMessage.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 40, CompletionTokens: 8}}
	child := sequenceModel(childMessage)
	parent := sequenceModel(
		toolCallMessage(namedToolCall("sp-1", subagent.SpawnToolName, `{"task":"child task"}`)),
		schema.AssistantMessage("parent done", nil),
	)
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	hanging := &hangingSubagentStore{Store: storage, entered: make(chan struct{})}
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) { return parent, 32768, nil },
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage, Runs: hanging,
		Checkpoints: NewStoreCheckpoints(storage),
		Subagents: &tools.SubagentConfig{
			Model:           func(context.Context) (model.BaseChatModel, error) { return child, nil },
			ModelForProfile: func(context.Context, string) (model.BaseChatModel, error) { return child, nil },
		},
	})
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	<-hanging.entered
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := runner.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext err = %v, want caller deadline preserved", err)
	}
	if err := runner.Close(); err != nil {
		t.Fatalf("Close after deadline must drain, got %v", err)
	}
	row, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.RunStatusCanceled || !strings.Contains(row.Error, "context deadline exceeded") {
		t.Fatalf("parent row = %+v", row)
	}
	rows, err := storage.RunList(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range rows {
		if candidate.Source == "subagent" {
			t.Fatalf("subagent row persisted despite failed write: %+v", candidate)
		}
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("store closed with a live writer: %v", err)
	}
}

func TestParkForShutdownPropagatesSubagentWriteFailure(t *testing.T) {
	storage := restartStore(t)
	conversation, err := storage.ConvCreate(context.Background(), "parked", map[string]any{"scope": nil})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.RunInsert(context.Background(), store.RunRow{ID: "parked-1", ConversationID: conversation.ID, Status: store.RunStatusInterrupted}); err != nil {
		t.Fatal(err)
	}
	manager, err := subagent.NewManager(subagent.Config{
		NewModel: func(context.Context) (model.BaseChatModel, error) {
			return sequenceModel(schema.AssistantMessage("child", nil)), nil
		},
		OnFinish: func(context.Context, subagent.Request, subagent.Result) error {
			return errors.New("disk exploded")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := manager.Spawn(context.Background(), subagent.Request{Task: "child", Scope: &subagent.Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Wait(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(Config{Store: storage, Runs: storage})
	current := &job{
		id: "parked-1", ctx: context.Background(), cancel: func() {},
		stream: discardStream{}, memory: guard.NewMemory(), steer: steer.NewQueue(1), subagents: manager,
	}
	runner.mu.Lock()
	runner.jobs[current.id] = current
	runner.mu.Unlock()
	runner.wg.Add(1)
	err = runner.CloseContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "disk exploded") {
		t.Fatalf("CloseContext err = %v, want parked subagent write failure", err)
	}
	row, err := storage.RunGet(context.Background(), "parked-1")
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.RunStatusInterrupted || row.FinishedAt != nil {
		t.Fatalf("parked run terminal state changed: %+v", row)
	}
	if err := runner.Close(); err == nil || !strings.Contains(err.Error(), "disk exploded") {
		t.Fatalf("repeated Close must keep reporting the drain error, got %v", err)
	}
}
