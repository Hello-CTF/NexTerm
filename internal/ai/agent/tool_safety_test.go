package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/schema"
)

func TestMalformedArgumentsAndUnknownToolDoNotEndRun(t *testing.T) {
	var actions atomic.Int64
	chat := sequenceModel(
		toolCallMessage(namedToolCall("bad", "docker_control", `{"container_id":"web","action":`)),
		toolCallMessage(namedToolCall("ghost", "no_such_tool", `{}`)),
		schema.AssistantMessage("recovered", nil),
	)
	runner, storage := testRunner(t, chat, tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { actions.Add(1); return nil }}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("terminal events done=%d error=%d events=%+v", done, failed, events)
	}
	results := map[string]Event{}
	for _, event := range events {
		if event.Type == "toolResult" {
			results[event.ID] = event
		}
	}
	if bad := results["bad"]; bad.OK || !strings.Contains(bad.Text, "不是有效的 JSON") {
		t.Fatalf("malformed result=%+v", bad)
	}
	if ghost := results["ghost"]; ghost.OK || !strings.Contains(ghost.Text, "no_such_tool") || !strings.Contains(ghost.Text, "docker_control") {
		t.Fatalf("unknown result=%+v all=%+v", ghost, events)
	}
	if actions.Load() != 0 {
		t.Fatalf("a rejected call executed %d actions", actions.Load())
	}
	rows, err := storage.MsgList(context.Background(), response.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	persisted := 0
	for _, row := range rows {
		if row.Role == historyTypeToolResult && strings.Contains(row.ContentJSON, `"ok":false`) {
			persisted++
		}
	}
	if persisted != 2 {
		t.Fatalf("persisted failed tool results=%d rows=%+v", persisted, rows)
	}
}

func TestConfirmationStillPausesWithToolSafety(t *testing.T) {
	var actions atomic.Int64
	chat := sequenceModel(
		toolCallMessage(namedToolCall("stop", "docker_control", `{"container_id":"web","action":"stop"}`)),
		schema.AssistantMessage("done", nil),
	)
	runner, _ := testRunner(t, chat, tools.Dependencies{DockerAct: func(context.Context, string, string, string) error { actions.Add(1); return nil }}, 0)
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	confirmation := waitEvent(t, stream, "confirmRequired")
	if actions.Load() != 0 {
		t.Fatal("action ran before confirmation")
	}
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if done, failed := terminalCounts(events); done != 1 || failed != 0 || actions.Load() != 1 {
		t.Fatalf("done=%d failed=%d actions=%d", done, failed, actions.Load())
	}
}

func TestRunCancelDuringToolStillEndsAsCanceled(t *testing.T) {
	started := make(chan struct{})
	chat := sequenceModel(
		toolCallMessage(namedToolCall("stop", "docker_control", `{"container_id":"web","action":"stop"}`)),
		schema.AssistantMessage("done", nil),
	)
	runner, _ := testRunner(t, chat, tools.Dependencies{DockerAct: func(ctx context.Context, _, _, _ string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}, 0)
	runner.config.Permission = func(context.Context) (guard.Config, error) { return guard.Config{Mode: guard.Silent}, nil }
	stream := &SliceStream{}
	response := startTestJob(t, runner, stream, "go")
	<-started
	if err := runner.Cancel(response.JobID); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	canceled := 0
	for _, event := range events {
		if event.Type == "canceled" {
			canceled++
		}
	}
	if done, failed := terminalCounts(events); canceled != 1 || done != 0 || failed != 1 {
		t.Fatalf("canceled=%d done=%d terminals=%d events=%+v", canceled, done, failed, events)
	}
}
