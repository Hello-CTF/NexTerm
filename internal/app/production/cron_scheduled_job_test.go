package production

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/agent"
	"github.com/Hello-CTF/NexTerm/internal/ai/cron"
	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

type startedCronRun struct {
	args       agent.ChatArgs
	unattended bool
}

// recordingCronAgentRunner hands every unattended run it is asked to start to
// the test and finishes it at once.
type recordingCronAgentRunner struct {
	started chan startedCronRun
}

func (r *recordingCronAgentRunner) Start(ctx context.Context, args agent.ChatArgs, factory agent.StreamFactory) (agent.StartResponse, error) {
	stream, err := factory(ctx, "cron-run", args.ConversationID)
	if err != nil {
		return agent.StartResponse{}, err
	}
	select {
	case r.started <- startedCronRun{args: args, unattended: guard.UnattendedFrom(ctx)}:
	default:
	}
	_ = stream.Send(ctx, agent.Event{Type: "done", Answer: "ok"})
	_ = stream.Close()
	return agent.StartResponse{JobID: "cron-run"}, nil
}

func (r *recordingCronAgentRunner) Cancel(string) error { return nil }

func (r *recordingCronAgentRunner) Wait(context.Context, string) error { return nil }

func TestScheduledJobServiceCreatesConversationJobThatRunsThroughCronExecutor(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	cronStore, err := cron.NewSQLiteStore(ctx, database.GORM())
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingCronAgentRunner{started: make(chan startedCronRun, 1)}
	scheduler, err := cron.NewScheduler(cronStore, &cron.AgentExecutor{Runner: runner}, cron.Options{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := database.ConvCreate(ctx, "scheduled job target", nil)
	if err != nil {
		t.Fatal(err)
	}
	service := scheduledJobService{scheduler: scheduler, conversations: database}

	if _, err := service.CreateJob(ctx, "missing-conversation", tools.ScheduledJobSpec{Prompt: "p", Schedule: "@daily"}); err == nil {
		t.Fatal("job created for a conversation that does not exist")
	}
	created, err := service.CreateJob(ctx, conversation.ID, tools.ScheduledJobSpec{Prompt: "检查磁盘用量", Schedule: "@every 1h", Timezone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || !created.Enabled || created.Timezone != "Asia/Shanghai" || created.NextRunAt.IsZero() {
		t.Fatalf("created = %+v", created)
	}
	listed, err := service.ListJobs(ctx, conversation.ID)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID || listed[0].Name != "检查磁盘用量" {
		t.Fatalf("listed = %+v, err = %v", listed, err)
	}
	if other, err := service.ListJobs(ctx, "other-conversation"); err != nil || len(other) != 0 {
		t.Fatalf("job visible in another conversation: %+v, %v", other, err)
	}

	if _, err := database.DB().ExecContext(ctx,
		`UPDATE cron_job SET next_run_at = ? WHERE id = ?`,
		time.Now().Add(-time.Minute).UnixMilli(), created.ID); err != nil {
		t.Fatal(err)
	}
	runtime := &cronRuntime{scheduler: scheduler}
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runtime.Shutdown(shutdownCtx)
	})
	var run startedCronRun
	select {
	case run = <-runner.started:
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for the scheduled job to run")
	}
	if run.args.ConversationID != conversation.ID || run.args.Message != "检查磁盘用量" || run.args.Source != "cron" || !run.unattended {
		t.Fatalf("run = %+v", run)
	}
	finished := waitCronJobFinished(t, scheduler, conversation.ID, created.ID)
	if finished.LastError != "" || !finished.Enabled || !finished.NextRunAt.After(time.Now()) {
		t.Fatalf("recurring job after its run = %+v", finished)
	}

	if _, err := service.CancelJob(ctx, "other-conversation", created.ID); !errors.Is(err, cron.ErrNotFound) {
		t.Fatalf("cancel from another conversation = %v", err)
	}
	cancelled, err := service.CancelJob(ctx, conversation.ID, created.ID)
	if err != nil || cancelled.ID != created.ID {
		t.Fatalf("cancelled = %+v, err = %v", cancelled, err)
	}
	if listed, err := service.ListJobs(ctx, conversation.ID); err != nil || len(listed) != 0 {
		t.Fatalf("job survived cancellation: %+v, %v", listed, err)
	}
}

func TestScheduledJobServiceValidatesRecurringSchedules(t *testing.T) {
	t.Parallel()
	service := scheduledJobService{now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }}
	for _, test := range []struct {
		schedule, timezone string
		want               error
		wantText           string
	}{
		{schedule: "0 9 * * 1-5", timezone: "Asia/Shanghai"},
		{schedule: "* * * * *"},
		{schedule: "@every 1m"},
		{schedule: "@daily"},
		{schedule: "@every 30s", wantText: "1 分钟"},
		{schedule: "@at 2026-01-02T09:00:00Z", want: cron.ErrInvalidSchedule},
		{schedule: "61 * * * *", want: cron.ErrInvalidSchedule},
		{schedule: "0 9 * * *", timezone: "Mars/Olympus", want: cron.ErrInvalidSchedule},
	} {
		err := service.ValidateSchedule(context.Background(), test.schedule, test.timezone)
		switch {
		case test.want == nil && test.wantText == "":
			if err != nil {
				t.Errorf("%q: unexpected error %v", test.schedule, err)
			}
		case test.want != nil:
			if !errors.Is(err, test.want) {
				t.Errorf("%q: err = %v, want %v", test.schedule, err, test.want)
			}
		default:
			if err == nil || !strings.Contains(err.Error(), test.wantText) {
				t.Errorf("%q: err = %v, want %q", test.schedule, err, test.wantText)
			}
		}
	}
}
