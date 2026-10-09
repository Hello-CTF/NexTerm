package production

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestReminderSchedulerRegistersOneShotJob(t *testing.T) {
	ctx := context.Background()
	services, database, _, _ := composeOutcomeRuntime(t, "echo hi")
	if services.cron == nil {
		t.Fatal("cron runtime not composed")
	}
	conversation, err := database.ConvCreate(ctx, "reminder target", nil)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	scheduled, err := reminderScheduler{scheduler: services.cron.scheduler}.ScheduleReminder(ctx, conversation.ID, tools.Reminder{Message: "检查备份", At: at})
	if err != nil {
		t.Fatal(err)
	}
	if scheduled.ID == "" || !scheduled.At.Equal(at) {
		t.Fatalf("scheduled = %+v", scheduled)
	}
	job, err := services.cron.scheduler.Get(ctx, conversation.ID, scheduled.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Schedule != cron.AtExpression(at) || !job.Enabled || !job.NextRunAt.Equal(at) {
		t.Fatalf("job = %+v", job)
	}
	if !strings.Contains(job.Name, "检查备份") || job.Prompt != "检查备份" {
		t.Fatalf("reminder job not recognizable in the cron list: %+v", job)
	}

	dispatcher := ipc.NewDispatcher()
	if err := services.cron.registerCommands(dispatcher); err != nil {
		t.Fatal(err)
	}
	var listed []cron.Job
	requireCronData(t, dispatchCronTest(dispatcher, "cron_list", `{"sessionId":"`+conversation.ID+`"}`), &listed)
	if len(listed) != 1 || listed[0].ID != scheduled.ID {
		t.Fatalf("reminder not visible through cron_list: %+v", listed)
	}
}
