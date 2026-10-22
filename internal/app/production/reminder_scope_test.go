package production

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestReminderSchedulerRejectsForeignAndStaleScope(t *testing.T) {
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	services, database, _, sessionA, _ := composeOutcomeRuntimeContext(t, "echo hi", ctxA, false)
	conversationA, err := database.ConvCreate(ctxA, "A", map[string]any{"scope": tools.Scope{SessionID: sessionA}})
	if err != nil {
		t.Fatal(err)
	}
	conversationB, err := database.ConvCreate(ctxB, "B", map[string]any{"scope": tools.Scope{SessionID: sessionA}})
	if err != nil {
		t.Fatal(err)
	}
	scheduler, _, err := composeCronScheduler(ctxA, services, cron.Options{})
	if err != nil {
		t.Fatal(err)
	}
	reminders := reminderScheduler{scheduler: scheduler, conversations: database, scopeValidator: productionScopeValidator(services)}
	reminder := tools.Reminder{Message: "check", At: time.Now().Add(time.Hour)}
	if _, err := reminders.ScheduleReminder(ctxA, conversationA.ID, reminder); err != nil {
		t.Fatalf("valid reminder rejected: %v", err)
	}
	if _, err := reminders.ScheduleReminder(ctxB, conversationB.ID, reminder); err == nil {
		t.Fatal("foreign reminder scope was accepted")
	}
	if err := services.Sessions.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reminders.ScheduleReminder(ctxA, conversationA.ID, reminder); err == nil {
		t.Fatal("stale reminder scope was accepted")
	}
	jobsA, err := scheduler.List(ctxA, conversationA.ID)
	if err != nil || len(jobsA) != 1 {
		t.Fatalf("rejected reminders changed job count: %+v, %v", jobsA, err)
	}
	jobsB, err := scheduler.List(ctxB, conversationB.ID)
	if err != nil || len(jobsB) != 0 {
		t.Fatalf("foreign reminder created jobs: %+v, %v", jobsB, err)
	}
}
