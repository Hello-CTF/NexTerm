package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func historyTestRow(conversationID, contentJSON string) store.MessageRow {
	return store.MessageRow{ID: "row", ConversationID: conversationID, ContentJSON: contentJSON}
}

func TestRebuildHistoryStructuredRows(t *testing.T) {
	rows := []store.MessageRow{
		historyTestRow("c", `{"role":"user","content":"question"}`),
		historyTestRow("c", `{"type":"toolCall","jobId":"j1","id":"call-1","name":"exec_commands","args":{"commands":["ls"]}}`),
		historyTestRow("c", `{"type":"fileChange","jobId":"j1","id":"call-1","path":"/etc/nginx.conf","before":"a","after":"b"}`),
		historyTestRow("c", `{"type":"toolResult","jobId":"j1","id":"call-1","tool":"exec_commands","ok":true,"text":"total 0","summary":"total 0","truncated":false,"exitCode":0}`),
		historyTestRow("c", `{"type":"planSubmitted","jobId":"j1","plan":"step 1"}`),
		historyTestRow("c", `{"role":"assistant","content":"answer"}`),
	}
	messages := historyMessages(rows, "other")
	if len(messages) != 4 {
		t.Fatalf("messages = %d, want user + assistant(toolCall) + tool + assistant: %s", len(messages), describeMessages(messages))
	}
	if messages[0].Role != schema.User || messages[0].Content != "question" {
		t.Fatalf("messages[0] = %+v", messages[0])
	}
	carrier := messages[1]
	if carrier.Role != schema.Assistant || len(carrier.ToolCalls) != 1 {
		t.Fatalf("messages[1] = %+v", carrier)
	}
	call := carrier.ToolCalls[0]
	if call.ID != "call-1" || call.Function.Name != "exec_commands" || call.Function.Arguments != `{"commands":["ls"]}` {
		t.Fatalf("rebuilt tool call = %+v", call)
	}
	result := messages[2]
	if result.Role != schema.Tool || result.ToolCallID != "call-1" {
		t.Fatalf("messages[2] = %+v", result)
	}
	var envelope tools.Output
	if err := json.Unmarshal([]byte(result.Content), &envelope); err != nil {
		t.Fatalf("tool message content is not a tools.Output envelope: %v", err)
	}
	if !envelope.OK || envelope.Text != "total 0" || envelope.Truncated {
		t.Fatalf("envelope = %+v", envelope)
	}
	if messages[3].Role != schema.Assistant || messages[3].Content != "answer" || len(messages[3].ToolCalls) != 0 {
		t.Fatalf("messages[3] = %+v", messages[3])
	}
}

func TestRebuildHistoryGroupsConsecutiveCalls(t *testing.T) {
	rows := []store.MessageRow{
		historyTestRow("c", `{"type":"toolCall","jobId":"j1","id":"call-1","name":"a","args":{}}`),
		historyTestRow("c", `{"type":"toolCall","jobId":"j1","id":"call-2","name":"b","args":{}}`),
		historyTestRow("c", `{"type":"toolResult","jobId":"j1","id":"call-1","ok":true,"text":"one"}`),
		historyTestRow("c", `{"type":"toolResult","jobId":"j1","id":"call-2","ok":false,"text":"two","exitCode":1}`),
	}
	messages := historyMessages(rows, "other")
	if len(messages) != 3 {
		t.Fatalf("messages = %d: %s", len(messages), describeMessages(messages))
	}
	if len(messages[0].ToolCalls) != 2 || messages[0].ToolCalls[0].ID != "call-1" || messages[0].ToolCalls[1].ID != "call-2" {
		t.Fatalf("grouped calls = %+v", messages[0].ToolCalls)
	}
	if messages[1].ToolCallID != "call-1" || messages[2].ToolCallID != "call-2" {
		t.Fatalf("tool order = %s, %s", messages[1].ToolCallID, messages[2].ToolCallID)
	}
}

func TestRebuildHistoryKeepsToolPairAcrossSteeredMessage(t *testing.T) {
	rows := []store.MessageRow{
		historyTestRow("c", `{"role":"user","content":"go","jobId":"j1"}`),
		historyTestRow("c", `{"type":"toolCall","jobId":"j1","id":"probe","name":"docker_exec","args":{"cmd":"ls"}}`),
		historyTestRow("c", `{"role":"user","content":"steered","steered":true,"jobId":"j1"}`),
		historyTestRow("c", `{"type":"toolResult","jobId":"j1","id":"probe","tool":"docker_exec","ok":true,"text":"exec done"}`),
		historyTestRow("c", `{"role":"assistant","content":"done"}`),
	}
	messages := historyMessages(rows, "other")
	if len(messages) != 5 {
		t.Fatalf("messages = %d: %s", len(messages), describeMessages(messages))
	}
	if messages[1].Role != schema.Assistant || len(messages[1].ToolCalls) != 1 || messages[1].ToolCalls[0].ID != "probe" {
		t.Fatalf("messages[1] = %+v", messages[1])
	}
	if messages[2].Role != schema.Tool || messages[2].ToolCallID != "probe" {
		t.Fatalf("messages[2] = %+v", messages[2])
	}
	if messages[3].Role != schema.User || messages[3].Content != "steered" {
		t.Fatalf("messages[3] = %+v", messages[3])
	}
	if messages[4].Role != schema.Assistant || messages[4].Content != "done" {
		t.Fatalf("messages[4] = %+v", messages[4])
	}
}

func TestRebuildHistorySkipsCurrentJobRows(t *testing.T) {
	rows := []store.MessageRow{
		historyTestRow("c", `{"role":"user","content":"current","jobId":"current"}`),
		historyTestRow("c", `{"type":"toolCall","jobId":"current","id":"call-1","name":"a","args":{}}`),
		historyTestRow("c", `{"role":"user","content":"earlier","jobId":"older"}`),
	}
	messages := historyMessages(rows, "current")
	if len(messages) != 1 || messages[0].Content != "earlier" {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestRepairHistoryOrphanResultsAndUnansweredCalls(t *testing.T) {
	messages := []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{namedToolCall("a", "x", `{}`), namedToolCall("b", "y", `{}`)}),
		schema.ToolMessage(`{"ok":true}`, "b"),
		schema.ToolMessage(`{"ok":true}`, "orphan"),
		schema.AssistantMessage("", []schema.ToolCall{namedToolCall("c", "z", `{}`)}),
		schema.ToolMessage(`{"ok":true}`, "a"),
	}
	repaired, orphanResults, unansweredCalls := repairHistory(messages)
	if orphanResults != 2 || unansweredCalls != 2 {
		t.Fatalf("repair counts orphan=%d unanswered=%d", orphanResults, unansweredCalls)
	}
	if len(repaired) != 3 {
		t.Fatalf("repaired = %d messages: %s", len(repaired), describeMessages(repaired))
	}
	if len(repaired[0].ToolCalls) != 1 || repaired[0].ToolCalls[0].ID != "b" {
		t.Fatalf("kept calls = %+v", repaired[0].ToolCalls)
	}
	if repaired[1].Role != schema.Tool || repaired[1].ToolCallID != "b" {
		t.Fatalf("kept result = %+v", repaired[1])
	}
	if repaired[2].Role != schema.Assistant || len(repaired[2].ToolCalls) != 0 {
		t.Fatalf("unanswered assistant = %+v", repaired[2])
	}
}

func TestRepairHistoryDropsDuplicateResults(t *testing.T) {
	messages := []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{namedToolCall("a", "x", `{}`)}),
		schema.ToolMessage(`{"ok":true,"text":"first"}`, "a"),
		schema.ToolMessage(`{"ok":false,"text":"second"}`, "a"),
	}
	repaired, orphanResults, unansweredCalls := repairHistory(messages)
	if orphanResults != 1 || unansweredCalls != 0 {
		t.Fatalf("repair counts orphan=%d unanswered=%d", orphanResults, unansweredCalls)
	}
	if len(repaired) != 2 || !strings.Contains(repaired[1].Content, "first") {
		t.Fatalf("duplicate resolution kept wrong result: %s", describeMessages(repaired))
	}
}

func TestRebuildHistoryLegacyRows(t *testing.T) {
	rows := []store.MessageRow{
		historyTestRow("c", `{"role":"user","content":"one","steered":false}`),
		historyTestRow("c", `{"role":"assistant","content":"two"}`),
		historyTestRow("c", `{"role":"user","content":""}`),
		historyTestRow("c", `{"role":"system","content":"ignored"}`),
		historyTestRow("c", `not-json`),
		historyTestRow("c", `{"role":"user","content":"current","jobId":"current"}`),
	}
	messages := historyMessages(rows, "current")
	if len(messages) != 2 || messages[0].Content != "one" || messages[1].Content != "two" {
		t.Fatalf("legacy rebuild = %+v", messages)
	}
	if messages[0].Role != schema.User || messages[1].Role != schema.Assistant {
		t.Fatalf("legacy roles = %s, %s", messages[0].Role, messages[1].Role)
	}
}

func TestPersistToolRowsTruncatesAndStripsRuntimeFields(t *testing.T) {
	storage, err := store.OpenInMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	conversation, err := storage.ConvCreate(context.Background(), "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{store: storage}
	current := &job{id: "job-1", args: ChatArgs{ConversationID: conversation.ID, PlanMode: true}}
	if err := runner.persistToolCallRows(context.Background(), conversation.ID, current.id, []schema.ToolCall{namedToolCall("call-1", "exec_commands", `{"commands":["ls"]}`)}); err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("输出", 40<<10)
	message := &schema.Message{Role: schema.Tool, ToolCallID: "call-1", ToolName: "exec_commands"}
	result := tools.Output{OK: true, Text: huge, Change: &tools.Change{ID: "call-1", Path: "/a", Before: "x", After: "y"}, Plan: "plan body"}
	text, cut := prefixBytes(result.Text, persistedToolResultLimit)
	result.Truncated = result.Truncated || cut
	if err := runner.persistToolResultRows(context.Background(), current, message, result, text); err != nil {
		t.Fatal(err)
	}

	rows, err := storage.MsgList(context.Background(), conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("persisted rows = %d, want toolCall + fileChange + toolResult + planSubmitted", len(rows))
	}
	if rows[0].Role != historyTypeToolCall || rows[1].Role != historyTypeFileChange || rows[2].Role != historyTypeToolResult || rows[3].Role != historyTypePlanSubmitted {
		t.Fatalf("row roles = %s, %s, %s, %s", rows[0].Role, rows[1].Role, rows[2].Role, rows[3].Role)
	}
	var callRow historyRow
	if err := json.Unmarshal([]byte(rows[0].ContentJSON), &callRow); err != nil {
		t.Fatal(err)
	}
	if callRow.ID != "call-1" || callRow.Name != "exec_commands" || callRow.JobID != "job-1" {
		t.Fatalf("toolCall row = %+v", callRow)
	}
	var resultRow historyRow
	if err := json.Unmarshal([]byte(rows[2].ContentJSON), &resultRow); err != nil {
		t.Fatal(err)
	}
	if !resultRow.Truncated || len(resultRow.Text) > persistedToolResultLimit || len(resultRow.Text) < persistedToolResultLimit-8 || !utf8.ValidString(resultRow.Text) {
		t.Fatalf("toolResult truncation: len=%d truncated=%v", len(resultRow.Text), resultRow.Truncated)
	}
	if resultRow.Summary == "" || len(resultRow.Summary) > 402 {
		t.Fatalf("summary = %q", resultRow.Summary)
	}
	for _, row := range rows {
		for _, forbidden := range []string{"reasoningContent", "responseMeta", "extra"} {
			if strings.Contains(strings.ToLower(row.ContentJSON), strings.ToLower(forbidden)) {
				t.Fatalf("row %s leaks %s: %s", row.Role, forbidden, row.ContentJSON)
			}
		}
	}
	if rowsCount(t, storage, conversation.ID, historyTypePlanSubmitted) != 1 {
		t.Fatal("planSubmitted row missing")
	}
}

func rowsCount(t *testing.T, storage *store.Store, conversationID, role string) int {
	t.Helper()
	rows, err := storage.MsgList(context.Background(), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range rows {
		if row.Role == role {
			count++
		}
	}
	return count
}

func TestStructuredHistoryReloadsIntoModelInput(t *testing.T) {
	var calls atomic.Int64
	var captured []*schema.Message
	chat := &fakeModel{stream: func(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		switch calls.Add(1) {
		case 1:
			return schema.StreamReaderFromArray([]*schema.Message{toolCallMessage(namedToolCall("call-1", "todo_write", `{"todos":[]}`))}), nil
		case 2:
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
		default:
			captured = messages
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
		}
	}}
	runner, _ := testRunner(t, chat, tools.Dependencies{}, 0)
	first := &SliceStream{}
	response := startTestJob(t, runner, first, "go")
	waitClosed(t, first)
	second := &SliceStream{}
	if _, err := runner.Start(context.Background(), ChatArgs{ConversationID: response.ConversationID, Message: "next"}, StaticStream(second)); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, second)

	if len(captured) != 6 {
		t.Fatalf("model input = %d messages: %s", len(captured), describeMessages(captured))
	}
	if captured[0].Role != schema.System {
		t.Fatalf("captured[0] = %+v", captured[0])
	}
	if captured[1].Role != schema.User || captured[1].Content != "go" {
		t.Fatalf("captured[1] = %+v", captured[1])
	}
	if captured[2].Role != schema.Assistant || len(captured[2].ToolCalls) != 1 || captured[2].ToolCalls[0].ID != "call-1" {
		t.Fatalf("captured[2] = %+v", captured[2])
	}
	if captured[3].Role != schema.Tool || captured[3].ToolCallID != "call-1" {
		t.Fatalf("captured[3] = %+v", captured[3])
	}
	if captured[4].Role != schema.Assistant || captured[4].Content != "done" {
		t.Fatalf("captured[4] = %+v", captured[4])
	}
	if captured[5].Role != schema.User || captured[5].Content != "next" {
		t.Fatalf("captured[5] = %+v", captured[5])
	}
}
