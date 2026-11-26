package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/cloudwego/eino/schema"
)

type recordingScheduledJobs struct {
	creates      atomic.Int64
	mu           sync.Mutex
	conversation string
	specs        []tools.ScheduledJobSpec
}

func (r *recordingScheduledJobs) ValidateSchedule(context.Context, string, string) error { return nil }

func (r *recordingScheduledJobs) CreateJob(_ context.Context, conversationID string, spec tools.ScheduledJobSpec) (tools.ScheduledJob, error) {
	r.creates.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conversation = conversationID
	r.specs = append(r.specs, spec)
	return tools.ScheduledJob{ID: "cron-1", Schedule: spec.Schedule, Timezone: "UTC", Enabled: true}, nil
}

func (r *recordingScheduledJobs) ListJobs(context.Context, string) ([]tools.ScheduledJob, error) {
	return nil, nil
}

func (r *recordingScheduledJobs) CancelJob(_ context.Context, _ string, jobID string) (tools.ScheduledJob, error) {
	return tools.ScheduledJob{ID: jobID}, nil
}

func TestScheduleJobConfirmThenCreate(t *testing.T) {
	jobs := &recordingScheduledJobs{}
	chat := sequenceModel(
		toolCallMessage(namedToolCall("cron", "schedule_job", `{"prompt":"检查磁盘用量","schedule":"0 9 * * 1-5","timezone":"Asia/Shanghai"}`)),
		schema.AssistantMessage("已创建定时任务", nil),
	)
	runner, storage := testRunner(t, chat, tools.Dependencies{ScheduledJobs: jobs}, 0)
	conversation, err := storage.ConvCreate(context.Background(), "定时任务测试", nil)
	if err != nil {
		t.Fatal(err)
	}
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "每个工作日早上检查磁盘", ConversationID: conversation.ID}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")
	if confirmation.Tool != "schedule_job" {
		t.Fatalf("confirmation = %+v", confirmation)
	}
	for _, want := range []string{"0 9 * * 1-5", "Asia/Shanghai", "检查磁盘用量"} {
		if !strings.Contains(confirmation.Rendered, want) {
			t.Fatalf("confirmation %q does not show %q", confirmation.Rendered, want)
		}
	}
	if jobs.creates.Load() != 0 {
		t.Fatal("job created before user confirmation")
	}
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	events := waitClosed(t, stream)
	if jobs.creates.Load() != 1 {
		t.Fatalf("create calls = %d", jobs.creates.Load())
	}
	jobs.mu.Lock()
	conversationID, specs := jobs.conversation, append([]tools.ScheduledJobSpec(nil), jobs.specs...)
	jobs.mu.Unlock()
	if conversationID != conversation.ID {
		t.Fatalf("job conversation = %q, want %q", conversationID, conversation.ID)
	}
	if len(specs) != 1 || specs[0].Prompt != "检查磁盘用量" || specs[0].Schedule != "0 9 * * 1-5" || specs[0].Timezone != "Asia/Shanghai" {
		t.Fatalf("specs = %+v", specs)
	}
	var result Event
	for _, event := range events {
		if event.Type == "toolResult" && event.ID == "cron" {
			result = event
		}
	}
	if !result.OK || !strings.Contains(result.Text, "cron-1") {
		t.Fatalf("toolResult = %+v", result)
	}
}

func TestScheduleJobDenyKeepsSchedulerUnused(t *testing.T) {
	jobs := &recordingScheduledJobs{}
	chat := sequenceModel(
		toolCallMessage(namedToolCall("cron", "schedule_job", `{"prompt":"检查磁盘用量","schedule":"@daily"}`)),
		schema.AssistantMessage("好的", nil),
	)
	runner, storage := testRunner(t, chat, tools.Dependencies{ScheduledJobs: jobs}, 0)
	conversation, err := storage.ConvCreate(context.Background(), "定时任务测试", nil)
	if err != nil {
		t.Fatal(err)
	}
	stream := &SliceStream{}
	response, err := runner.Start(context.Background(), ChatArgs{Message: "每天检查磁盘", ConversationID: conversation.ID}, StaticStream(stream))
	if err != nil {
		t.Fatal(err)
	}
	confirmation := waitEvent(t, stream, "confirmRequired")
	if err := runner.Confirm(Confirmation{JobID: response.JobID, CallID: confirmation.ID, Nonce: confirmation.Nonce, Decision: "deny"}); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, stream)
	if jobs.creates.Load() != 0 {
		t.Fatal("denied job reached the scheduler")
	}
}
