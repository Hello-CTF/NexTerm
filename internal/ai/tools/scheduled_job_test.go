package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
)

type fakeScheduledJobs struct {
	mu            sync.Mutex
	validateErr   error
	created       []ScheduledJobSpec
	conversations []string
	cancelled     []string
	jobs          []ScheduledJob
}

func (f *fakeScheduledJobs) ValidateSchedule(_ context.Context, schedule, _ string) error {
	if f.validateErr != nil {
		return f.validateErr
	}
	if schedule == "@every 10s" {
		return errors.New("定时任务的执行间隔不能短于 1 分钟")
	}
	return nil
}

func (f *fakeScheduledJobs) CreateJob(_ context.Context, conversationID string, spec ScheduledJobSpec) (ScheduledJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, spec)
	f.conversations = append(f.conversations, conversationID)
	return ScheduledJob{ID: "cron-1", Schedule: spec.Schedule, Timezone: "UTC", Enabled: true, NextRunAt: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)}, nil
}

func (f *fakeScheduledJobs) ListJobs(_ context.Context, conversationID string) ([]ScheduledJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conversations = append(f.conversations, conversationID)
	return f.jobs, nil
}

func (f *fakeScheduledJobs) CancelJob(_ context.Context, conversationID, jobID string) (ScheduledJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conversations = append(f.conversations, conversationID)
	f.cancelled = append(f.cancelled, jobID)
	return ScheduledJob{ID: jobID}, nil
}

func (f *fakeScheduledJobs) calls() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created), len(f.cancelled)
}

func TestValidateScheduleJobArgs(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		args    ScheduleJobArgs
		wantErr string
	}{
		{"empty prompt", ScheduleJobArgs{Schedule: "0 9 * * *"}, "定时任务内容不能为空"},
		{"blank prompt", ScheduleJobArgs{Prompt: "  ", Schedule: "0 9 * * *"}, "定时任务内容不能为空"},
		{"long prompt", ScheduleJobArgs{Prompt: strings.Repeat("x", scheduledJobPromptMax+1), Schedule: "0 9 * * *"}, "定时任务内容过长"},
		{"long name", ScheduleJobArgs{Prompt: "p", Name: strings.Repeat("名", scheduledJobNameMax+1), Schedule: "0 9 * * *"}, "定时任务名称过长"},
		{"missing schedule", ScheduleJobArgs{Prompt: "p"}, "schedule 不能为空"},
		{"one-shot schedule", ScheduleJobArgs{Prompt: "p", Schedule: "@at 2026-01-02T09:00:00Z"}, "send_reminder"},
		{"valid", ScheduleJobArgs{Prompt: " 检查磁盘 ", Name: " 巡检 ", Schedule: " 0 9 * * * ", Timezone: " Asia/Shanghai "}, ""},
	} {
		spec, err := ValidateScheduleJobArgs(test.args)
		if test.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", test.name, err)
				continue
			}
			if spec != (ScheduledJobSpec{Name: "巡检", Prompt: "检查磁盘", Schedule: "0 9 * * *", Timezone: "Asia/Shanghai"}) {
				t.Errorf("%s: spec = %+v", test.name, spec)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), test.wantErr) {
			t.Errorf("%s: err = %v, want %q", test.name, err, test.wantErr)
		}
	}
}

func TestScheduledJobToolsFollowServiceAndPlanMode(t *testing.T) {
	t.Parallel()
	names := func(execution *Execution) map[string]bool {
		built, err := execution.Tools()
		if err != nil {
			t.Fatal(err)
		}
		result := make(map[string]bool, len(built))
		for _, current := range built {
			info, err := current.Info(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			result[info.Name] = true
		}
		return result
	}
	all := []string{ScheduleJobTool, ListScheduledJobsTool, CancelScheduledJobTool}
	without := names(&Execution{JobID: "j", Registry: NewRegistry(Dependencies{})})
	with := names(&Execution{JobID: "j", Registry: NewRegistry(Dependencies{ScheduledJobs: &fakeScheduledJobs{}})})
	plan := names(&Execution{JobID: "j", Registry: NewRegistry(Dependencies{ScheduledJobs: &fakeScheduledJobs{}}), PlanMode: true})
	for _, name := range all {
		if without[name] {
			t.Errorf("%s exposed without a scheduler", name)
		}
		if !with[name] {
			t.Errorf("%s missing when the scheduler is configured", name)
		}
		if plan[name] {
			t.Errorf("%s exposed in plan mode", name)
		}
	}
}

// A recurring job runs unattended again and again, so creating or deleting
// one must never be easier to approve than a one-shot reminder.
func TestScheduledJobRulingIsAtLeastAsStrictAsReminder(t *testing.T) {
	t.Parallel()
	reminder := guard.ClassifyTool(ReminderTool, json.RawMessage(`{"message":"m","delay_minutes":5}`), guard.Config{})
	rememberedReminder := guard.NewMemory()
	rememberedReminder.Add(reminder.Kind)
	strictness := map[guard.Action]int{guard.ActionAllow: 0, guard.ActionAsk: 1, guard.ActionDeny: 2}
	for _, call := range []struct{ name, args string }{
		{ScheduleJobTool, `{"prompt":"检查磁盘","schedule":"0 9 * * *"}`},
		{CancelScheduledJobTool, `{"job_id":"cron-1"}`},
	} {
		ruling := guard.ClassifyTool(call.name, json.RawMessage(call.args), guard.Config{})
		if ruling.Risk != guard.NeedsConfirm || ruling.Kind == reminder.Kind {
			t.Fatalf("%s ruling = %+v", call.name, ruling)
		}
		for _, mode := range []guard.Mode{guard.Unattended, guard.ReadOnly, guard.ReadWrite, guard.Silent, guard.FullAccess} {
			config := guard.Config{Mode: mode}
			for _, memory := range []*guard.Memory{nil, rememberedReminder} {
				got := guard.Decide(config, ruling, memory)
				want := guard.Decide(config, reminder, nil)
				if strictness[got.Action] < strictness[want.Action] {
					t.Errorf("%s in %s mode = %s, reminder = %s", call.name, mode, got.Action, want.Action)
				}
			}
		}
		if got := guard.Decide(guard.Config{Mode: guard.Unattended}, ruling, nil); got.Action != guard.ActionDeny {
			t.Errorf("%s unattended = %+v, want deny", call.name, got)
		}
		if got := guard.Decide(guard.Config{Mode: guard.ReadWrite}, ruling, rememberedReminder); got.Action != guard.ActionAsk {
			t.Errorf("%s covered by a remembered reminder approval: %+v", call.name, got)
		}
	}
	if ruling := guard.ClassifyTool(ListScheduledJobsTool, json.RawMessage(`{}`), guard.Config{}); ruling.Risk != guard.Safe {
		t.Fatalf("list ruling = %+v", ruling)
	}
}

func TestUnattendedRunCannotCreateOrCancelScheduledJobs(t *testing.T) {
	t.Parallel()
	jobs := &fakeScheduledJobs{jobs: []ScheduledJob{{ID: "cron-1", Prompt: "p", Schedule: "0 9 * * *", Timezone: "UTC", Enabled: true}}}
	execution := &Execution{JobID: "run-1", ConversationID: "conv-1", Registry: NewRegistry(Dependencies{ScheduledJobs: jobs}), Permission: guard.Config{Mode: guard.Unattended}}
	for _, call := range []Call{
		{ID: "c1", Name: ScheduleJobTool, Args: json.RawMessage(`{"prompt":"检查磁盘","schedule":"0 9 * * *"}`)},
		{ID: "c2", Name: CancelScheduledJobTool, Args: json.RawMessage(`{"job_id":"cron-1"}`)},
	} {
		output, err := execution.initial(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}
		if output.OK || !strings.Contains(output.Text, "权限策略已拒绝") {
			t.Fatalf("%s unattended output = %+v", call.Name, output)
		}
	}
	if created, cancelled := jobs.calls(); created != 0 || cancelled != 0 {
		t.Fatalf("unattended run reached the scheduler: created=%d cancelled=%d", created, cancelled)
	}
	output, err := execution.initial(context.Background(), Call{ID: "c3", Name: ListScheduledJobsTool, Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !output.OK || !strings.Contains(output.Text, `"id":"cron-1"`) {
		t.Fatalf("list output = %+v", output)
	}
}

func TestScheduledJobPrecheckFailsBeforeConfirmation(t *testing.T) {
	t.Parallel()
	jobs := &fakeScheduledJobs{}
	execution := &Execution{JobID: "run-1", ConversationID: "conv-1", Registry: NewRegistry(Dependencies{ScheduledJobs: jobs}), Permission: guard.Config{Mode: guard.ReadWrite}}
	for _, test := range []struct {
		args string
		want string
	}{
		{`{"prompt":"p","schedule":"@every 10s"}`, "1 分钟"},
		{`{"prompt":"","schedule":"0 9 * * *"}`, "定时任务内容不能为空"},
		{`{"prompt":"p","schedule":"@at 2026-01-02T09:00:00Z"}`, "send_reminder"},
	} {
		// A call that reached the confirmation step would interrupt instead of returning a failure.
		output, err := execution.initial(context.Background(), Call{ID: "c", Name: ScheduleJobTool, Args: json.RawMessage(test.args)})
		if err != nil || output.OK || !strings.Contains(output.Text, test.want) {
			t.Fatalf("%s: output = %+v err = %v", test.args, output, err)
		}
	}
	noConversation := &Execution{JobID: "run-1", Registry: NewRegistry(Dependencies{ScheduledJobs: jobs}), Permission: guard.Config{Mode: guard.ReadWrite}}
	output, err := noConversation.initial(context.Background(), Call{ID: "c", Name: CancelScheduledJobTool, Args: json.RawMessage(`{"job_id":"cron-1"}`)})
	if err != nil || output.OK || !strings.Contains(output.Text, "会话标识") {
		t.Fatalf("without conversation: output = %+v err = %v", output, err)
	}
	if created, cancelled := jobs.calls(); created != 0 || cancelled != 0 {
		t.Fatalf("rejected calls reached the scheduler: created=%d cancelled=%d", created, cancelled)
	}
}

func TestExecuteScheduledJobBindsTheConversation(t *testing.T) {
	t.Parallel()
	jobs := &fakeScheduledJobs{}
	audited := 0
	execution := &Execution{JobID: "run-1", ConversationID: "conv-1", Registry: NewRegistry(Dependencies{ScheduledJobs: jobs, Audit: func(context.Context, AuditEntry) error {
		audited++
		return nil
	}})}
	created := execution.executeScheduledJob(context.Background(), Call{ID: "c1", Name: ScheduleJobTool, Args: json.RawMessage(`{"prompt":"检查磁盘","schedule":"0 9 * * *","name":"巡检"}`)})
	if !created.OK || !strings.Contains(created.Text, "cron-1") || !strings.Contains(created.Text, "2026-01-02T09:00:00Z") {
		t.Fatalf("create output = %+v", created)
	}
	cancelled := execution.executeScheduledJob(context.Background(), Call{ID: "c2", Name: CancelScheduledJobTool, Args: json.RawMessage(`{"job_id":" cron-1 "}`)})
	if !cancelled.OK || !strings.Contains(cancelled.Text, "cron-1") {
		t.Fatalf("cancel output = %+v", cancelled)
	}
	if len(jobs.created) != 1 || jobs.created[0].Prompt != "检查磁盘" || jobs.created[0].Name != "巡检" {
		t.Fatalf("created = %+v", jobs.created)
	}
	if len(jobs.cancelled) != 1 || jobs.cancelled[0] != "cron-1" {
		t.Fatalf("cancelled = %+v", jobs.cancelled)
	}
	for _, conversation := range jobs.conversations {
		if conversation != "conv-1" {
			t.Fatalf("job bound to %q, want the current conversation", conversation)
		}
	}
	if audited != 2 {
		t.Fatalf("audit entries = %d", audited)
	}
}

func TestScheduledJobConfirmationShowsScheduleAndPrompt(t *testing.T) {
	t.Parallel()
	rendered := DisplayCall(Call{Name: ScheduleJobTool, Args: json.RawMessage(`{"prompt":"检查磁盘","schedule":"0 9 * * 1-5","timezone":"Asia/Shanghai"}`)})
	for _, want := range []string{"0 9 * * 1-5", "Asia/Shanghai", "检查磁盘"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered %q is missing %q", rendered, want)
		}
	}
}
