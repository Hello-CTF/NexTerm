package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ScheduleJobTool        = "schedule_job"
	ListScheduledJobsTool  = "list_scheduled_jobs"
	CancelScheduledJobTool = "cancel_scheduled_job"

	scheduledJobPromptMax = 2000
	scheduledJobNameMax   = 80
	// ScheduledJobMinInterval is the shortest gap between two runs of a job the AI creates.
	ScheduledJobMinInterval = time.Minute
)

// ScheduledJobSpec is a recurring job the AI asks to create in its own conversation.
type ScheduledJobSpec struct {
	Name     string
	Prompt   string
	Schedule string
	Timezone string
}

type ScheduledJob struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	Prompt    string    `json:"prompt"`
	Schedule  string    `json:"schedule"`
	Timezone  string    `json:"timezone"`
	Enabled   bool      `json:"enabled"`
	Running   bool      `json:"running,omitempty"`
	NextRunAt time.Time `json:"nextRunAt,omitzero"`
	LastRunAt time.Time `json:"lastRunAt,omitzero"`
	LastError string    `json:"lastError,omitempty"`
}

// ScheduledJobService manages the recurring jobs bound to one conversation; the
// production implementation is the cron scheduler.
type ScheduledJobService interface {
	// ValidateSchedule reports whether the expression is a valid recurring
	// schedule in the timezone whose runs are at least ScheduledJobMinInterval apart.
	ValidateSchedule(ctx context.Context, schedule, timezone string) error
	CreateJob(ctx context.Context, conversationID string, spec ScheduledJobSpec) (ScheduledJob, error)
	ListJobs(ctx context.Context, conversationID string) ([]ScheduledJob, error)
	CancelJob(ctx context.Context, conversationID, jobID string) (ScheduledJob, error)
}

type ScheduleJobArgs struct {
	Prompt   string `json:"prompt" jsonschema:"required" jsonschema_description:"Instruction the AI runs in this conversation at every occurrence, written so it makes sense on its own (at most 2000 characters)"`
	Schedule string `json:"schedule" jsonschema:"required" jsonschema_description:"Recurring schedule: a 5-field cron expression (minute hour day-of-month month day-of-week, e.g. 0 9 * * 1-5), @hourly, @daily, @weekly, @monthly, @yearly, or @every <duration> of at least 1m (e.g. @every 30m). For a single future time use send_reminder instead"`
	Timezone string `json:"timezone,omitempty" jsonschema_description:"IANA timezone the cron expression is evaluated in, e.g. Asia/Shanghai (default UTC). Ask the user when their timezone matters and is unknown"`
	Name     string `json:"name,omitempty" jsonschema_description:"Short name shown in the job list (at most 80 characters); defaults to the start of the prompt"`
}

type CancelScheduledJobArgs struct {
	JobID string `json:"job_id" jsonschema:"required" jsonschema_description:"Job id returned by schedule_job or list_scheduled_jobs"`
}

func scheduledJobTool(name string) bool {
	return name == ScheduleJobTool || name == ListScheduledJobsTool || name == CancelScheduledJobTool
}

// ValidateScheduleJobArgs checks the arguments that do not need the scheduler;
// the schedule expression itself is checked by ScheduledJobService.ValidateSchedule.
func ValidateScheduleJobArgs(args ScheduleJobArgs) (ScheduledJobSpec, error) {
	prompt := strings.TrimSpace(args.Prompt)
	if prompt == "" {
		return ScheduledJobSpec{}, invalid("定时任务内容不能为空")
	}
	if !utf8.ValidString(prompt) || utf8.RuneCountInString(prompt) > scheduledJobPromptMax {
		return ScheduledJobSpec{}, invalid("定时任务内容过长或不是有效 UTF-8（上限 %d 字符）", scheduledJobPromptMax)
	}
	name := strings.TrimSpace(args.Name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > scheduledJobNameMax {
		return ScheduledJobSpec{}, invalid("定时任务名称过长或不是有效 UTF-8（上限 %d 字符）", scheduledJobNameMax)
	}
	schedule := strings.TrimSpace(args.Schedule)
	if schedule == "" {
		return ScheduledJobSpec{}, invalid("schedule 不能为空")
	}
	if strings.HasPrefix(strings.ToLower(schedule), "@at") {
		return ScheduledJobSpec{}, invalid("schedule_job 只创建循环执行的任务，一次性提醒请使用 send_reminder")
	}
	return ScheduledJobSpec{Name: name, Prompt: prompt, Schedule: schedule, Timezone: strings.TrimSpace(args.Timezone)}, nil
}

// precheckScheduledJob rejects a call that cannot succeed before the user is
// asked to confirm it.
func (e *Execution) precheckScheduledJob(ctx context.Context, call Call) error {
	if e.ConversationID == "" {
		return errors.New("当前运行缺少会话标识，无法管理定时任务")
	}
	switch call.Name {
	case ScheduleJobTool:
		var args ScheduleJobArgs
		if err := decode(call.Args, &args); err != nil {
			return err
		}
		spec, err := ValidateScheduleJobArgs(args)
		if err != nil {
			return err
		}
		return e.Registry.deps.ScheduledJobs.ValidateSchedule(ctx, spec.Schedule, spec.Timezone)
	case CancelScheduledJobTool:
		var args CancelScheduledJobArgs
		if err := decode(call.Args, &args); err != nil {
			return err
		}
		if strings.TrimSpace(args.JobID) == "" {
			return invalid("job_id 不能为空")
		}
	}
	return nil
}

func (e *Execution) executeScheduledJob(ctx context.Context, call Call) Output {
	if err := e.precheckScheduledJob(ctx, call); err != nil {
		return Fail(err)
	}
	jobs := e.Registry.deps.ScheduledJobs
	switch call.Name {
	case ScheduleJobTool:
		var args ScheduleJobArgs
		_ = decode(call.Args, &args)
		spec, _ := ValidateScheduleJobArgs(args)
		job, err := jobs.CreateJob(ctx, e.ConversationID, spec)
		if err != nil {
			return Fail(fmt.Errorf("创建定时任务失败: %w", err))
		}
		e.auditScheduledJob(ctx, call)
		return OK(fmt.Sprintf("定时任务已创建（任务 %s）：%s（%s），下次执行 %s；每次到点 AI 会在当前会话无人值守地执行，需要确认的操作会被拒绝；用户可在「后台任务」中查看、停用或删除",
			job.ID, job.Schedule, job.Timezone, job.NextRunAt.Format(time.RFC3339)))
	case ListScheduledJobsTool:
		listed, err := jobs.ListJobs(ctx, e.ConversationID)
		if err != nil {
			return Fail(fmt.Errorf("读取定时任务失败: %w", err))
		}
		if listed == nil {
			listed = []ScheduledJob{}
		}
		encoded, err := json.Marshal(map[string]any{"jobs": listed})
		if err != nil {
			return Fail(err)
		}
		return OK(string(encoded))
	case CancelScheduledJobTool:
		var args CancelScheduledJobArgs
		_ = decode(call.Args, &args)
		job, err := jobs.CancelJob(ctx, e.ConversationID, strings.TrimSpace(args.JobID))
		if err != nil {
			return Fail(fmt.Errorf("删除定时任务失败: %w", err))
		}
		e.auditScheduledJob(ctx, call)
		return OK(fmt.Sprintf("定时任务 %s 已删除，不会再执行", job.ID))
	default:
		return Fail(invalid("未知工具 %s", call.Name))
	}
}

func (e *Execution) auditScheduledJob(ctx context.Context, call Call) {
	if e.Registry.deps.Audit == nil {
		return
	}
	entry := AuditEntry{Kind: "exec", Payload: map[string]any{"tool": call.Name, "args": redactArguments(call.Args), "run_id": e.JobID, "call_id": call.ID}}
	_ = e.Registry.deps.Audit(context.WithoutCancel(ctx), entry)
}
