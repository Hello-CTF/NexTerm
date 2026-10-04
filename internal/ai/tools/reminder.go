package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ReminderTool       = "send_reminder"
	reminderMessageMax = 500
	reminderDelayMax   = 30 * 24 * time.Hour
)

type Reminder struct {
	Message string
	At      time.Time
}

type ScheduledReminder struct {
	ID string
	At time.Time
}

type ReminderScheduler interface {
	ScheduleReminder(ctx context.Context, conversationID string, reminder Reminder) (ScheduledReminder, error)
}

type SendReminderArgs struct {
	Message      string `json:"message" jsonschema:"required"`
	DelayMinutes int64  `json:"delay_minutes,omitempty"`
	At           string `json:"at,omitempty"`
}

func ValidateReminderArgs(now time.Time, args SendReminderArgs) (Reminder, error) {
	message := strings.TrimSpace(args.Message)
	if message == "" {
		return Reminder{}, invalid("提醒内容不能为空")
	}
	if !utf8.ValidString(message) || len([]rune(message)) > reminderMessageMax {
		return Reminder{}, invalid("提醒内容过长或不是有效 UTF-8（上限 %d 字符）", reminderMessageMax)
	}
	if args.DelayMinutes != 0 && args.At != "" {
		return Reminder{}, invalid("delay_minutes 与 at 只能二选一")
	}
	var at time.Time
	switch {
	case args.At != "":
		parsed, err := time.Parse(time.RFC3339, args.At)
		if err != nil {
			return Reminder{}, invalid("at 必须是 RFC3339 时间（如 2026-01-02T15:04:05+08:00）")
		}
		at = parsed
	case args.DelayMinutes != 0:
		if args.DelayMinutes < 0 {
			return Reminder{}, invalid("delay_minutes 不能为负数")
		}
		at = now.Add(time.Duration(args.DelayMinutes) * time.Minute)
	default:
		return Reminder{}, invalid("必须提供 delay_minutes 或 at 之一")
	}
	if !at.After(now) {
		return Reminder{}, invalid("提醒时间必须晚于当前时间")
	}
	if at.Sub(now) > reminderDelayMax {
		return Reminder{}, invalid("提醒时间最远只能设定在 30 天内")
	}
	return Reminder{Message: message, At: at}, nil
}

func (e *Execution) executeReminder(ctx context.Context, call Call) Output {
	var args SendReminderArgs
	if err := decode(call.Args, &args); err != nil {
		return Fail(err)
	}
	reminder, err := ValidateReminderArgs(e.Registry.deps.now(), args)
	if err != nil {
		return Fail(err)
	}
	if e.ConversationID == "" {
		return Fail(errors.New("当前运行缺少会话标识，无法投递提醒"))
	}
	scheduled, err := e.Registry.deps.Reminders.ScheduleReminder(ctx, e.ConversationID, reminder)
	if err != nil {
		return Fail(fmt.Errorf("创建提醒失败: %w", err))
	}
	if e.Registry.deps.Audit != nil {
		entry := AuditEntry{Kind: "exec", Payload: map[string]any{"tool": call.Name, "args": redactArguments(call.Args), "run_id": e.JobID, "call_id": call.ID}}
		_ = e.Registry.deps.Audit(context.WithoutCancel(ctx), entry)
	}
	return OK(fmt.Sprintf("提醒已创建（任务 %s），将于 %s 投递到当前会话；可在定时任务列表查看投递状态",
		scheduled.ID, scheduled.At.Format(time.RFC3339)))
}
