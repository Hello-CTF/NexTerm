package cron

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

func syncedFrom(job Job) SyncedJob {
	return SyncedJob{
		ID: job.ID, SessionID: job.SessionID, Name: job.Name, Prompt: job.Prompt, Schedule: job.Schedule,
		Timezone: job.Timezone, Enabled: job.Enabled, Timeout: job.Timeout, ModelProfileID: job.ModelProfileID,
		DeviceID: job.DeviceID, DeviceName: job.DeviceName, CreatedAt: job.CreatedAt, ConfigUpdatedAt: job.ConfigUpdatedAt,
	}
}

// A job synced to a second device is shown there but fires only on the device it is bound to,
// even when both devices' schedulers scan the same due time.
func TestSyncedJobRunsOnlyOnItsDeviceDatabase(t *testing.T) {
	ctx := context.Background()
	deviceA, deviceB := openDatabaseStore(t), openDatabaseStore(t)
	if deviceA.Device().ID == deviceB.Device().ID {
		t.Fatal("two databases share a device ID")
	}
	var calls atomic.Int32
	executor := executorFunc(func(context.Context, Trigger) error {
		calls.Add(1)
		return nil
	})
	start := time.Now()
	newScheduler := func(jobs *SQLStore, owner string) *Scheduler {
		scheduler, err := NewScheduler(jobs, executor, Options{
			Now: func() time.Time { return start }, OwnerID: owner, LeaseDuration: time.Minute,
			ExecutionTimeout: time.Minute, PollInterval: time.Hour,
		})
		if err != nil {
			t.Fatal(err)
		}
		return scheduler
	}
	schedulerA, schedulerB := newScheduler(deviceA, "a"), newScheduler(deviceB, "b")
	job, err := schedulerA.Register(ctx, testRegistration("session"))
	if err != nil {
		t.Fatal(err)
	}
	if job.DeviceID != deviceA.Device().ID {
		t.Fatalf("new job bound to %q, want device A", job.DeviceID)
	}
	if err := deviceB.PutSynced(ctx, syncedFrom(job), nil, start); err != nil {
		t.Fatal(err)
	}
	listed, err := deviceB.List(ctx)
	if err != nil || len(listed) != 1 || !listed[0].ForeignDevice || listed[0].DeviceID != job.DeviceID {
		t.Fatalf("device B jobs=%+v err=%v", listed, err)
	}
	if _, err := schedulerB.SetEnabled(ctx, "session", job.ID, false); !errors.Is(err, ErrForeignDevice) {
		t.Fatalf("editing a foreign job: %v", err)
	}

	due := job.NextRunAt.Add(time.Second)
	internal := WithInternalAccess(ctx)
	if _, err := schedulerB.runOnce(internal, due); err != nil {
		t.Fatal(err)
	}
	if _, err := schedulerA.runOnce(internal, due); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for calls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("executions=%d, want exactly one on the bound device", got)
	}
	// Let the run finish its bookkeeping before the databases close.
	for time.Now().Before(deadline) {
		current, err := deviceA.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(current) == 1 && !current[0].Running() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stillDue, err := deviceB.ListDueIdle(internal, due.Add(time.Hour), 10)
	if err != nil || len(stillDue) != 0 {
		t.Fatalf("device B due jobs=%+v err=%v, want none", stillDue, err)
	}
}

// Synced updates keep run bookkeeping, move the revision and refuse stale snapshots;
// synced deletions respect newer configuration; user deletions leave a sync tombstone.
func TestSyncedJobWritesDatabase(t *testing.T) {
	ctx := context.Background()
	jobs := openDatabaseStore(t)
	now := time.Date(2026, time.March, 1, 8, 0, 0, 0, time.UTC)
	synced := SyncedJob{
		ID: "job-synced", SessionID: "session", Prompt: "check disks", Schedule: "0 9 * * *", Timezone: "UTC",
		Enabled: true, Timeout: time.Minute, DeviceID: "elsewhere", DeviceName: "laptop",
		CreatedAt: now.Add(-time.Hour), ConfigUpdatedAt: now.Add(-time.Minute),
	}
	if err := jobs.PutSynced(ctx, synced, nil, now); err != nil {
		t.Fatal(err)
	}
	listed, err := jobs.List(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("jobs=%+v err=%v", listed, err)
	}
	stored := listed[0]
	if !stored.ConfigUpdatedAt.Equal(synced.ConfigUpdatedAt) || stored.DeviceName != "laptop" || !stored.NextRunAt.After(now) {
		t.Fatalf("stored job=%+v", stored)
	}
	if err := jobs.PutSynced(ctx, synced, nil, now); !errors.Is(err, ErrSyncConflict) {
		t.Fatalf("a write expecting no job over an existing one: %v", err)
	}
	update := synced
	update.Enabled, update.ConfigUpdatedAt = false, now
	if err := jobs.PutSynced(ctx, update, &stored, now); err != nil {
		t.Fatal(err)
	}
	if err := jobs.PutSynced(ctx, update, &stored, now); !errors.Is(err, ErrSyncConflict) {
		t.Fatalf("a write bound to a stale revision: %v", err)
	}
	invalid := synced
	invalid.ID, invalid.Schedule = "job-invalid", "not a schedule"
	if err := jobs.PutSynced(ctx, invalid, nil, now); err == nil {
		t.Fatal("an invalid schedule must be rejected")
	}

	if removed, err := jobs.DeleteSynced(ctx, synced.ID, now.Add(-time.Second)); err != nil || removed {
		t.Fatalf("an older deletion removed=%v err=%v", removed, err)
	}
	if removed, err := jobs.DeleteSynced(ctx, synced.ID, now.Add(time.Second)); err != nil || !removed {
		t.Fatalf("a newer deletion removed=%v err=%v", removed, err)
	}

	created, err := jobs.Create(ctx, Job{
		ID: "job-local", SessionID: "session", Prompt: "p", Schedule: "0 9 * * *", Timezone: "UTC",
		Enabled: true, Timeout: time.Minute, CreatedAt: now, UpdatedAt: now, NextRunAt: now.Add(time.Hour),
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Delete(ctx, created.SessionID, created.ID, created.Revision); err != nil {
		t.Fatal(err)
	}
	var tombstone model.SyncTombstone
	if err := jobs.db.WithContext(ctx).Where("id = ?", created.ID).First(&tombstone).Error; err != nil || tombstone.Kind != SyncTombstoneKind {
		t.Fatalf("deletion tombstone=%+v err=%v", tombstone, err)
	}
}
