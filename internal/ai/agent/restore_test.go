package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestRestoreResumesWithoutDuplicatingStructuredRows(t *testing.T) {
	storage := restartStore(t)
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")

	rows, err := storage.MsgList(context.Background(), response.ConversationID)
	if err != nil || len(rows) != 2 || rows[1].Role != historyTypeToolCall {
		t.Fatalf("pre-restart rows = %+v err=%v", rows, err)
	}

	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), deps, nil)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	resumed := &SliceStream{}
	if err := restarted.ConfirmStream(context.Background(), Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, resumed)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("resumed terminal counts done=%d error=%d", done, failed)
	}

	rows, err = storage.MsgList(context.Background(), response.ConversationID)
	if err != nil || len(rows) != 4 {
		t.Fatalf("post-resume rows = %d err=%v", len(rows), err)
	}
	if rows[0].Role != "user" || rows[1].Role != historyTypeToolCall || rows[2].Role != historyTypeToolResult || rows[3].Role != "assistant" {
		t.Fatalf("post-resume row roles = %s, %s, %s, %s", rows[0].Role, rows[1].Role, rows[2].Role, rows[3].Role)
	}
}

func TestRestoredHistoryReloadsToolPairIntoModelInput(t *testing.T) {
	storage := restartStore(t)
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")

	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), deps, nil)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	resumed := &SliceStream{}
	if err := restarted.ConfirmStream(context.Background(), Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, resumed)

	var captured []*schema.Message
	var calls atomic.Int64
	capture := &fakeModel{stream: func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			captured = messages
		}
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
	}}
	third := durableRunner(t, storage, capture, deps, nil)
	thirdStream := &SliceStream{}
	if _, err := third.Start(context.Background(), ChatArgs{ConversationID: response.ConversationID, Message: "next"}, StaticStream(thirdStream)); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, thirdStream)

	if len(captured) != 6 {
		t.Fatalf("model input = %d messages: %s", len(captured), describeMessages(captured))
	}
	if captured[2].Role != schema.Assistant || len(captured[2].ToolCalls) != 1 || captured[2].ToolCalls[0].ID != "call" {
		t.Fatalf("captured[2] = %+v", captured[2])
	}
	if captured[3].Role != schema.Tool || captured[3].ToolCallID != "call" {
		t.Fatalf("captured[3] = %+v", captured[3])
	}
	if captured[4].Role != schema.Assistant || captured[4].Content != "done" {
		t.Fatalf("captured[4] = %+v", captured[4])
	}
	if captured[5].Role != schema.User || captured[5].Content != "next" {
		t.Fatalf("captured[5] = %+v", captured[5])
	}
}

func TestRecoverRunsSupersededRunStaysFinished(t *testing.T) {
	storage := restartStore(t)
	conversation, err := storage.ConvCreate(context.Background(), "c", nil)
	if err != nil {
		t.Fatal(err)
	}
	finished := int64(1)
	if err := storage.RunInsert(context.Background(), store.RunRow{ID: "superseded-run", ConversationID: conversation.ID, Status: RunStatusSuperseded, CreatedAt: 1, UpdatedAt: 1, FinishedAt: &finished}); err != nil {
		t.Fatal(err)
	}
	runner := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("ok", nil)), tools.Dependencies{}, nil)
	if err := runner.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	row, err := storage.RunGet(context.Background(), "superseded-run")
	if err != nil || row.Status != RunStatusSuperseded || row.FinishedAt == nil {
		t.Fatalf("superseded run after recovery = %+v err=%v", row, err)
	}
}

func TestRestoredJobNotifyCarriesConversationOwner(t *testing.T) {
	storage := restartStore(t)
	conversation, err := storage.ConvCreate(ipc.WithUserID(context.Background(), "user-1"), "owned", map[string]any{"scope": tools.Scope{SessionID: "session"}})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var owners []string
	deps := tools.Dependencies{
		DockerAct: func(context.Context, string, string, string) error { return nil },
		Notify: func(ctx context.Context, _ string) error {
			mu.Lock()
			owners = append(owners, tools.NotifyOwnerID(ctx))
			mu.Unlock()
			return nil
		},
	}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{ConversationID: conversation.ID, Message: "go", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")

	restarted := durableRunner(t, storage, sequenceModel(
		toolCallMessage(namedToolCall("n1", "notify", `{"message":"恢复完成"}`)),
		schema.AssistantMessage("done", nil),
	), deps, nil)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	resumed := &SliceStream{}
	if err := restarted.ConfirmStream(context.Background(), Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, resumed)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("resumed terminal counts done=%d error=%d", done, failed)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(owners) != 1 || owners[0] != "user-1" {
		t.Fatalf("notify owners = %v", owners)
	}
}

func TestRecoveredUsageDeduplicatesCallIDsAndKeepsLegacyEvents(t *testing.T) {
	storage := restartStore(t)
	conversation, err := storage.ConvCreate(context.Background(), "usage replay", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.RunInsert(context.Background(), store.RunRow{ID: "run-usage-replay", ConversationID: conversation.ID, Status: store.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		callID      string
		channel     string
		prompt      int
		completion  int
		cacheCreate int
		latency     int
	}{
		{callID: "summary:agent-1", channel: "summary", prompt: 100, completion: 20, cacheCreate: 3, latency: 10},
		{callID: "provider-main", prompt: 40, completion: 7, cacheCreate: 4, latency: 20},
		{callID: "summary:agent-1", channel: "summary", prompt: 100, completion: 20, cacheCreate: 3, latency: 10},
		{prompt: 5, completion: 1, cacheCreate: 1, latency: 1},
		{prompt: 5, completion: 1, cacheCreate: 1, latency: 1},
	} {
		event := event
		callID := ""
		if event.callID != "" {
			callID = fmt.Sprintf(`,"callId":%q`, event.callID)
		}
		channel := ""
		if event.channel != "" {
			channel = fmt.Sprintf(`,"channel":%q`, event.channel)
		}
		if _, err := storage.RunAppendEvent(context.Background(), "run-usage-replay", "usage", func(seq uint64) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"type":"usage","seq":%d%s%s,"promptTokens":%d,"completionTokens":%d,"cacheCreationTokens":%d,"latencyMs":%d}`, seq, callID, channel, event.prompt, event.completion, event.cacheCreate, event.latency)), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewRunner(Config{Store: storage, Runs: storage})
	t.Cleanup(func() { _ = runner.Close() })
	total := runner.recoveredUsage(context.Background(), "run-usage-replay")
	if total.PromptTokens != 150 || total.CompletionTokens != 29 || total.CacheCreationTokens != 9 || total.LatencyMS != 32 {
		t.Fatalf("recovered usage = %+v, want 150/29 cache 9 latency 32", total)
	}
}

func TestRecoveredUsageKeepsUnchanneledCallsWithSharedProviderID(t *testing.T) {
	storage := restartStore(t)
	conversation, err := storage.ConvCreate(context.Background(), "legacy provider replay", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.RunInsert(context.Background(), store.RunRow{ID: "run-legacy-provider", ConversationID: conversation.ID, Status: store.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		prompt      int
		completion  int
		cacheCreate int
		latency     int
	}{
		{prompt: 100, completion: 20, cacheCreate: 3, latency: 10},
		{prompt: 50, completion: 9, cacheCreate: 4, latency: 20},
	} {
		event := event
		if _, err := storage.RunAppendEvent(context.Background(), "run-legacy-provider", "usage", func(seq uint64) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"type":"usage","seq":%d,"callId":"summary:provider-shared","promptTokens":%d,"completionTokens":%d,"cacheCreationTokens":%d,"latencyMs":%d}`, seq, event.prompt, event.completion, event.cacheCreate, event.latency)), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewRunner(Config{Store: storage, Runs: storage})
	t.Cleanup(func() { _ = runner.Close() })
	total := runner.recoveredUsage(context.Background(), "run-legacy-provider")
	if total.PromptTokens != 150 || total.CompletionTokens != 29 || total.CacheCreationTokens != 7 || total.LatencyMS != 30 {
		t.Fatalf("unchanneled provider calls were deduplicated: %+v", total)
	}
}

func TestRestoreCompactionDoesNotDoubleCountNewSummary(t *testing.T) {
	ctx := context.Background()
	storage := restartStore(t)
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	oldMain := compactionUsageMessage("", 10, 2, "shared-provider")
	oldMain.ToolCalls = []schema.ToolCall{namedToolCall("call", "docker_control", `{"container_id":"web","action":"start"}`)}
	runner := durableRunner(t, storage, sequenceModel(oldMain), deps, nil)
	runner.config.Model = func(context.Context) (model.BaseChatModel, uint64, error) { return sequenceModel(oldMain), 1200, nil }
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")

	rows, err := storage.MsgList(ctx, response.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	padding := 2550 - estimatedMessages(historyMessages(rows, response.JobID))
	if padding <= 0 {
		t.Fatalf("unexpected base history size: %d", estimatedMessages(historyMessages(rows, response.JobID)))
	}
	if err := storage.MsgInsert(ctx, response.ConversationID, "user", map[string]any{"role": "user", "content": strings.Repeat("x", padding)}, nil, nil); err != nil {
		t.Fatal(err)
	}
	rows, err = storage.MsgList(ctx, response.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if got := estimatedMessages(historyMessages(rows, response.JobID)); got != 2550 {
		t.Fatalf("history size = %d, want 2550", got)
	}

	summaryCalls := 0
	restoredChat := &compactionModel{}
	restoredChat.generate = func(context.Context, []*schema.Message) (*schema.Message, error) {
		summaryCalls++
		time.Sleep(5 * time.Millisecond)
		return compactionUsageMessage("摘要", 100, 20, "shared-provider"), nil
	}
	restoredChat.stream = func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		return schema.StreamReaderFromArray([]*schema.Message{compactionUsageMessage("完成", 40, 7, "shared-provider")}), nil
	}
	restarted := durableRunner(t, storage, restoredChat, deps, nil)
	restarted.config.Model = func(context.Context) (model.BaseChatModel, uint64, error) { return restoredChat, 1200, nil }
	if err := restarted.RecoverRuns(ctx); err != nil {
		t.Fatal(err)
	}
	resumed := &SliceStream{}
	if err := restarted.ConfirmStream(ctx, Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, resumed)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("terminal counts done=%d failed=%d events=%+v", done, failed, events)
	}
	if summaryCalls == 0 || len(restoredChat.snapshotInputs()) != 1 {
		t.Fatalf("summary calls=%d main calls=%d, want summary plus one resumed main call", summaryCalls, len(restoredChat.snapshotInputs()))
	}
	row, err := storage.RunGet(ctx, response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	wantPrompt := int64(50 + 100*summaryCalls)
	wantCompletion := int64(9 + 20*summaryCalls)
	wantCacheCreation := int64(4 + 10*summaryCalls)
	if row.TokensIn != wantPrompt || row.TokensOut != wantCompletion || row.CacheCreationTokens != wantCacheCreation || row.LatencyMS < int64(5*summaryCalls) {
		t.Fatalf("restored usage = %+v, want old main + %d summary calls + resumed main exactly once: %d/%d cache %d", row, summaryCalls, wantPrompt, wantCompletion, wantCacheCreation)
	}
	usageEvents := 0
	for _, event := range runEventsOf(t, storage, response.JobID) {
		if event.Type == "usage" {
			usageEvents++
		}
	}
	if usageEvents != summaryCalls+2 {
		t.Fatalf("usage events = %d, want old main + %d summary calls + resumed main", usageEvents, summaryCalls)
	}
}

func TestRestoredJobOperationsRequireRequestOwner(t *testing.T) {
	storage := restartStore(t)
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	runner.config.ServerMode = true
	alice := ipc.WithUserID(context.Background(), "alice")
	stream := &SliceStream{}
	response, err := runner.Start(alice, ChatArgs{Message: "go", Scope: tools.Scope{SessionID: "session"}}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")

	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil)), deps, nil)
	restarted.config.ServerMode = true
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	bob := ipc.WithUserID(context.Background(), "bob")
	if err := restarted.CancelContext(bob, response.JobID); err != ErrJobForbidden {
		t.Fatalf("foreign restored cancel = %v", err)
	}
	if err := restarted.ConfirmStreamContext(context.Background(), Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, nil); err != ErrJobForbidden {
		t.Fatalf("unknown restored confirm = %v", err)
	}
	if err := restarted.ConfirmStreamContext(bob, Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, nil); err != ErrJobForbidden {
		t.Fatalf("foreign restored confirm = %v", err)
	}
	if current := restarted.lookupJob(response.JobID); current != nil {
		t.Fatal("denied confirm restored the job")
	}
	snapshot, err := restarted.HITLSnapshot(response.JobID)
	if err != nil || snapshot.Terminal != nil || len(snapshot.Pending) != 1 {
		t.Fatalf("restored state after denied operations = %+v err=%v", snapshot, err)
	}

	resumed := &SliceStream{}
	alicePeer := ipc.WithUserID(context.Background(), "alice")
	if err := restarted.ConfirmStreamContext(alicePeer, Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, resumed)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("resumed terminal counts done=%d error=%d", done, failed)
	}
}
