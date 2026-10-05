package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/tools"
	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestMaxIterationsExhaustionEmitsNonRetryableTerminal(t *testing.T) {
	storage := restartStore(t)
	chat := sequenceModel(toolCallMessage(namedToolCall("loop", "todo_write", `{"todos":[]}`)))
	runner := durableRunner(t, storage, chat, tools.Dependencies{}, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	events := waitClosed(t, stream)
	done, failed := terminalCounts(events)
	if done != 0 || failed != 1 {
		t.Fatalf("terminal counts done=%d error=%d", done, failed)
	}
	terminal := events[len(events)-1]
	if terminal.Type != "error" || terminal.Retryable || !terminal.MaxIterations {
		t.Fatalf("terminal event = %+v", terminal)
	}
	if !strings.Contains(terminal.Message, "最大迭代次数（24）") {
		t.Fatalf("terminal message = %q", terminal.Message)
	}
	row := waitRunStatus(t, storage, response.JobID, store.RunStatusFailed)
	if row.Turns != 24 || !strings.Contains(row.Error, "最大迭代次数") {
		t.Fatalf("run row = %+v", row)
	}
	journal := runEventsOf(t, storage, response.JobID)
	last := journal[len(journal)-1]
	if last.Type != "error" || !strings.Contains(last.PayloadJSON, `"maxIterations":true`) || !strings.Contains(last.PayloadJSON, `"retryable":false`) {
		t.Fatalf("terminal journal event = %+v", last)
	}
	messages, err := runner.Messages(context.Background(), response.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	toolCalls := 0
	toolResults := 0
	for _, message := range messages {
		switch message.Role {
		case historyTypeToolCall:
			toolCalls++
		case historyTypeToolResult:
			toolResults++
		}
	}
	if toolCalls != 24 || toolResults != 24 || len(messages) != 49 {
		t.Fatalf("persisted rows: total=%d toolCall=%d toolResult=%d", len(messages), toolCalls, toolResults)
	}
}

func TestMaxIterationsSmallLimitMessage(t *testing.T) {
	runner, _ := testRunner(t, sequenceModel(toolCallMessage(namedToolCall("loop", "todo_write", `{"todos":[]}`))), tools.Dependencies{}, 2)
	stream := &SliceStream{}
	startTestJob(t, runner, stream, "go")
	events := waitClosed(t, stream)
	terminal := events[len(events)-1]
	if terminal.Type != "error" || terminal.Retryable || !terminal.MaxIterations || !strings.Contains(terminal.Message, "最大迭代次数（2）") {
		t.Fatalf("terminal event = %+v", terminal)
	}
}

func userMessageIDWithJob(t *testing.T, storage *store.Store, conversationID, jobID string) string {
	t.Helper()
	rows, err := storage.MsgList(context.Background(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		var persisted struct {
			Role  string `json:"role"`
			JobID string `json:"jobId"`
		}
		if json.Unmarshal([]byte(row.ContentJSON), &persisted) == nil && persisted.Role == "user" && persisted.JobID == jobID {
			return row.ID
		}
	}
	t.Fatalf("user message for job %s not found", jobID)
	return ""
}

func requireCheckpointGone(t *testing.T, storage *store.Store, id string) {
	t.Helper()
	if _, found, err := storage.CheckpointGet(context.Background(), id); err != nil || found {
		t.Fatalf("checkpoint %s survived: found=%v err=%v", id, found, err)
	}
}

func requireHitlBlobGone(t *testing.T, storage *store.Store, id string) {
	t.Helper()
	rows, err := storage.HitlRunList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == id {
			t.Fatalf("hitl blob %s survived", id)
		}
	}
}

func TestEditResendTruncatesAndSupersedesFinishedRuns(t *testing.T) {
	storage := restartStore(t)
	var calls atomic.Int64
	chat := &fakeModel{stream: func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		switch calls.Add(1) {
		case 1:
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("one", nil)}), nil
		case 2:
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("call-9", "todo_write", `{"todos":[]}`))}), nil
		default:
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("two", nil)}), nil
		}
	}}
	runner := durableRunner(t, storage, chat, tools.Dependencies{}, nil)
	first := &SliceStream{}
	firstResponse := startTestJob(t, runner, first, "first")
	waitClosed(t, first)
	second := &SliceStream{}
	secondResponse, err := runner.Start(context.Background(), ChatArgs{ConversationID: firstResponse.ConversationID, Message: "second"}, StaticStream(second))
	if err != nil {
		t.Fatal(err)
	}
	waitClosed(t, second)

	if err := storage.CheckpointSet(context.Background(), secondResponse.JobID, []byte("checkpoint")); err != nil {
		t.Fatal(err)
	}
	if err := storage.HitlRunSave(context.Background(), secondResponse.JobID, []byte("blob")); err != nil {
		t.Fatal(err)
	}
	target := userMessageIDWithJob(t, storage, firstResponse.ConversationID, secondResponse.JobID)
	if err := runner.EditResend(context.Background(), firstResponse.ConversationID, target); err != nil {
		t.Fatal(err)
	}

	rows, err := storage.MsgList(context.Background(), firstResponse.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Role != "user" || rows[1].Role != "assistant" || rows[2].Role != "user" {
		t.Fatalf("truncated rows = %+v", rows)
	}
	secondRow, err := storage.RunGet(context.Background(), secondResponse.JobID)
	if err != nil || secondRow.Status != RunStatusSuperseded {
		t.Fatalf("second run = %+v err=%v", secondRow, err)
	}
	firstRow, err := storage.RunGet(context.Background(), firstResponse.JobID)
	if err != nil || firstRow.Status != store.RunStatusCompleted {
		t.Fatalf("first run = %+v err=%v", firstRow, err)
	}
	requireCheckpointGone(t, storage, secondResponse.JobID)
	requireHitlBlobGone(t, storage, secondResponse.JobID)
}

func TestEditResendCancelsActiveLaterRun(t *testing.T) {
	storage := restartStore(t)
	var calls atomic.Int64
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("one", nil)}), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	runner := durableRunner(t, storage, chat, tools.Dependencies{}, nil)
	first := &SliceStream{}
	firstResponse := startTestJob(t, runner, first, "first")
	waitClosed(t, first)
	second := &SliceStream{}
	secondResponse, err := runner.Start(context.Background(), ChatArgs{ConversationID: firstResponse.ConversationID, Message: "second"}, StaticStream(second))
	if err != nil {
		t.Fatal(err)
	}

	if err := storage.CheckpointSet(context.Background(), secondResponse.JobID, []byte("checkpoint")); err != nil {
		t.Fatal(err)
	}
	target := userMessageIDWithJob(t, storage, firstResponse.ConversationID, secondResponse.JobID)
	if err := runner.EditResend(context.Background(), firstResponse.ConversationID, target); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, second)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("active run terminal counts done=%d error=%d", done, failed)
	}
	row, err := storage.RunGet(context.Background(), secondResponse.JobID)
	if err != nil || row.Status != RunStatusSuperseded || row.FinishedAt == nil {
		t.Fatalf("superseded active run = %+v err=%v", row, err)
	}
	firstRow, err := storage.RunGet(context.Background(), firstResponse.JobID)
	if err != nil || firstRow.Status != store.RunStatusCompleted {
		t.Fatalf("first run = %+v err=%v", firstRow, err)
	}
	requireCheckpointGone(t, storage, secondResponse.JobID)
	requireHitlBlobGone(t, storage, secondResponse.JobID)
}

func TestEditResendLegacyMessageWithoutJob(t *testing.T) {
	storage := restartStore(t)
	conversation, err := storage.ConvCreate(context.Background(), "legacy", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.MsgInsert(context.Background(), conversation.ID, "user", map[string]any{"role": "user", "content": "legacy"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	finished := ids.NowMS()
	earlier := ids.New()
	if err := storage.RunInsert(context.Background(), store.RunRow{ID: earlier, ConversationID: conversation.ID, Status: store.RunStatusCompleted, CreatedAt: 1, UpdatedAt: 1, FinishedAt: &finished}); err != nil {
		t.Fatal(err)
	}
	later := ids.New()
	if err := storage.RunInsert(context.Background(), store.RunRow{ID: later, ConversationID: conversation.ID, Status: store.RunStatusCompleted, CreatedAt: finished + 1000, UpdatedAt: finished + 1000, FinishedAt: &finished}); err != nil {
		t.Fatal(err)
	}
	runner := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("ok", nil)), tools.Dependencies{}, nil)
	rows, err := storage.MsgList(context.Background(), conversation.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v err=%v", rows, err)
	}
	if err := runner.EditResend(context.Background(), conversation.ID, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	earlierRow, err := storage.RunGet(context.Background(), earlier)
	if err != nil || earlierRow.Status != store.RunStatusCompleted {
		t.Fatalf("earlier run = %+v err=%v", earlierRow, err)
	}
	laterRow, err := storage.RunGet(context.Background(), later)
	if err != nil || laterRow.Status != RunStatusSuperseded {
		t.Fatalf("later run = %+v err=%v", laterRow, err)
	}
}

func TestEditResendRejectsNonUserAndMissingMessages(t *testing.T) {
	storage := restartStore(t)
	runner := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("ok", nil)), tools.Dependencies{}, nil)
	conversation, err := storage.ConvCreate(context.Background(), "c", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.MsgInsert(context.Background(), conversation.ID, "assistant", map[string]any{"role": "assistant", "content": "a"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := storage.MsgList(context.Background(), conversation.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %+v err=%v", rows, err)
	}
	if err := runner.EditResend(context.Background(), conversation.ID, rows[0].ID); err == nil || !strings.Contains(err.Error(), "只能编辑用户消息") {
		t.Fatalf("assistant edit error = %v", err)
	}
	if err := runner.EditResend(context.Background(), conversation.ID, ids.New()); err == nil || !strings.Contains(err.Error(), "消息不存在") {
		t.Fatalf("missing edit error = %v", err)
	}
}

type failingRunStore struct {
	RunStore
	failFinishUsage  error
	failUpdateStatus error
	failHitlDelete   error
}

func (f *failingRunStore) RunFinishUsage(ctx context.Context, runID, status, answer, errMsg string, turns int, tokensIn, tokensOut, cacheCreationTokens, latencyMS int64) error {
	if f.failFinishUsage != nil {
		return f.failFinishUsage
	}
	return f.RunStore.RunFinishUsage(ctx, runID, status, answer, errMsg, turns, tokensIn, tokensOut, cacheCreationTokens, latencyMS)
}

func (f *failingRunStore) RunUpdateStatus(ctx context.Context, runID, status string) error {
	if f.failUpdateStatus != nil {
		return f.failUpdateStatus
	}
	return f.RunStore.RunUpdateStatus(ctx, runID, status)
}

func (f *failingRunStore) HitlRunDelete(ctx context.Context, id string) error {
	if f.failHitlDelete != nil {
		return f.failHitlDelete
	}
	return f.RunStore.HitlRunDelete(ctx, id)
}

func failingStoreRunner(t *testing.T, storage *store.Store, chat model.BaseChatModel, runs RunStore) *Runner {
	t.Helper()
	config := Config{
		Model:       func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 32768, nil },
		Tools:       tools.NewRegistry(tools.Dependencies{}),
		Store:       storage,
		Runs:        runs,
		Checkpoints: NewStoreCheckpoints(storage),
	}
	runner := NewRunner(config)
	t.Cleanup(func() { _ = runner.Close() })
	return runner
}

func startTwoFinishedJobs(t *testing.T, runner *Runner) (StartResponse, StartResponse) {
	t.Helper()
	first := &SliceStream{}
	firstResponse := startTestJob(t, runner, first, "first")
	waitClosed(t, first)
	second := &SliceStream{}
	secondResponse, err := runner.Start(context.Background(), ChatArgs{ConversationID: firstResponse.ConversationID, Message: "second"}, StaticStream(second))
	if err != nil {
		t.Fatal(err)
	}
	waitClosed(t, second)
	return firstResponse, secondResponse
}

func TestEditResendStatusUpdateFailureAborts(t *testing.T) {
	storage := restartStore(t)
	failing := &failingRunStore{RunStore: storage, failUpdateStatus: errors.New("status store down")}
	runner := failingStoreRunner(t, storage, sequenceModel(schema.AssistantMessage("ok", nil)), failing)
	firstResponse, secondResponse := startTwoFinishedJobs(t, runner)
	target := userMessageIDWithJob(t, storage, firstResponse.ConversationID, secondResponse.JobID)
	err := runner.EditResend(context.Background(), firstResponse.ConversationID, target)
	if err == nil || !strings.Contains(err.Error(), "更新 AI 运行") {
		t.Fatalf("edit-resend error = %v", err)
	}
	rows, listErr := storage.MsgList(context.Background(), firstResponse.ConversationID)
	if listErr != nil || len(rows) != 4 {
		t.Fatalf("messages must stay intact after failed edit: rows=%d err=%v", len(rows), listErr)
	}
	secondRow, getErr := storage.RunGet(context.Background(), secondResponse.JobID)
	if getErr != nil || secondRow.Status != store.RunStatusCompleted {
		t.Fatalf("second run = %+v err=%v", secondRow, getErr)
	}
}

func TestEditResendFinishUsageFailureAborts(t *testing.T) {
	storage := restartStore(t)
	failing := &failingRunStore{RunStore: storage, failFinishUsage: errors.New("finish store down")}
	var calls atomic.Int64
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("one", nil)}), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	runner := failingStoreRunner(t, storage, chat, failing)
	first := &SliceStream{}
	firstResponse := startTestJob(t, runner, first, "first")
	waitClosed(t, first)
	second := &SliceStream{}
	secondResponse, err := runner.Start(context.Background(), ChatArgs{ConversationID: firstResponse.ConversationID, Message: "second"}, StaticStream(second))
	if err != nil {
		t.Fatal(err)
	}
	target := userMessageIDWithJob(t, storage, firstResponse.ConversationID, secondResponse.JobID)
	editErr := runner.EditResend(context.Background(), firstResponse.ConversationID, target)
	if editErr == nil || !strings.Contains(editErr.Error(), "标记 AI 运行") {
		t.Fatalf("edit-resend error = %v", editErr)
	}
	events := waitClosed(t, second)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("canceled run terminal counts done=%d error=%d", done, failed)
	}
	rows, listErr := storage.MsgList(context.Background(), firstResponse.ConversationID)
	if listErr != nil || len(rows) != 3 {
		t.Fatalf("messages must stay intact after failed edit: rows=%d err=%v", len(rows), listErr)
	}
	secondRow, getErr := storage.RunGet(context.Background(), secondResponse.JobID)
	if getErr != nil || secondRow.Status == RunStatusSuperseded {
		t.Fatalf("second run must not be marked superseded: %+v err=%v", secondRow, getErr)
	}
}

func TestEditResendCleanupFailureAbortsThenRetrySucceeds(t *testing.T) {
	storage := restartStore(t)
	failing := &failingRunStore{RunStore: storage, failHitlDelete: errors.New("blob store down")}
	runner := failingStoreRunner(t, storage, sequenceModel(schema.AssistantMessage("ok", nil)), failing)
	firstResponse, secondResponse := startTwoFinishedJobs(t, runner)
	target := userMessageIDWithJob(t, storage, firstResponse.ConversationID, secondResponse.JobID)
	err := runner.EditResend(context.Background(), firstResponse.ConversationID, target)
	if err == nil || !strings.Contains(err.Error(), "清理 AI 任务") {
		t.Fatalf("edit-resend error = %v", err)
	}
	rows, listErr := storage.MsgList(context.Background(), firstResponse.ConversationID)
	if listErr != nil || len(rows) != 4 {
		t.Fatalf("messages must stay intact after failed edit: rows=%d err=%v", len(rows), listErr)
	}
	secondRow, getErr := storage.RunGet(context.Background(), secondResponse.JobID)
	if getErr != nil || secondRow.Status != RunStatusSuperseded {
		t.Fatalf("second run = %+v err=%v", secondRow, getErr)
	}
	failing.failHitlDelete = nil
	if err := runner.EditResend(context.Background(), firstResponse.ConversationID, target); err != nil {
		t.Fatalf("retry edit-resend: %v", err)
	}
	rows, listErr = storage.MsgList(context.Background(), firstResponse.ConversationID)
	if listErr != nil || len(rows) != 3 {
		t.Fatalf("rows after retry = %d err=%v", len(rows), listErr)
	}
}

func TestEditResendConcurrentToolWriteDuringTruncate(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	deps := tools.Dependencies{DockerExec: func(context.Context, string, string, string) (tools.ExecResult, error) {
		once.Do(func() { close(started) })
		<-release
		return tools.ExecResult{Output: "late output", ExitCode: 0}, nil
	}}
	chat := sequenceModel(
		schema.AssistantMessage("one", nil),
		toolCallMessage(namedToolCall("probe", "docker_exec", `{"container_id":"web","cmd":"ls"}`)),
		schema.AssistantMessage("two", nil),
	)
	runner := steerRunner(t, chat, deps, 0)
	silentPermission(runner)
	first := &SliceStream{}
	firstResponse := startTestJob(t, runner, first, "first")
	waitClosed(t, first)
	second := &SliceStream{}
	secondResponse, err := runner.Start(context.Background(), ChatArgs{ConversationID: firstResponse.ConversationID, Message: "second", Scope: tools.Scope{SessionID: "session"}}, StaticStream(second))
	if err != nil {
		t.Fatal(err)
	}
	waitChannelClosed(t, started, "tool did not start")

	messages, err := runner.Messages(context.Background(), firstResponse.ConversationID)
	if err != nil || len(messages) != 4 {
		t.Fatalf("pre-edit messages = %d err=%v", len(messages), err)
	}
	target := ""
	for _, message := range messages {
		content, ok := message.Content.(map[string]any)
		if ok && message.Role == "user" && content["jobId"] == secondResponse.JobID {
			target = message.ID
		}
	}
	if target == "" {
		t.Fatal("edit target not found")
	}

	editErr := make(chan error, 1)
	go func() {
		editErr <- runner.EditResend(context.Background(), firstResponse.ConversationID, target)
	}()
	time.Sleep(100 * time.Millisecond)
	close(release)
	if err := <-editErr; err != nil {
		t.Fatalf("edit-resend: %v", err)
	}
	messages, err = runner.Messages(context.Background(), firstResponse.ConversationID)
	if err != nil || len(messages) != 3 {
		t.Fatalf("post-edit messages = %d err=%v", len(messages), err)
	}
	if messages[2].Role != "user" {
		t.Fatalf("last surviving row = %+v", messages[2])
	}
	events := waitClosed(t, second)
	if done, failed := terminalCounts(events); done != 0 || failed != 1 {
		t.Fatalf("canceled run terminal counts done=%d error=%d", done, failed)
	}
}

func TestEditResendBlocksConcurrentStartUntilTruncate(t *testing.T) {
	storage := restartStore(t)
	var calls atomic.Int64
	secondModelCalled := make(chan struct{})
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		switch calls.Add(1) {
		case 1:
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("one", nil)}), nil
		case 2:
			close(secondModelCalled)
			<-ctx.Done()
			return nil, ctx.Err()
		default:
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("three", nil)}), nil
		}
	}}
	runner := durableRunner(t, storage, chat, tools.Dependencies{}, nil)
	first := &SliceStream{}
	firstResponse := startTestJob(t, runner, first, "first")
	waitClosed(t, first)
	second := &SliceStream{}
	secondResponse, err := runner.Start(context.Background(), ChatArgs{ConversationID: firstResponse.ConversationID, Message: "second"}, StaticStream(second))
	if err != nil {
		t.Fatal(err)
	}
	waitChannelClosed(t, secondModelCalled, "second run model was not invoked")
	target := userMessageIDWithJob(t, storage, firstResponse.ConversationID, secondResponse.JobID)

	hookEntered := make(chan struct{})
	hookRelease := make(chan struct{})
	editResendTestHook = func() {
		close(hookEntered)
		<-hookRelease
	}
	t.Cleanup(func() { editResendTestHook = nil })
	editDone := make(chan error, 1)
	go func() {
		editDone <- runner.EditResend(context.Background(), firstResponse.ConversationID, target)
	}()
	waitChannelClosed(t, hookEntered, "edit-resend did not reach the truncate gate")

	third := &SliceStream{}
	thirdDone := make(chan error, 1)
	go func() {
		_, err := runner.Start(context.Background(), ChatArgs{ConversationID: firstResponse.ConversationID, Message: "third"}, StaticStream(third))
		thirdDone <- err
	}()
	select {
	case err := <-thirdDone:
		t.Fatalf("concurrent Start completed inside the edit window: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(hookRelease)
	if err := <-editDone; err != nil {
		t.Fatalf("edit-resend: %v", err)
	}
	if err := <-thirdDone; err != nil {
		t.Fatalf("third start: %v", err)
	}
	events := waitClosed(t, third)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("third run terminal counts done=%d error=%d", done, failed)
	}
	rows, err := storage.MsgList(context.Background(), firstResponse.ConversationID)
	if err != nil || len(rows) != 5 {
		t.Fatalf("rows = %d err=%v", len(rows), err)
	}
	if rows[0].Role != "user" || rows[1].Role != "assistant" || rows[2].Role != "user" || rows[3].Role != "user" || rows[4].Role != "assistant" {
		t.Fatalf("row roles = %s, %s, %s, %s, %s", rows[0].Role, rows[1].Role, rows[2].Role, rows[3].Role, rows[4].Role)
	}
	secondRow, err := storage.RunGet(context.Background(), secondResponse.JobID)
	if err != nil || secondRow.Status != RunStatusSuperseded {
		t.Fatalf("second run = %+v err=%v", secondRow, err)
	}
}

func TestEditResendAffectedFollowsMessageOrderWithinSameMillisecond(t *testing.T) {
	storage := restartStore(t)
	ctx := context.Background()
	conversation, err := storage.ConvCreate(ctx, "same-ms", nil)
	if err != nil {
		t.Fatal(err)
	}
	targetRun := "Z0000000000000000000000001"
	laterRun := "A0000000000000000000000001"
	if err := storage.MsgInsert(ctx, conversation.ID, "user", map[string]any{"role": "user", "content": "target", "jobId": targetRun}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := storage.MsgInsert(ctx, conversation.ID, "user", map[string]any{"role": "user", "content": "later", "jobId": laterRun}, nil, nil); err != nil {
		t.Fatal(err)
	}
	created := ids.NowMS()
	finished := created
	for _, id := range []string{targetRun, laterRun} {
		if err := storage.RunInsert(ctx, store.RunRow{ID: id, ConversationID: conversation.ID, Status: store.RunStatusCompleted, CreatedAt: created, UpdatedAt: created, FinishedAt: &finished}); err != nil {
			t.Fatal(err)
		}
	}
	runner := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("ok", nil)), tools.Dependencies{}, nil)
	rows, err := storage.MsgList(ctx, conversation.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows = %d err=%v", len(rows), err)
	}
	if err := runner.EditResend(ctx, conversation.ID, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	laterRow, err := storage.RunGet(ctx, laterRun)
	if err != nil || laterRow.Status != RunStatusSuperseded {
		t.Fatalf("later run with smaller ID must be superseded: %+v err=%v", laterRow, err)
	}
	targetRow, err := storage.RunGet(ctx, targetRun)
	if err != nil || targetRow.Status != RunStatusSuperseded {
		t.Fatalf("target run = %+v err=%v", targetRow, err)
	}
	rows, err = storage.MsgList(ctx, conversation.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows after edit = %d err=%v", len(rows), err)
	}
}

func TestEditResendSupersedesAllLaterRunsWhenStartWinsLock(t *testing.T) {
	storage := restartStore(t)
	runner := durableRunner(t, storage, sequenceModel(schema.AssistantMessage("ok", nil)), tools.Dependencies{}, nil)
	responses := make([]StartResponse, 0, 3)
	for _, message := range []string{"one", "two", "three"} {
		stream := &SliceStream{}
		var response StartResponse
		if len(responses) == 0 {
			response = startTestJob(t, runner, stream, message)
		} else {
			var err error
			response, err = runner.Start(context.Background(), ChatArgs{ConversationID: responses[0].ConversationID, Message: message}, StaticStream(stream))
			if err != nil {
				t.Fatal(err)
			}
		}
		waitClosed(t, stream)
		responses = append(responses, response)
	}
	conversationID := responses[0].ConversationID
	target := userMessageIDWithJob(t, storage, conversationID, responses[0].JobID)
	if err := runner.EditResend(context.Background(), conversationID, target); err != nil {
		t.Fatal(err)
	}
	for index, response := range responses {
		row, err := storage.RunGet(context.Background(), response.JobID)
		if err != nil || row.Status != RunStatusSuperseded {
			t.Fatalf("run %d = %+v err=%v", index, row, err)
		}
	}
	rows, err := storage.MsgList(context.Background(), conversationID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows after edit = %d err=%v", len(rows), err)
	}
}
