package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func busyTestRunner(t *testing.T, chat model.BaseChatModel) (*Runner, *store.Store) {
	t.Helper()
	storage := restartStore(t)
	runner := durableRunner(t, storage, chat, tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}, nil)
	return runner, storage
}

func messageContents(t *testing.T, storage *store.Store, conversationID string) []map[string]any {
	t.Helper()
	rows, err := storage.MsgList(context.Background(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	contents := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		var content map[string]any
		if err := json.Unmarshal([]byte(row.ContentJSON), &content); err != nil {
			t.Fatal(err)
		}
		contents = append(contents, content)
	}
	return contents
}

func TestStartRefusesSecondRunWhileConversationBusy(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	runner, storage := busyTestRunner(t, &blockingChat{started: started, release: release, final: schema.AssistantMessage("first done", nil)})
	first := &SliceStream{}
	response := startTestJob(t, runner, first, "first")
	waitChannelClosed(t, started, "first run did not reach the model")

	second := &SliceStream{}
	_, err := runner.Start(context.Background(), ChatArgs{ConversationID: response.ConversationID, Message: "second"}, StaticStream(second))
	if !errors.Is(err, ErrConversationRunBusy) || !IsConversationBusy(err) {
		t.Fatalf("second start error = %v, want ErrConversationRunBusy", err)
	}
	if _, closed := second.Snapshot(); !closed {
		t.Fatal("refused start must close the stream it opened")
	}
	for _, content := range messageContents(t, storage, response.ConversationID) {
		if content["content"] == "second" {
			t.Fatalf("refused start saved its message: %+v", content)
		}
	}

	close(release)
	waitClosed(t, first)
	waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
	third := &SliceStream{}
	if _, err := runner.Start(context.Background(), ChatArgs{ConversationID: response.ConversationID, Message: "third"}, StaticStream(third)); err != nil {
		t.Fatalf("start after the run finished: %v", err)
	}
	waitClosed(t, third)
}

func TestStartRefusesWhileRestoredRunAwaitsConfirmation(t *testing.T) {
	storage := restartStore(t)
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")

	restarted := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("done", nil), schema.AssistantMessage("next", nil)), deps, nil)
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Start(context.Background(), ChatArgs{ConversationID: response.ConversationID, Message: "new"}, StaticStream(&SliceStream{})); !errors.Is(err, ErrConversationRunBusy) {
		t.Fatalf("start beside a restored pending run = %v, want ErrConversationRunBusy", err)
	}
	if _, err := restarted.CompactConversation(context.Background(), response.ConversationID); !errors.Is(err, ErrConversationBusy) {
		t.Fatalf("compact beside a restored pending run = %v, want ErrConversationBusy", err)
	}

	resumed := &SliceStream{}
	if err := restarted.ConfirmStream(context.Background(), Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}, StaticStream(resumed)); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, resumed)
	waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
	next := &SliceStream{}
	if _, err := restarted.Start(context.Background(), ChatArgs{ConversationID: response.ConversationID, Message: "new"}, StaticStream(next)); err != nil {
		t.Fatalf("start after the restored run finished: %v", err)
	}
	waitClosed(t, next)
}

func TestStaleRunningRowIsReportedInterruptedAndStopsBlocking(t *testing.T) {
	runner, storage := busyTestRunner(t, sequenceModel(schema.AssistantMessage("ok", nil)))
	ctx := context.Background()
	conversation, err := storage.ConvCreate(ctx, "t", map[string]any{"scope": tools.Scope{SessionID: "session"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.RunInsert(ctx, store.RunRow{ID: "stale-run", ConversationID: conversation.ID, Status: store.RunStatusRunning, Source: "chat"}); err != nil {
		t.Fatal(err)
	}

	got, err := runner.RunGet(ctx, "stale-run")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.RunStatusInterrupted || got.FinishedAt == nil || got.Error == "" {
		t.Fatalf("stale run reported as %+v, want interrupted and finished", got)
	}
	listed, err := runner.RunList(ctx, conversation.ID, 0)
	if err != nil || len(listed) != 1 || listed[0].Status != store.RunStatusInterrupted {
		t.Fatalf("run list = %+v err=%v", listed, err)
	}

	stream := &SliceStream{}
	if _, err := runner.Start(ctx, ChatArgs{ConversationID: conversation.ID, Message: "go"}, StaticStream(stream)); err != nil {
		t.Fatalf("stale row blocked a new run: %v", err)
	}
	waitClosed(t, stream)
	row, err := storage.RunGet(ctx, "stale-run")
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.RunStatusInterrupted || row.FinishedAt == nil || row.Error != staleRunMessage {
		t.Fatalf("stale row after start = %+v", row)
	}
}

func TestDeleteConversationRefusesActiveRun(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	runner, storage := busyTestRunner(t, &blockingChat{started: started, release: release, final: schema.AssistantMessage("done", nil)})
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	waitChannelClosed(t, started, "run did not reach the model")

	var removed atomic.Int64
	remove := func(ctx context.Context) error {
		removed.Add(1)
		return storage.ConvDelete(ctx, response.ConversationID)
	}
	if err := runner.DeleteConversation(context.Background(), response.ConversationID, remove); !errors.Is(err, ErrConversationDeleteBusy) {
		t.Fatalf("delete during a run = %v, want ErrConversationDeleteBusy", err)
	}
	if removed.Load() != 0 {
		t.Fatal("delete ran while the conversation had an active run")
	}
	close(release)
	waitClosed(t, stream)
	waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
	if err := runner.DeleteConversation(context.Background(), response.ConversationID, remove); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ConvGet(context.Background(), response.ConversationID); err == nil {
		t.Fatal("conversation still exists after delete")
	}
}

func TestEditResendReplacesUnfinishedRunAnchoredEarlier(t *testing.T) {
	runner, storage := busyTestRunner(t, sequenceModel(schema.AssistantMessage("ok", nil)))
	ctx := context.Background()
	conversation, err := storage.ConvCreate(ctx, "t", map[string]any{"scope": tools.Scope{SessionID: "session"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.MsgInsert(ctx, conversation.ID, "user", map[string]any{"role": "user", "content": "first", "jobId": "pending-run"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := storage.RunInsert(ctx, store.RunRow{ID: "pending-run", ConversationID: conversation.ID, Status: store.RunStatusInterrupted, Source: "chat"}); err != nil {
		t.Fatal(err)
	}
	if err := storage.MsgInsert(ctx, conversation.ID, "user", map[string]any{"role": "user", "content": "later", "steered": true, "jobId": "pending-run"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := storage.MsgList(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.EditResend(ctx, conversation.ID, rows[1].ID); err != nil {
		t.Fatal(err)
	}
	row, err := storage.RunGet(ctx, "pending-run")
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != RunStatusSuperseded || row.FinishedAt == nil {
		t.Fatalf("unfinished run after edit = %+v, want superseded", row)
	}
	stream := &SliceStream{}
	if _, err := runner.Start(ctx, ChatArgs{ConversationID: conversation.ID, Message: "edited"}, StaticStream(stream)); err != nil {
		t.Fatalf("start after edit: %v", err)
	}
	waitClosed(t, stream)
}

func TestCompactLeavesConversationUsableWhileSummarizing(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	chat := &compactionModel{}
	chat.generate = func(ctx context.Context, _ []*schema.Message) (*schema.Message, error) {
		if once.CompareAndSwap(false, true) {
			close(entered)
		}
		select {
		case <-release:
			return schema.AssistantMessage("摘要", nil), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	runner, storage := busyTestRunner(t, chat)
	ctx := context.Background()
	conversation, err := storage.ConvCreate(ctx, "t", map[string]any{"scope": tools.Scope{SessionID: "session"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []map[string]any{{"role": "user", "content": "a"}, {"role": "assistant", "content": "b"}} {
		if err := storage.MsgInsert(ctx, conversation.ID, content["role"].(string), content, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	compacted := make(chan error, 1)
	go func() {
		_, err := runner.CompactConversation(ctx, conversation.ID)
		compacted <- err
	}()
	waitChannelClosed(t, entered, "summary did not start")

	stream := &SliceStream{}
	startCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := runner.Start(startCtx, ChatArgs{ConversationID: conversation.ID, Message: "go"}, StaticStream(stream)); err != nil {
		t.Fatalf("start during /compact: %v", err)
	}
	waitClosed(t, stream)
	close(release)
	select {
	case err := <-compacted:
		if !IsConversationBusy(err) {
			t.Fatalf("compact after new messages = %v, want a busy refusal", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("compact did not return")
	}
	for _, content := range messageContents(t, storage, conversation.ID) {
		if content["type"] == historyTypeSummary {
			t.Fatalf("stale summary saved over newer messages: %+v", content)
		}
	}
}

func TestCloseRecordsRunningRunAsInterruptedAndRetryable(t *testing.T) {
	storage := restartStore(t)
	started := make(chan struct{})
	var once atomic.Bool
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if once.CompareAndSwap(false, true) {
			close(started)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	runner := durableRunner(t, storage, chat, tools.Dependencies{}, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	waitChannelClosed(t, started, "run did not reach the model")
	if err := runner.Close(); err != nil {
		t.Fatal(err)
	}
	row, err := storage.RunGet(context.Background(), response.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.RunStatusInterrupted || row.FinishedAt == nil || row.Error != errRunShutdown.Error() {
		t.Fatalf("run after app close = %+v, want interrupted with the restart wording", row)
	}
	events := runEventsOf(t, storage, response.JobID)
	last := events[len(events)-1]
	var terminal struct {
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	}
	if err := json.Unmarshal([]byte(last.PayloadJSON), &terminal); err != nil {
		t.Fatal(err)
	}
	if last.Type != "error" || !terminal.Retryable || !strings.Contains(terminal.Message, "重启") {
		t.Fatalf("terminal event = %s %s, want a retryable restart error", last.Type, last.PayloadJSON)
	}
}

type flakyFinishStore struct {
	*store.Store
	failures atomic.Int64
}

func (s *flakyFinishStore) RunFinishUsage(ctx context.Context, runID, status, answer, errMsg string, turns int, tokensIn, tokensOut, cacheCreationTokens, latencyMS int64) error {
	if s.failures.Add(-1) >= 0 {
		return errors.New("database is locked")
	}
	return s.Store.RunFinishUsage(ctx, runID, status, answer, errMsg, turns, tokensIn, tokensOut, cacheCreationTokens, latencyMS)
}

func TestRunFinishRetriesTransientStatusWrite(t *testing.T) {
	storage := restartStore(t)
	flaky := &flakyFinishStore{Store: storage}
	flaky.failures.Store(2)
	runner := NewRunner(Config{
		Model: func(context.Context) (model.BaseChatModel, uint64, error) {
			return sequenceModel(schema.AssistantMessage("ok", nil)), 32768, nil
		},
		Tools: tools.NewRegistry(tools.Dependencies{}), Store: storage, Runs: flaky,
		Checkpoints: NewStoreCheckpoints(storage),
	})
	t.Cleanup(func() { _ = runner.Close() })
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	waitClosed(t, stream)
	row := waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
	if row.FinishedAt == nil {
		t.Fatalf("run row = %+v", row)
	}
}
