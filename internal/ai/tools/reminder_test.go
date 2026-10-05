package tools

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

type fakeReminderScheduler struct {
	scheduled []Reminder
	calls     int
	err       error
}

func (f *fakeReminderScheduler) ScheduleReminder(_ context.Context, conversationID string, reminder Reminder) (ScheduledReminder, error) {
	f.calls++
	if f.err != nil {
		return ScheduledReminder{}, f.err
	}
	f.scheduled = append(f.scheduled, reminder)
	return ScheduledReminder{ID: "job-1", At: reminder.At}, nil
}

func TestValidateReminderArgs(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 5, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name    string
		args    SendReminderArgs
		wantErr string
	}{
		{"empty message", SendReminderArgs{DelayMinutes: 5}, "提醒内容不能为空"},
		{"blank message", SendReminderArgs{Message: "   ", DelayMinutes: 5}, "提醒内容不能为空"},
		{"long message", SendReminderArgs{Message: strings.Repeat("x", reminderMessageMax+1), DelayMinutes: 5}, "提醒内容过长"},
		{"both time specs", SendReminderArgs{Message: "m", DelayMinutes: 5, At: now.Add(time.Hour).Format(time.RFC3339)}, "二选一"},
		{"no time spec", SendReminderArgs{Message: "m"}, "delay_minutes 或 at"},
		{"bad rfc3339", SendReminderArgs{Message: "m", At: "明天下午"}, "RFC3339"},
		{"past time", SendReminderArgs{Message: "m", At: now.Add(-time.Minute).Format(time.RFC3339)}, "晚于当前时间"},
		{"too far", SendReminderArgs{Message: "m", DelayMinutes: int64(31 * 24 * 60)}, "30 天"},
		{"negative delay", SendReminderArgs{Message: "m", DelayMinutes: -3}, "不能为负数"},
		{"valid delay", SendReminderArgs{Message: "m", DelayMinutes: 5}, ""},
		{"valid at", SendReminderArgs{Message: "m", At: now.Add(2 * time.Hour).Format(time.RFC3339)}, ""},
	} {
		reminder, err := ValidateReminderArgs(now, test.args)
		if test.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", test.name, err)
				continue
			}
			if !reminder.At.After(now) || reminder.Message == "" {
				t.Errorf("%s: reminder = %+v", test.name, reminder)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), test.wantErr) {
			t.Errorf("%s: err = %v, want %q", test.name, err, test.wantErr)
		}
	}
	reminder, err := ValidateReminderArgs(now, SendReminderArgs{Message: "  带空格  ", DelayMinutes: 5})
	if err != nil || reminder.Message != "带空格" {
		t.Fatalf("message not trimmed: %+v err=%v", reminder, err)
	}
}

func TestValidateReminderArgsRejectsOverflowingDelay(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 5, 12, 0, 0, 0, time.UTC)
	for _, delay := range []int64{math.MaxInt64, math.MaxInt64 / 2, int64(reminderDelayMax/time.Minute) + 1} {
		if _, err := ValidateReminderArgs(now, SendReminderArgs{Message: "m", DelayMinutes: delay}); err == nil {
			t.Errorf("delay_minutes=%d accepted", delay)
		} else if !strings.Contains(err.Error(), "30 天") {
			t.Errorf("delay_minutes=%d: err = %v", delay, err)
		}
	}
	boundary, err := ValidateReminderArgs(now, SendReminderArgs{Message: "m", DelayMinutes: int64(reminderDelayMax / time.Minute)})
	if err != nil {
		t.Fatalf("30-day boundary rejected: %v", err)
	}
	if !boundary.At.Equal(now.Add(reminderDelayMax)) {
		t.Fatalf("boundary at = %v", boundary.At)
	}
}

func TestReminderToolFollowsSchedulerAvailability(t *testing.T) {
	t.Parallel()
	names := func(execution *Execution) map[string]bool {
		einoTools, err := execution.Tools()
		if err != nil {
			t.Fatal(err)
		}
		result := make(map[string]bool, len(einoTools))
		for _, current := range einoTools {
			info, err := current.Info(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			result[info.Name] = true
		}
		return result
	}
	without := names(&Execution{JobID: "j", Registry: NewRegistry(Dependencies{})})
	if without[ReminderTool] {
		t.Fatal("send_reminder exposed without a scheduler")
	}
	with := names(&Execution{JobID: "j", Registry: NewRegistry(Dependencies{Reminders: &fakeReminderScheduler{}})})
	if !with[ReminderTool] {
		t.Fatal("send_reminder missing when the scheduler is configured")
	}
	plan := names(&Execution{JobID: "j", Registry: NewRegistry(Dependencies{Reminders: &fakeReminderScheduler{}}), PlanMode: true})
	if plan[ReminderTool] {
		t.Fatal("send_reminder exposed in plan mode")
	}
}

func TestExecuteReminderSchedulesAndReportsStatus(t *testing.T) {
	t.Parallel()
	sink := &fakeReminderScheduler{}
	audited := 0
	deps := Dependencies{Reminders: sink, Audit: func(context.Context, AuditEntry) error {
		audited++
		return nil
	}}
	execution := &Execution{JobID: "job-1", ConversationID: "conv-1", Registry: NewRegistry(deps)}
	output := execution.executeReminder(context.Background(), Call{ID: "c1", Name: ReminderTool, Args: json.RawMessage(`{"message":"检查备份","delay_minutes":10}`)})
	if !output.OK {
		t.Fatalf("output = %+v", output)
	}
	if sink.calls != 1 || len(sink.scheduled) != 1 {
		t.Fatalf("scheduler calls = %d", sink.calls)
	}
	if sink.scheduled[0].Message != "检查备份" {
		t.Fatalf("scheduled reminder = %+v", sink.scheduled[0])
	}
	if !strings.Contains(output.Text, "job-1") {
		t.Fatalf("delivery status missing the job id: %q", output.Text)
	}
	if audited != 1 {
		t.Fatalf("audit entries = %d", audited)
	}
}

func TestExecuteReminderFailsClosed(t *testing.T) {
	t.Parallel()
	sink := &fakeReminderScheduler{err: errors.New("存储不可用")}
	execution := &Execution{JobID: "job-1", ConversationID: "conv-1", Registry: NewRegistry(Dependencies{Reminders: sink})}
	output := execution.executeReminder(context.Background(), Call{ID: "c1", Name: ReminderTool, Args: json.RawMessage(`{"message":"m","delay_minutes":10}`)})
	if output.OK || !strings.Contains(output.Text, "创建提醒失败") {
		t.Fatalf("output = %+v", output)
	}
	noConversation := &Execution{JobID: "job-1", Registry: NewRegistry(Dependencies{Reminders: &fakeReminderScheduler{}})}
	output = noConversation.executeReminder(context.Background(), Call{ID: "c2", Name: ReminderTool, Args: json.RawMessage(`{"message":"m","delay_minutes":10}`)})
	if output.OK || !strings.Contains(output.Text, "会话标识") {
		t.Fatalf("output without conversation = %+v", output)
	}
	invalid := execution.executeReminder(context.Background(), Call{ID: "c3", Name: ReminderTool, Args: json.RawMessage(`{"message":"m"}`)})
	if invalid.OK {
		t.Fatalf("invalid args accepted: %+v", invalid)
	}
}
