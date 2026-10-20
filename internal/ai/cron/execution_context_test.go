package cron

import (
	"context"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/owner"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestSchedulerExecutorContextIsAccountScoped(t *testing.T) {
	storage := newTestSQLiteStore(t)
	storage.shared = false
	ctxA := ipc.WithUserID(context.Background(), "owner-a")
	ctxB := ipc.WithUserID(context.Background(), "owner-b")
	scheduler := testScheduler(t, storage, executorFunc(func(ctx context.Context, trigger Trigger) error {
		if internalAccess(ctx) {
			t.Fatal("executor retained scheduler internal access")
		}
		if owner.OpenAccess(ctx) {
			t.Fatal("account executor retained Open access")
		}
		listed, err := storage.List(ctx)
		if err != nil {
			return err
		}
		for _, job := range listed {
			if job.OwnerID != "owner-a" {
				t.Fatalf("account executor listed foreign job: %+v", job)
			}
		}
		due, err := storage.ListDue(ctx, time.Now())
		if err != nil {
			return err
		}
		for _, job := range due {
			if job.OwnerID != "owner-a" {
				t.Fatalf("account executor listed foreign due job: %+v", job)
			}
		}
		return nil
	}), newFakeClock(time.Now()), func(options *Options) {
		options.MaxJobsPerSession = 1
	})
	for _, ctx := range []context.Context{ctxA, ctxB} {
		if _, err := scheduler.Register(ctx, Registration{SessionID: "shared-session", Prompt: "run", Schedule: "* * * * *"}); err != nil {
			t.Fatalf("same-session registration must be counted per owner: %v", err)
		}
	}
	jobA, err := scheduler.Get(ctxA, "shared-session", schedulerJobs(t, scheduler, ctxA, "shared-session")[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := scheduler.claim(WithInternalAccess(context.Background()), jobA, time.Now())
	if err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	scheduler.wg.Wait()
}

func schedulerJobs(t *testing.T, scheduler *Scheduler, ctx context.Context, sessionID string) []Job {
	t.Helper()
	jobs, err := scheduler.List(ctx, sessionID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %+v, %v", jobs, err)
	}
	return jobs
}
