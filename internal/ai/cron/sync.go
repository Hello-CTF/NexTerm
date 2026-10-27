package cron

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

// SyncTombstoneKind marks a deleted job in the sync tombstone table.
const SyncTombstoneKind = "cron"

// ErrSyncConflict reports that a job changed after the caller read it; the
// synced write was not applied.
var ErrSyncConflict = errors.New("定时任务在同步期间被修改")

// SyncedJob is the configuration of a job as it travels between devices.
// Run bookkeeping (next run, leases, failures) stays on each device.
type SyncedJob struct {
	ID              string
	SessionID       string
	Name            string
	Prompt          string
	Schedule        string
	Timezone        string
	Enabled         bool
	Timeout         time.Duration
	ModelProfileID  string
	DeviceID        string
	DeviceName      string
	CreatedAt       time.Time
	ConfigUpdatedAt time.Time
}

func validateSyncedJob(job SyncedJob) error {
	if strings.TrimSpace(job.ID) == "" || job.ID != strings.TrimSpace(job.ID) || len(job.ID) > 128 {
		return errors.New("定时任务 ID 无效")
	}
	if strings.TrimSpace(job.SessionID) == "" {
		return errors.New("定时任务必须关联一个会话")
	}
	if strings.TrimSpace(job.Prompt) == "" {
		return errors.New("定时任务的提示词不能为空")
	}
	if job.Timeout <= 0 {
		return errors.New("定时任务超时时间必须为正数")
	}
	if strings.TrimSpace(job.DeviceID) == "" {
		return errors.New("定时任务缺少绑定设备")
	}
	if _, err := loadSchedule(job.Schedule, job.Timezone); err != nil {
		return err
	}
	return nil
}

// nextRun is the job's next occurrence after now, for the device it is bound to.
func nextRun(job SyncedJob, now time.Time) (time.Time, error) {
	schedule, err := loadSchedule(job.Schedule, job.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	next, ok := schedule.Next(now)
	if !ok {
		return time.Time{}, fmt.Errorf("%w: no occurrence within ten years", ErrInvalidSchedule)
	}
	return next, nil
}

// PutSynced writes a job received through sync when the caller's job still
// equals expected (nil: no job). It keeps the local run bookkeeping, refuses
// a job that is running here, and reschedules from now when the schedule or
// the enabled state changed.
func (s *SQLStore) PutSynced(ctx context.Context, job SyncedJob, expected *Job, now time.Time) error {
	if err := s.validateAccess(ctx); err != nil {
		return err
	}
	if err := validateSyncedJob(job); err != nil {
		return err
	}
	next, err := nextRun(job, now)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cronJob := query.Use(tx).CronJob
		current, err := cronJob.WithContext(ctx).
			Where(cronJob.ID.Eq(job.ID)).
			Where(s.ownerConditions(ctx, cronJob.TableName())...).
			First()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			current = nil
		} else if err != nil {
			return err
		}
		if (current == nil) != (expected == nil) || current != nil && current.Revision != expected.Revision {
			return ErrSyncConflict
		}
		if current == nil {
			ownerID, ownerKind := ownerFromContext(ctx)
			row := store.CronJob{
				ID: job.ID, SessionID: job.SessionID, Name: job.Name, Prompt: job.Prompt, Schedule: job.Schedule,
				Timezone: job.Timezone, Enabled: job.Enabled, TimeoutMS: job.Timeout.Milliseconds(),
				ModelProfileID: job.ModelProfileID, CreatedAt: timeMS(job.CreatedAt), UpdatedAt: timeMS(job.ConfigUpdatedAt),
				Revision: 1, NextRunAt: timeMS(next), OwnerID: ownerID, OwnerKind: string(ownerKind),
				DeviceID: job.DeviceID, DeviceName: job.DeviceName, ConfigUpdatedAt: timeMS(job.ConfigUpdatedAt),
			}
			if err := cronJob.WithContext(ctx).Create(&row); err != nil {
				if store.IsUniqueErr(err) {
					return ErrConflict
				}
				return err
			}
			return nil
		}
		if current.RunID != "" {
			return ErrJobRunning
		}
		assignments := map[string]any{
			"session_id": job.SessionID, "name": job.Name, "prompt": job.Prompt, "schedule": job.Schedule,
			"timezone": job.Timezone, "enabled": job.Enabled, "timeout_ms": job.Timeout.Milliseconds(),
			"model_profile_id": job.ModelProfileID, "created_at": timeMS(job.CreatedAt),
			"updated_at": max(current.UpdatedAt, timeMS(job.ConfigUpdatedAt)), "revision": current.Revision + 1,
			"device_id": job.DeviceID, "device_name": job.DeviceName, "config_updated_at": timeMS(job.ConfigUpdatedAt),
		}
		if current.Schedule != job.Schedule || current.Timezone != job.Timezone || current.Enabled != job.Enabled {
			assignments["next_run_at"] = timeMS(next)
			assignments["retry_at"] = nil
			assignments["circuit_open_until"] = nil
		}
		info, err := cronJob.WithContext(ctx).
			Where(cronJob.ID.Eq(job.ID), cronJob.Revision.Eq(current.Revision), cronJob.RunID.Eq("")).
			Where(s.ownerConditions(ctx, cronJob.TableName())...).
			UpdateColumns(assignments)
		if err != nil {
			return err
		}
		if info.RowsAffected != 1 {
			return ErrSyncConflict
		}
		return nil
	})
}

// DeleteSynced applies a synced deletion: the caller's job is removed unless
// its configuration changed after deletedAt. A job running here is kept
// until its run ends (ErrJobRunning). It reports whether the deletion took
// effect (an absent job counts as removed).
func (s *SQLStore) DeleteSynced(ctx context.Context, id string, deletedAt time.Time) (bool, error) {
	if err := s.validateAccess(ctx); err != nil {
		return false, err
	}
	applied := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cronJob := query.Use(tx).CronJob
		current, err := cronJob.WithContext(ctx).
			Where(cronJob.ID.Eq(id)).
			Where(s.ownerConditions(ctx, cronJob.TableName())...).
			First()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			applied = true
			return nil
		}
		if err != nil {
			return err
		}
		if configUpdatedAt(current).After(deletedAt) {
			return nil
		}
		if current.RunID != "" {
			return ErrJobRunning
		}
		info, err := cronJob.WithContext(ctx).
			Where(cronJob.ID.Eq(id), cronJob.Revision.Eq(current.Revision), cronJob.RunID.Eq("")).
			Where(s.ownerConditions(ctx, cronJob.TableName())...).
			Delete()
		if err != nil {
			return err
		}
		if info.RowsAffected != 1 {
			return ErrSyncConflict
		}
		applied = true
		return nil
	})
	return applied, err
}
