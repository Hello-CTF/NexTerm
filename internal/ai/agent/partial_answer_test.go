package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/steer"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ai/usage"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// streamThenWait streams the given text frames and then holds the turn open
// until the run is canceled.
func streamThenWait(frames ...string) *fakeModel {
	return &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		reader, writer := schema.Pipe[*schema.Message](len(frames) + 1)
		go func() {
			defer writer.Close()
			for _, frame := range frames {
				writer.Send(schema.AssistantMessage(frame, nil), nil)
			}
			<-ctx.Done()
			writer.Send(nil, ctx.Err())
		}()
		return reader, nil
	}}
}

// streamThenFail streams the given frames and then fails the turn.
func streamThenFail(frames ...*schema.Message) func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		reader, writer := schema.Pipe[*schema.Message](len(frames) + 1)
		go func() {
			defer writer.Close()
			for _, frame := range frames {
				writer.Send(cloneFakeMessage(frame), nil)
			}
			writer.Send(nil, errors.New("stream exploded"))
		}()
		return reader, nil
	}
}

type storedRow struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content string `json:"content"`
	JobID   string `json:"jobId"`
	Interim bool   `json:"interim"`
	Partial bool   `json:"partial"`
	ID      string `json:"id"`
}

func storedRows(t *testing.T, storage *store.Store, conversationID string) []storedRow {
	t.Helper()
	rows, err := storage.MsgList(context.Background(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]storedRow, 0, len(rows))
	for _, row := range rows {
		var decoded storedRow
		if err := json.Unmarshal([]byte(row.ContentJSON), &decoded); err != nil {
			t.Fatal(err)
		}
		out = append(out, decoded)
	}
	return out
}

func partialRows(rows []storedRow) []storedRow {
	var out []storedRow
	for _, row := range rows {
		if row.Partial {
			out = append(out, row)
		}
	}
	return out
}

func deltaText(events []Event) string {
	var b strings.Builder
	for _, event := range events {
		if event.Type == "delta" {
			b.WriteString(event.Text)
		}
	}
	return b.String()
}

func requirePartialLast(t *testing.T, rows []storedRow, jobID, text string) {
	t.Helper()
	partials := partialRows(rows)
	if len(partials) != 1 {
		t.Fatalf("partial rows = %+v, want exactly one", partials)
	}
	last := rows[len(rows)-1]
	if !last.Partial || last.Role != "assistant" || last.Content != text || last.JobID != jobID || last.Interim {
		t.Fatalf("last row = %+v, want partial %q of %s", last, text, jobID)
	}
}

func TestPartialAnswerSavedWhenStreamFails(t *testing.T) {
	chat := &fakeModel{stream: streamThenFail(schema.AssistantMessage("部分", nil), schema.AssistantMessage("回答", nil))}
	storage := restartStore(t)
	runner := durableRunner(t, storage, chat, tools.Dependencies{}, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	events := waitClosed(t, stream)
	if _, failed := terminalCounts(events); failed != 1 {
		t.Fatalf("events = %+v", events)
	}
	// The row is saved before the terminal event, so it is there once the
	// stream closes.
	requirePartialLast(t, storedRows(t, storage, response.ConversationID), response.JobID, "部分回答")
	if row := waitRunStatus(t, storage, response.JobID, store.RunStatusFailed); row.FinishedAt == nil {
		t.Fatalf("run row = %+v", row)
	}
}

func TestPartialAnswerSavedWhenCanceledDuringStreaming(t *testing.T) {
	storage := restartStore(t)
	runner := durableRunner(t, storage, streamThenWait("正在", "分析"), tools.Dependencies{}, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	waitDeltaText(t, stream, "正在分析")
	if err := runner.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if events[len(events)-1].Type != "canceled" {
		t.Fatalf("terminal = %+v", events[len(events)-1])
	}
	requirePartialLast(t, storedRows(t, storage, response.ConversationID), response.JobID, "正在分析")
	waitRunStatus(t, storage, response.JobID, store.RunStatusCanceled)
}

func TestPartialAnswerSavedWhenShutdownStopsStreaming(t *testing.T) {
	storage := restartStore(t)
	runner := durableRunner(t, storage, streamThenWait("先检查", "磁盘"), tools.Dependencies{}, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	waitDeltaText(t, stream, "先检查磁盘")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runner.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, stream)
	row := waitRunStatus(t, storage, response.JobID, store.RunStatusInterrupted)
	if row.FinishedAt == nil {
		t.Fatalf("interrupted run row = %+v", row)
	}
	requirePartialLast(t, storedRows(t, storage, response.ConversationID), response.JobID, "先检查磁盘")
}

func TestPartialAnswerKeepsTextOfAFailedToolCallTurn(t *testing.T) {
	// The turn failed before its tool calls were saved, so its text has no
	// interim row and is kept as the partial answer.
	index := 0
	call := schema.AssistantMessage("", []schema.ToolCall{{Index: &index, ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "todo_write", Arguments: `{"todos":`}}})
	chat := &fakeModel{stream: streamThenFail(schema.AssistantMessage("让我查一下", nil), call)}
	storage := restartStore(t)
	runner := durableRunner(t, storage, chat, tools.Dependencies{}, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	waitClosed(t, stream)
	rows := storedRows(t, storage, response.ConversationID)
	requirePartialLast(t, rows, response.JobID, "让我查一下")
	for _, row := range rows {
		if row.Interim || row.Type == historyTypeToolCall {
			t.Fatalf("unfinished tool-call turn was saved: %+v", rows)
		}
	}
}

func TestPartialAnswerFollowsTheRunsToolRows(t *testing.T) {
	var turn atomic.Int64
	steps := []func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error){
		func(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			message := schema.AssistantMessage("先列待办。", []schema.ToolCall{namedToolCall("call-1", "todo_write", `{"todos":[]}`)})
			return schema.StreamReaderFromArray([]*schema.Message{message}), nil
		},
		streamThenFail(schema.AssistantMessage("结论是", nil)),
	}
	chat := &fakeModel{stream: func(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		step := steps[min(int(turn.Add(1))-1, len(steps)-1)]
		return step(ctx, messages, options...)
	}}
	storage := restartStore(t)
	runner := durableRunner(t, storage, chat, tools.Dependencies{}, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	waitClosed(t, stream)
	rows := storedRows(t, storage, response.ConversationID)
	var shape []string
	for _, row := range rows {
		switch {
		case row.Type != "":
			shape = append(shape, row.Type)
		case row.Interim:
			shape = append(shape, "interim:"+row.Content)
		case row.Partial:
			shape = append(shape, "partial:"+row.Content)
		default:
			shape = append(shape, row.Role+":"+row.Content)
		}
	}
	want := []string{"user:go", "interim:先列待办。", historyTypeToolCall, historyTypeToolResult, "partial:结论是"}
	if strings.Join(shape, "|") != strings.Join(want, "|") {
		t.Fatalf("rows = %v, want %v", shape, want)
	}
}

func TestStepLimitDoesNotRepeatSavedInterimText(t *testing.T) {
	// The step limit stops the run before the next model call, so the last
	// turn's text is already saved as an interim row and nothing is added.
	chat := sequenceModel(schema.AssistantMessage("先列待办。", []schema.ToolCall{namedToolCall("loop", "todo_write", `{"todos":[]}`)}))
	runner, storage := testRunner(t, chat, tools.Dependencies{}, 1)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	events := waitClosed(t, stream)
	if last := events[len(events)-1]; last.Type != "error" || !last.MaxIterations {
		t.Fatalf("terminal = %+v", last)
	}
	rows := storedRows(t, storage, response.ConversationID)
	if partials := partialRows(rows); len(partials) != 0 {
		t.Fatalf("step limit saved a partial answer: %+v", rows)
	}
	interim := 0
	for _, row := range rows {
		if row.Interim && row.Content == "先列待办。" {
			interim++
		}
	}
	if interim != 1 {
		t.Fatalf("interim rows = %d in %+v", interim, rows)
	}
}

func TestStepLimitSavesTextStreamedInTheStoppedTurn(t *testing.T) {
	// A step limit reached while a turn's text is still unsaved keeps that
	// text like any other stop.
	runner, storage, stream, current := registeredJob(t, "job-limit")
	current.recordStreamed("还差一步")
	runner.complete(current, "", 1, usage.Usage{}, &maxIterationsError{limit: 1})
	events := waitClosed(t, stream)
	if last := events[len(events)-1]; last.Type != "error" || !last.MaxIterations {
		t.Fatalf("terminal = %+v", last)
	}
	requirePartialLast(t, storedRows(t, storage, current.args.ConversationID), "job-limit", "还差一步")
	if row, err := storage.RunGet(context.Background(), current.id); err != nil || row.FinishedAt == nil {
		t.Fatalf("run row = %+v err=%v", row, err)
	}
}

// registeredJob builds a run registered with the runner as Start leaves it,
// so a test can end it through complete.
func registeredJob(t *testing.T, id string) (*Runner, *store.Store, *SliceStream, *job) {
	t.Helper()
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	runner := NewRunner(Config{Store: storage, Runs: storage})
	t.Cleanup(func() { _ = runner.Close() })
	conversation, err := storage.ConvCreate(context.Background(), "partial", nil)
	if err != nil {
		t.Fatal(err)
	}
	stream := &SliceStream{}
	current := &job{
		id: id, args: ChatArgs{ConversationID: conversation.ID}, ctx: context.Background(), cancel: func() {},
		deliveryCtx: context.Background(), stream: stream, steer: steer.NewQueue(1),
	}
	if _, err := runner.hitl.RegisterRun(context.Background(), current.id, current.id); err != nil {
		t.Fatal(err)
	}
	if err := storage.RunInsert(context.Background(), store.RunRow{ID: current.id, ConversationID: conversation.ID, Status: store.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.jobs[current.id] = current
	runner.wg.Add(1)
	runner.mu.Unlock()
	return runner, storage, stream, current
}

func TestNoPartialAnswerWithoutStreamedText(t *testing.T) {
	for _, test := range []struct {
		name string
		chat *fakeModel
	}{
		{"nothing streamed", &fakeModel{stream: streamThenFail()}},
		{"whitespace only", &fakeModel{stream: streamThenFail(schema.AssistantMessage("  \n\t", nil))}},
		{"reasoning only", &fakeModel{stream: streamThenFail(&schema.Message{Role: schema.Assistant, ReasoningContent: "思考"})}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, storage := testRunner(t, test.chat, tools.Dependencies{}, 0)
			stream := &SliceStream{}
			response := startTestJob(t, runner, stream, "go")
			events := waitClosed(t, stream)
			if _, failed := terminalCounts(events); failed != 1 {
				t.Fatalf("events = %+v", events)
			}
			rows := storedRows(t, storage, response.ConversationID)
			if len(rows) != 1 || rows[0].Role != "user" {
				t.Fatalf("rows = %+v, want only the user message", rows)
			}
		})
	}
}

func TestSuccessfulRunSavesOnlyTheFinalAnswer(t *testing.T) {
	chat := &fakeModel{steps: []fakeStep{{chunks: []*schema.Message{schema.AssistantMessage("磁盘", nil), schema.AssistantMessage("充足", nil)}}}}
	runner, storage := testRunner(t, chat, tools.Dependencies{}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	events := waitClosed(t, stream)
	if done, _ := terminalCounts(events); done != 1 {
		t.Fatalf("events = %+v", events)
	}
	rows := storedRows(t, storage, response.ConversationID)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if final := rows[1]; final.Role != "assistant" || final.Content != "磁盘充足" || final.Partial || final.Interim {
		t.Fatalf("final row = %+v", final)
	}
}

func TestCompleteAfterFinalAnswerSavesNoPartial(t *testing.T) {
	// A cancel that lands after the final answer was saved still ends the run
	// as canceled; the saved answer must not be repeated as a partial.
	runner, storage, stream, current := registeredJob(t, "job-late")
	current.recordStreamed("答案")
	if err := runner.persistFinalAnswer(current, "答案", usage.Usage{}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.hitl.Cancel(current.id); err != nil {
		t.Fatal(err)
	}
	runner.complete(current, "答案", 1, usage.Usage{}, nil)
	events := waitClosed(t, stream)
	if last := events[len(events)-1]; last.Type != "canceled" {
		t.Fatalf("terminal = %+v", last)
	}
	rows := storedRows(t, storage, current.args.ConversationID)
	if len(rows) != 1 || rows[0].Partial || rows[0].Content != "答案" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRebuildHistoryShowsPartialAnswerAsInterrupted(t *testing.T) {
	rows := []store.MessageRow{
		{ID: "1", ContentJSON: `{"role":"user","content":"旧问题","jobId":"job-0"}`},
		{ID: "2", ContentJSON: `{"role":"assistant","content":"旧回答","jobId":"job-0","partial":true}`},
		{ID: "3", ContentJSON: `{"role":"user","content":"查看磁盘","jobId":"job-1"}`},
		{ID: "4", ContentJSON: `{"role":"assistant","content":"先看分区。","jobId":"job-1","interim":true}`},
		{ID: "5", ContentJSON: `{"type":"toolCall","jobId":"job-1","id":"call-1","name":"exec_commands","args":{}}`},
		{ID: "6", ContentJSON: `{"type":"toolResult","jobId":"job-1","id":"call-1","tool":"exec_commands","ok":true,"text":"sda1 90%"}`},
		{ID: "7", ContentJSON: `{"role":"user","content":"顺便看内存","jobId":"job-1","steered":true}`},
		{ID: "8", ContentJSON: `{"role":"assistant","content":"磁盘快满了，","jobId":"job-1","partial":true}`},
		{ID: "9", ContentJSON: `{"role":"assistant","content":"  \n","jobId":"job-1","partial":true}`},
		{ID: "10", ContentJSON: `{"role":"user","content":"继续","jobId":"job-2"}`},
		{ID: "11", ContentJSON: `{"type":"summary","summary":"用户在排查磁盘","auto":true,"beforeJobId":"job-1"}`},
		{ID: "12", ContentJSON: `{"role":"assistant","content":"当前任务的部分","jobId":"job-2","partial":true}`},
	}
	messages := historyMessages(rows, "job-2")
	var got []string
	for _, message := range messages {
		entry := string(message.Role) + ":" + message.Content
		for _, call := range message.ToolCalls {
			entry += "+call:" + call.ID
		}
		if message.Role == schema.Tool {
			entry = "tool:" + message.ToolCallID
		}
		got = append(got, entry)
	}
	want := []string{
		"user:[History Summary]\n用户在排查磁盘",
		"user:查看磁盘",
		"assistant:先看分区。+call:call-1",
		"tool:call-1",
		"user:顺便看内存",
		"assistant:磁盘快满了，\n\n[answer interrupted]",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("history = %q\nwant %q", got, want)
	}

	// Without the summary the older partial answer is kept and marked too.
	messages = historyMessages(rows[:9], "")
	if messages[1].Role != schema.Assistant || messages[1].Content != "旧回答\n\n[answer interrupted]" {
		t.Fatalf("older partial = %+v", messages[1])
	}
	for _, message := range messages {
		if message.Role == schema.Assistant && message.Content == "" && len(message.ToolCalls) == 0 {
			t.Fatalf("empty assistant message in %+v", messages)
		}
	}
	var stored storedRow
	if err := json.Unmarshal([]byte(rows[7].ContentJSON), &stored); err != nil || stored.Content != "磁盘快满了，" {
		t.Fatalf("stored partial changed: %+v err=%v", stored, err)
	}
}

func TestNextRunSeesThePartialAnswer(t *testing.T) {
	var calls atomic.Int64
	seen := make(chan []*schema.Message, 4)
	chat := &fakeModel{stream: func(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		if calls.Add(1) == 1 {
			return streamThenFail(schema.AssistantMessage("答到一半", nil))(ctx, messages, options...)
		}
		seen <- messages
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("好的", nil)}), nil
	}}
	runner, _ := testRunner(t, chat, tools.Dependencies{}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "问题")
	waitClosed(t, stream)
	next := &SliceStream{}
	if _, err := runner.Start(context.Background(), ChatArgs{ConversationID: response.ConversationID, Message: "问题", Scope: tools.Scope{SessionID: "session"}}, StaticStream(next)); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, next)
	if len(seen) != 1 {
		t.Fatalf("model inputs = %d", len(seen))
	}
	input := <-seen
	found := false
	for index, message := range input {
		if message.Role == schema.Assistant && message.Content == "答到一半\n\n[answer interrupted]" {
			found = index > 0 && input[index-1].Role == schema.User && input[index-1].Content == "问题"
		}
	}
	if !found {
		t.Fatalf("partial answer missing from the next run's input: %+v", input)
	}
}

func TestEditResendRemovesTheSupersededRunsPartialAnswer(t *testing.T) {
	// The partial answer of a run replaced by an edit goes with the run's
	// other rows, so the resent message does not see the old answer.
	storage := restartStore(t)
	runner := durableRunner(t, storage, streamThenWait("旧回答"), tools.Dependencies{}, nil)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "first")
	waitDeltaText(t, stream, "旧回答")
	target := userMessageIDWithJob(t, storage, response.ConversationID, response.JobID)
	if err := runner.EditResend(context.Background(), response.ConversationID, target); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, stream)
	if row, err := storage.RunGet(context.Background(), response.JobID); err != nil || row.Status != RunStatusSuperseded {
		t.Fatalf("superseded run = %+v err=%v", row, err)
	}
	rows := storedRows(t, storage, response.ConversationID)
	for _, row := range rows {
		if row.Role == "assistant" {
			t.Fatalf("superseded run left an assistant row: %+v", rows)
		}
	}
}

// TestCancelDuringStreamingSavesExactlyTheDeliveredText cancels at an
// arbitrary point of a continuous stream: the saved partial is exactly the
// text the client received, saved once.
func TestCancelDuringStreamingSavesExactlyTheDeliveredText(t *testing.T) {
	chat := &fakeModel{stream: func(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		reader, writer := schema.Pipe[*schema.Message](0)
		go func() {
			defer writer.Close()
			for {
				if closed := writer.Send(schema.AssistantMessage("字", nil), nil); closed {
					return
				}
				select {
				case <-ctx.Done():
					writer.Send(nil, ctx.Err())
					return
				default:
				}
			}
		}()
		return reader, nil
	}}
	runner, storage := testRunner(t, chat, tools.Dependencies{}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	_ = waitEvent(t, stream, "delta")
	if err := runner.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	delivered := deltaText(events)
	if delivered == "" {
		t.Fatal("no streamed text delivered")
	}
	requirePartialLast(t, storedRows(t, storage, response.ConversationID), response.JobID, delivered)
}

func waitDeltaText(t *testing.T, stream *SliceStream, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, _ := stream.Snapshot()
		if deltaText(events) == text {
			return
		}
		time.Sleep(time.Millisecond)
	}
	events, _ := stream.Snapshot()
	t.Fatalf("streamed text = %q, want %q", deltaText(events), text)
}
