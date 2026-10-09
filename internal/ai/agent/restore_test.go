package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
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
