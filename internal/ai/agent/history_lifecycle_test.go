package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestRebuildHistoryPairsInterleavedRuns(t *testing.T) {
	rows := []store.MessageRow{
		historyTestRow("c", `{"role":"user","content":"a","jobId":"A"}`),
		historyTestRow("c", `{"type":"toolCall","jobId":"A","id":"call-a","name":"exec_commands","args":{}}`),
		historyTestRow("c", `{"role":"user","content":"b","jobId":"B"}`),
		historyTestRow("c", `{"type":"toolCall","jobId":"B","id":"call-b","name":"exec_commands","args":{}}`),
		historyTestRow("c", `{"type":"toolResult","jobId":"A","id":"call-a","tool":"exec_commands","ok":true,"text":"a result"}`),
		historyTestRow("c", `{"role":"assistant","content":"a done"}`),
		historyTestRow("c", `{"type":"toolResult","jobId":"B","id":"call-b","tool":"exec_commands","ok":true,"text":"b result"}`),
		historyTestRow("c", `{"role":"assistant","content":"b done","jobId":"B"}`),
	}
	messages := rebuildHistory(rows, "next")
	if _, orphans, unanswered := repairHistory(messages); orphans != 0 || unanswered != 0 {
		t.Fatalf("interleaved runs lost pairs: orphans=%d unanswered=%d: %s", orphans, unanswered, describeMessages(messages))
	}
	want := []string{"user:a", "assistant:call-a", "tool:call-a", "assistant:a done", "user:b", "assistant:call-b", "tool:call-b", "assistant:b done"}
	if got := messageShape(messages); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rebuilt history = %v, want %v", got, want)
	}
}

func TestRebuildHistoryJoinsInterimTextWithItsToolCalls(t *testing.T) {
	rows := []store.MessageRow{
		historyTestRow("c", `{"role":"user","content":"check","jobId":"A"}`),
		historyTestRow("c", `{"role":"assistant","content":"先看看磁盘","jobId":"A","interim":true}`),
		historyTestRow("c", `{"type":"toolCall","jobId":"A","id":"call-1","name":"exec_commands","args":{}}`),
		historyTestRow("c", `{"type":"toolCall","jobId":"A","id":"call-2","name":"exec_commands","args":{}}`),
		historyTestRow("c", `{"type":"toolResult","jobId":"A","id":"call-1","tool":"exec_commands","ok":true,"text":"1"}`),
		historyTestRow("c", `{"type":"toolResult","jobId":"A","id":"call-2","tool":"exec_commands","ok":true,"text":"2"}`),
		historyTestRow("c", `{"role":"assistant","content":"done","jobId":"A"}`),
	}
	messages := historyMessages(rows, "next")
	if len(messages) != 5 {
		t.Fatalf("messages = %s", describeMessages(messages))
	}
	carrier := messages[1]
	if carrier.Role != schema.Assistant || carrier.Content != "先看看磁盘" || len(carrier.ToolCalls) != 2 {
		t.Fatalf("interim text and calls must be one assistant message: %+v", carrier)
	}
	if messages[2].ToolCallID != "call-1" || messages[3].ToolCallID != "call-2" || messages[4].Content != "done" {
		t.Fatalf("rebuilt history = %s", describeMessages(messages))
	}
}

func TestRebuildHistoryStartsAutoSummaryBeforeItsRun(t *testing.T) {
	rows := []store.MessageRow{
		historyTestRow("c", `{"role":"user","content":"old question"}`),
		historyTestRow("c", `{"role":"assistant","content":"old answer"}`),
		historyTestRow("c", `{"role":"user","content":"new question","jobId":"A"}`),
		historyTestRow("c", `{"type":"summary","summary":"earlier work","messages":2,"auto":true,"beforeJobId":"A"}`),
		historyTestRow("c", `{"type":"toolCall","jobId":"A","id":"call-1","name":"exec_commands","args":{}}`),
		historyTestRow("c", `{"type":"toolResult","jobId":"A","id":"call-1","tool":"exec_commands","ok":true,"text":"1"}`),
		historyTestRow("c", `{"role":"assistant","content":"new answer","jobId":"A"}`),
	}
	want := []string{"user:[History Summary]\nearlier work", "user:new question", "assistant:call-1", "tool:call-1", "assistant:new answer"}
	if got := messageShape(historyMessages(rows, "B")); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rebuilt history = %q, want %q", got, want)
	}
}

func messageShape(messages []*schema.Message) []string {
	shape := make([]string, 0, len(messages))
	for _, message := range messages {
		switch {
		case message.Role == schema.Tool:
			shape = append(shape, "tool:"+message.ToolCallID)
		case len(message.ToolCalls) > 0:
			ids := make([]string, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				ids = append(ids, call.ID)
			}
			shape = append(shape, string(message.Role)+":"+strings.Join(ids, ","))
		default:
			shape = append(shape, string(message.Role)+":"+message.Content)
		}
	}
	return shape
}

func TestRunSavesInterimTextBeforeToolCalls(t *testing.T) {
	withText := toolCallMessage(namedToolCall("call-1", "todo_write", `{"todos":[]}`))
	withText.Content = "先记下计划"
	runner, storage := testRunner(t, sequenceModel(withText, schema.AssistantMessage("done", nil)), tools.Dependencies{}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	waitClosed(t, stream)
	contents := messageContents(t, storage, response.ConversationID)
	var kinds []string
	for _, content := range contents {
		switch {
		case content["interim"] == true:
			if content["role"] != "assistant" || content["content"] != "先记下计划" || content["jobId"] != response.JobID {
				t.Fatalf("interim row = %+v", content)
			}
			kinds = append(kinds, "interim")
		case content["type"] != nil:
			kinds = append(kinds, content["type"].(string))
		default:
			kinds = append(kinds, content["role"].(string))
			if content["role"] == "assistant" && content["jobId"] != response.JobID {
				t.Fatalf("final answer row lacks its run: %+v", content)
			}
		}
	}
	if got := strings.Join(kinds, ","); got != "user,interim,toolCall,toolResult,assistant" {
		t.Fatalf("row order = %s", got)
	}
	rows, err := storage.MsgList(context.Background(), response.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	messages := historyMessages(rows, "next")
	if len(messages) != 4 || messages[1].Content != "先记下计划" || len(messages[1].ToolCalls) != 1 {
		t.Fatalf("reloaded history = %s", describeMessages(messages))
	}
}

func TestDroppedSteerNeverReachesHistory(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runner := steerRunner(t, &blockingChat{started: started, release: release, final: schema.AssistantMessage("finished", nil)}, tools.Dependencies{}, 0)
	silentPermission(runner)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	waitChannelClosed(t, started, "model did not start")
	if err := runner.Steer(response.JobID, "too-late"); err != nil {
		t.Fatal(err)
	}
	close(release)
	events := waitClosed(t, stream)
	if eventCount(events, "steerDropped") != 1 {
		t.Fatalf("events = %+v", events)
	}
	rows, err := runner.store.MsgList(context.Background(), response.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if strings.Contains(row.ContentJSON, "too-late") {
			t.Fatalf("dropped steer was saved: %s", row.ContentJSON)
		}
	}
	for _, message := range historyMessages(rows, "next") {
		if strings.Contains(message.Content, "too-late") {
			t.Fatalf("dropped steer reaches the next run: %s", describeMessages(historyMessages(rows, "next")))
		}
	}
}

func TestDeliveredSteerIsSavedAfterTheToolResult(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	chat := &recordingChat{}
	chat.step = func(call int, _ []*schema.Message) *schema.Message {
		if call == 1 {
			return toolCallMessage(namedToolCall("probe", "docker_exec", `{"container_id":"web","cmd":"ls"}`))
		}
		return schema.AssistantMessage("done", nil)
	}
	runner := steerRunner(t, chat, tools.Dependencies{DockerExec: blockingDockerExec(started, release)}, 0)
	silentPermission(runner)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	waitChannelClosed(t, started, "tool did not start")
	if err := runner.Steer(response.JobID, "also check logs"); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitClosed(t, stream)
	rows, err := runner.store.MsgList(context.Background(), response.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"user:go", "assistant:probe", "tool:probe", "user:also check logs", "assistant:done"}
	if got := messageShape(historyMessages(rows, "next")); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("history = %q, want %q", got, want)
	}
}

func largeHistoryRunner(t *testing.T, chat *compactionModel) (*Runner, *store.Store, string) {
	t.Helper()
	storage := restartStore(t)
	runner := durableRunner(t, storage, chat, tools.Dependencies{}, nil)
	runner.config.Model = func(context.Context) (model.BaseChatModel, uint64, error) { return chat, 1200, nil }
	ctx := context.Background()
	conversation, err := storage.ConvCreate(ctx, "t", map[string]any{"scope": tools.Scope{SessionID: "session"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []map[string]any{
		{"role": "user", "content": strings.Repeat("问", 600)},
		{"role": "assistant", "content": strings.Repeat("答", 600)},
	} {
		if err := storage.MsgInsert(ctx, conversation.ID, content["role"].(string), content, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	return runner, storage, conversation.ID
}

func TestAutoSummaryIsSavedAndReusedByTheNextRun(t *testing.T) {
	var summaries atomic.Int64
	chat := &compactionModel{}
	chat.generate = func(_ context.Context, input []*schema.Message) (*schema.Message, error) {
		if strings.Contains(input[len(input)-1].Content, "Summarize") {
			summaries.Add(1)
			return schema.AssistantMessage("先前的对话摘要", nil), nil
		}
		return schema.AssistantMessage("标题", nil), nil
	}
	runner, storage, conversationID := largeHistoryRunner(t, chat)
	var firstRunSummaries int64
	for index, message := range []string{"first", "second"} {
		stream := &SliceStream{}
		response, err := runner.Start(context.Background(), ChatArgs{ConversationID: conversationID, Message: message}, StaticStream(stream))
		if err != nil {
			t.Fatal(err)
		}
		waitClosed(t, stream)
		waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
		if index == 0 {
			saved := 0
			for _, content := range messageContents(t, storage, conversationID) {
				if content["type"] == historyTypeSummary {
					saved++
					text, _ := content["summary"].(string)
					if !strings.Contains(text, "先前的对话摘要") || content["beforeJobId"] != response.JobID || content["auto"] != true {
						t.Fatalf("saved summary = %+v", content)
					}
				}
			}
			if saved != 1 {
				t.Fatalf("saved summaries = %d, want 1", saved)
			}
			firstRunSummaries = summaries.Load()
		}
	}
	if firstRunSummaries == 0 || summaries.Load() != firstRunSummaries {
		t.Fatalf("summary calls first=%d total=%d, want the second run to reuse the saved summary", firstRunSummaries, summaries.Load())
	}
	inputs := chat.snapshotInputs()
	last := inputs[len(inputs)-1]
	if !strings.Contains(last[1].Content, "先前的对话摘要") {
		t.Fatalf("second run input does not start from the saved summary: %s", describeMessages(last))
	}
}

func TestFailedSummaryIsNotRetriedWithinTheRun(t *testing.T) {
	var summaries atomic.Int64
	chat := &compactionModel{}
	chat.generate = func(_ context.Context, input []*schema.Message) (*schema.Message, error) {
		if strings.Contains(input[len(input)-1].Content, "Summarize") {
			summaries.Add(1)
			return nil, errors.New("summary model unavailable")
		}
		return schema.AssistantMessage("标题", nil), nil
	}
	var mainCalls atomic.Int64
	chat.stream = func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if mainCalls.Add(1) == 1 {
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("call-1", "todo_write", `{"todos":[]}`))}), nil
		}
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("完成", nil)}), nil
	}
	runner, storage, conversationID := largeHistoryRunner(t, chat)
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{ConversationID: conversationID, Message: "go"}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("terminal counts done=%d failed=%d events=%+v", done, failed, events)
	}
	waitRunStatus(t, storage, response.JobID, store.RunStatusCompleted)
	if summaries.Load() != 1 || mainCalls.Load() != 2 {
		t.Fatalf("summary calls = %d main calls = %d, want one summary attempt across both model calls", summaries.Load(), mainCalls.Load())
	}
	notified := false
	for _, event := range events {
		if event.Type == "status" && event.Detail != nil && *event.Detail == summaryFailedNotice {
			notified = true
		}
	}
	if !notified {
		t.Fatalf("failed summary was not reported: %+v", events)
	}
}

func TestRestoreFailureReportsItsCause(t *testing.T) {
	storage := restartStore(t)
	deps := tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { return nil }}
	runner := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")

	restarted := durableRunner(t, storage, dockerConfirmChat(), deps, nil)
	restarted.config.Model = func(context.Context) (model.BaseChatModel, uint64, error) {
		return nil, 0, errors.New("模型档案已删除")
	}
	if err := restarted.RecoverRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := restarted.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"})
	if err == nil || errors.Is(err, ErrJobNotFound) || !strings.Contains(err.Error(), "模型档案已删除") {
		t.Fatalf("restore failure = %v, want the real cause", err)
	}
}

func TestSteerSavedBeforeItsToolRowsIsRebuiltAfterThem(t *testing.T) {
	encode := func(row map[string]any) store.MessageRow {
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		return store.MessageRow{ConversationID: "conversation", ContentJSON: string(encoded)}
	}
	// The agent loop saved the steer while the event consumer was still saving the turn's tool rows.
	rows := []store.MessageRow{
		encode(map[string]any{"role": "user", "content": "go", "jobId": "job-1"}),
		encode(map[string]any{"role": "user", "content": "also check logs", "steered": true, "jobId": "job-1", "after": []string{"probe"}}),
		encode(map[string]any{"type": historyTypeToolCall, "jobId": "job-1", "id": "probe", "name": "docker_exec", "args": map[string]any{}}),
		encode(map[string]any{"type": historyTypeToolResult, "jobId": "job-1", "id": "probe", "tool": "docker_exec", "ok": true, "text": "ok"}),
		encode(map[string]any{"role": "assistant", "content": "done", "jobId": "job-1"}),
	}
	want := []string{"user:go", "assistant:probe", "tool:probe", "user:also check logs", "assistant:done"}
	if got := messageShape(historyMessages(rows, "next")); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("history = %q, want %q", got, want)
	}
}
