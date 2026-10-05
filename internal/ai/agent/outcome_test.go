package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

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
