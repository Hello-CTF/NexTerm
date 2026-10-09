package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/schema"
)

type recordingReminderSink struct {
	calls        atomic.Int64
	conversation string
	reminders    []tools.Reminder
}

func (r *recordingReminderSink) ScheduleReminder(_ context.Context, conversationID string, reminder tools.Reminder) (tools.ScheduledReminder, error) {
	r.calls.Add(1)
	r.conversation = conversationID
	r.reminders = append(r.reminders, reminder)
	return tools.ScheduledReminder{ID: "reminder-1", At: reminder.At}, nil
}

func TestSendReminderConfirmThenSchedule(t *testing.T) {
	sink := &recordingReminderSink{}
	chat := sequenceModel(
		toolCallMessage(namedToolCall("remind", "send_reminder", `{"message":"检查备份","delay_minutes":10}`)),
		schema.AssistantMessage("已创建提醒", nil),
	)
	runner, storage := testRunner(t, chat, tools.Dependencies{Reminders: sink}, 0)
	conversation, err := storage.ConvCreate(context.Background(), "提醒测试", nil)
	if err != nil {
		t.Fatal(err)
	}
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "帮我定个提醒", ConversationID: conversation.ID}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")
	if confirmation.Tool != "send_reminder" || !strings.Contains(confirmation.Rendered, "检查备份") {
		t.Fatalf("confirmation = %+v", confirmation)
	}
	if sink.calls.Load() != 0 {
		t.Fatal("reminder scheduled before user confirmation")
	}
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if sink.calls.Load() != 1 {
		t.Fatalf("scheduler calls = %d", sink.calls.Load())
	}
	if sink.conversation != conversation.ID {
		t.Fatalf("reminder conversation = %q", sink.conversation)
	}
	if len(sink.reminders) != 1 || sink.reminders[0].Message != "检查备份" {
		t.Fatalf("reminders = %+v", sink.reminders)
	}
	var result Event
	for _, event := range events {
		if event.Type == "toolResult" && event.ID == "remind" {
			result = event
		}
	}
	if !result.OK || !strings.Contains(result.Text, "reminder-1") {
		t.Fatalf("toolResult = %+v", result)
	}
	if done, failed := terminalCounts(events); done != 1 || failed != 0 {
		t.Fatalf("terminal events done=%d error=%d", done, failed)
	}
}

func TestSendReminderDenyKeepsSchedulerUnused(t *testing.T) {
	sink := &recordingReminderSink{}
	chat := sequenceModel(
		toolCallMessage(namedToolCall("remind", "send_reminder", `{"message":"检查备份","delay_minutes":10}`)),
		schema.AssistantMessage("好的", nil),
	)
	runner, storage := testRunner(t, chat, tools.Dependencies{Reminders: sink}, 0)
	conversation, err := storage.ConvCreate(context.Background(), "提醒测试", nil)
	if err != nil {
		t.Fatal(err)
	}
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "帮我定个提醒", ConversationID: conversation.ID}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "deny"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if sink.calls.Load() != 0 {
		t.Fatal("denied reminder reached the scheduler")
	}
	var result Event
	for _, event := range events {
		if event.Type == "toolResult" && event.ID == "remind" {
			result = event
		}
	}
	if result.OK || !strings.Contains(result.Text, "拒绝") {
		t.Fatalf("toolResult = %+v", result)
	}
}

func TestSendReminderRejectsInvalidTimeWithoutScheduling(t *testing.T) {
	sink := &recordingReminderSink{}
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	chat := sequenceModel(
		toolCallMessage(namedToolCall("remind", "send_reminder", `{"message":"过期提醒","at":"`+past+`"}`)),
		schema.AssistantMessage("无法创建", nil),
	)
	runner, _ := testRunner(t, chat, tools.Dependencies{Reminders: sink}, 0)
	runner.config.Permission = func(context.Context) (guard.Config, error) { return guard.Config{Mode: guard.Silent}, nil }
	stream := &SliceStream{}
	startTestJob(t, runner, stream, "帮我定个提醒")
	events := waitClosed(t, stream)
	if sink.calls.Load() != 0 {
		t.Fatal("invalid reminder reached the scheduler")
	}
	var result Event
	for _, event := range events {
		if event.Type == "toolResult" && event.ID == "remind" {
			result = event
		}
	}
	if result.OK || !strings.Contains(result.Text, "晚于当前时间") {
		t.Fatalf("toolResult = %+v", result)
	}
}
