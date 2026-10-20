package cron

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	storepkg "github.com/Hello-CTF/NexTerm/internal/store"
)

type ownerExecution struct {
	trigger Trigger
	ownerID string
}

func TestDatabaseOwnerPreservedThroughExecutionLifecycle(t *testing.T) {
	cronStore := openDatabaseStore(t)
	ctxA := ipc.WithUserID(context.Background(), "user-a")
	ctxB := ipc.WithUserID(context.Background(), "user-b")
	clock := newFakeClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	executions := make(chan ownerExecution, 1)
	scheduler := testScheduler(t, cronStore, executorFunc(func(ctx context.Context, trigger Trigger) error {
		executions <- ownerExecution{trigger: trigger, ownerID: ipc.OwnerID(ctx)}
		return nil
	}), clock)
	registration := testRegistration("session")
	registration.ID = "owned-job"
	job, err := scheduler.Register(ctxA, registration)
	if err != nil {
		t.Fatal(err)
	}
	if job.OwnerID != "user-a" || job.OwnerKind != OwnerKindAccount {
		t.Fatalf("registered owner = %q/%q", job.OwnerID, job.OwnerKind)
	}

	now := clock.Add(time.Minute)
	claimed, err := scheduler.runOnce(context.Background(), now)
	if err != nil || claimed != 1 {
		t.Fatalf("background claim = %d, %v", claimed, err)
	}
	waitSchedulerWithTimeout(t, scheduler)
	execution := <-executions
	if execution.trigger.OwnerID != "user-a" || execution.trigger.OwnerKind != OwnerKindAccount || execution.ownerID != "user-a" {
		t.Fatalf("execution lost owner: %+v", execution)
	}

	jobs, err := scheduler.List(ctxA, "session")
	if err != nil || len(jobs) != 1 {
		t.Fatalf("owner list = %+v, %v", jobs, err)
	}
	finished := jobs[0]
	if finished.OwnerID != "user-a" || finished.OwnerKind != OwnerKindAccount || finished.Running() || finished.LastError != "" {
		t.Fatalf("finished job = %+v", finished)
	}
	if foreign, err := scheduler.List(ctxB, "session"); err != nil || len(foreign) != 0 {
		t.Fatalf("foreign list = %+v, %v", foreign, err)
	}
	if _, err := scheduler.Get(ctxB, "session", job.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign get error = %v", err)
	}
	var row storepkg.CronJob
	if err := cronStore.db.WithContext(context.Background()).Where("id = ?", job.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.OwnerID != "user-a" || row.OwnerKind != string(OwnerKindAccount) {
		t.Fatalf("durable owner changed to %q/%q", row.OwnerID, row.OwnerKind)
	}
}

func TestDatabaseCreateAndCASUseRequestAndDurableOwners(t *testing.T) {
	cronStore := openDatabaseStore(t)
	ctxA := ipc.WithUserID(context.Background(), "user-a")
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	input := fullTestJob("job", "session", now)
	input.OwnerID = "request-forgery"
	input.OwnerKind = OwnerKindLegacy
	created, err := cronStore.Create(ctxA, input, 4)
	if err != nil {
		t.Fatal(err)
	}
	if created.OwnerID != "user-a" || created.OwnerKind != OwnerKindAccount {
		t.Fatalf("create owner = %q/%q", created.OwnerID, created.OwnerKind)
	}

	candidate := created
	candidate.OwnerID = ""
	candidate.OwnerKind = OwnerKindOpen
	candidate.LastError = "CAS reached the row"
	updated, err := cronStore.CompareAndSwap(WithInternalAccess(context.Background()), candidate, created.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if updated.OwnerID != "user-a" || updated.OwnerKind != OwnerKindAccount {
		t.Fatalf("CAS returned owner = %q/%q", updated.OwnerID, updated.OwnerKind)
	}
	jobs, err := cronStore.List(ctxA)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("owner list = %+v, %v", jobs, err)
	}
	if jobs[0].OwnerID != "user-a" || jobs[0].OwnerKind != OwnerKindAccount || jobs[0].LastError != "CAS reached the row" {
		t.Fatalf("reloaded job = %+v", jobs[0])
	}
}

func TestOwnerlessLegacyGateAndDesktopCompatibility(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		allowLegacy bool
	}{
		{name: "account mode blocks", allowLegacy: false},
		{name: "desktop open mode runs", allowLegacy: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			cronStore := newTestSQLiteStore(t)
			now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
			job := fullTestJob("legacy", "session", now)
			job.Run = RunState{}
			job.Lease = Lease{}
			job.NextRunAt = now
			job.RetryAt = time.Time{}
			job.CircuitOpenUntil = time.Time{}
			if _, err := cronStore.Create(context.Background(), job, 4); err != nil {
				t.Fatal(err)
			}
			if err := cronStore.db.Model(&storepkg.CronJob{}).Where("id = ?", job.ID).
				Updates(map[string]any{"owner_id": "", "owner_kind": ""}).Error; err != nil {
				t.Fatal(err)
			}
			clock := newFakeClock(now)
			triggers := make(chan Trigger, 1)
			scheduler := testScheduler(t, cronStore, executorFunc(func(_ context.Context, trigger Trigger) error {
				triggers <- trigger
				return nil
			}), clock, func(options *Options) {
				options.AllowOwnerlessLegacy = testCase.allowLegacy
			})
			claimed, err := scheduler.runOnce(context.Background(), now)
			if err != nil {
				t.Fatal(err)
			}
			if !testCase.allowLegacy {
				if claimed != 0 || len(triggers) != 0 {
					t.Fatalf("legacy execution claimed = %d, triggers = %d", claimed, len(triggers))
				}
				stored := loadStoredJob(t, cronStore, job.ID)
				if stored.Enabled || stored.OwnerID != "" || stored.OwnerKind != OwnerKindLegacy || !strings.Contains(stored.LastError, ErrOwnerlessLegacy.Error()) {
					t.Fatalf("blocked legacy job = %+v", stored)
				}
				return
			}
			if claimed != 1 {
				t.Fatalf("desktop legacy claim = %d", claimed)
			}
			waitSchedulerWithTimeout(t, scheduler)
			trigger := <-triggers
			if trigger.OwnerID != "" || trigger.OwnerKind != OwnerKindLegacy {
				t.Fatalf("desktop legacy trigger = %+v", trigger)
			}
			stored := loadStoredJob(t, cronStore, job.ID)
			if stored.OwnerKind != OwnerKindLegacy || stored.OwnerID != "" || stored.Running() || stored.LastError != "" {
				t.Fatalf("desktop legacy result = %+v", stored)
			}
		})
	}
}
