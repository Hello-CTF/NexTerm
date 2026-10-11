package cron

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen"
	"gorm.io/gen/field"
	"gorm.io/gorm"
)

type SQLStore struct {
	db *gorm.DB
}

var _ Store = (*SQLStore)(nil)

func NewSQLiteStore(ctx context.Context, db *gorm.DB) (*SQLStore, error) {
	return newSQLStore(ctx, db)
}

func NewPostgresStore(ctx context.Context, db *gorm.DB) (*SQLStore, error) {
	return newSQLStore(ctx, db)
}

func newSQLStore(ctx context.Context, db *gorm.DB) (*SQLStore, error) {
	if db == nil {
		return nil, errors.New("cron: sql store requires a database handle")
	}
	if !db.WithContext(ctx).Migrator().HasTable(&store.CronJob{}) {
		return nil, fmt.Errorf("cron: cron_job table is missing or unreadable")
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) Close() error {
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func (s *SQLStore) List(ctx context.Context) ([]Job, error) {
	cronJob := query.Use(s.db).CronJob
	rows := []store.CronJob{}
	err := cronJob.WithContext(ctx).
		Where(cronOwnerConditions(ctx, cronJob.TableName())...).
		Order(cronJob.CreatedAt, cronJob.ID).
		Scan(&rows)
	if err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, jobFromModel(&row))
	}
	return jobs, nil
}

func (s *SQLStore) Create(ctx context.Context, job Job, maxPerSession int) (Job, error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cronJob := query.Use(tx).CronJob
		existing, err := cronJob.WithContext(ctx).
			Where(cronJob.ID.Eq(job.ID)).
			Where(cronOwnerConditions(ctx, cronJob.TableName())...).
			Count()
		if err != nil {
			return err
		}
		if existing > 0 {
			return ErrConflict
		}
		count, err := cronJob.WithContext(ctx).
			Where(cronJob.SessionID.Eq(job.SessionID)).
			Where(cronOwnerConditions(ctx, cronJob.TableName())...).
			Count()
		if err != nil {
			return err
		}
		if count >= int64(maxPerSession) {
			return ErrJobLimit
		}
		job.Revision = 1
		row := jobToModel(ctx, job)
		if err := cronJob.WithContext(ctx).Create(&row); err != nil {
			if store.IsUniqueErr(err) {
				return ErrConflict
			}
			return err
		}
		return nil
	})
	return job, err
}

func (s *SQLStore) CompareAndSwap(ctx context.Context, job Job, expectedRevision uint64) (Job, error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cronJob := query.Use(tx).CronJob
		current, err := cronJob.WithContext(ctx).
			Where(cronJob.ID.Eq(job.ID)).
			Where(cronOwnerConditions(ctx, cronJob.TableName())...).
			First()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision || current.SessionID != job.SessionID {
			return ErrConflict
		}
		job.Revision = current.Revision + 1
		row := jobToModel(ctx, job)
		info, err := cronJob.WithContext(ctx).
			Where(cronJob.ID.Eq(job.ID), cronJob.Revision.Eq(expectedRevision), cronJob.SessionID.Eq(job.SessionID)).
			Where(cronOwnerConditions(ctx, cronJob.TableName())...).
			UpdateColumns(jobAssignments(row))
		if err != nil {
			return err
		}
		if info.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	})
	return job, err
}

func (s *SQLStore) Delete(ctx context.Context, sessionID, jobID string, expectedRevision uint64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cronJob := query.Use(tx).CronJob
		current, err := cronJob.WithContext(ctx).
			Where(cronJob.ID.Eq(jobID)).
			Where(cronOwnerConditions(ctx, cronJob.TableName())...).
			First()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if current.SessionID != sessionID {
			return ErrNotFound
		}
		if current.Revision != expectedRevision {
			return ErrConflict
		}
		info, err := cronJob.WithContext(ctx).
			Where(cronJob.ID.Eq(jobID), cronJob.Revision.Eq(expectedRevision), cronJob.SessionID.Eq(sessionID)).
			Where(cronOwnerConditions(ctx, cronJob.TableName())...).
			Delete()
		if err != nil {
			return err
		}
		if info.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	})
}

func jobToModel(ctx context.Context, job Job) store.CronJob {
	return store.CronJob{
		ID: job.ID, SessionID: job.SessionID, Name: job.Name, Prompt: job.Prompt, Schedule: job.Schedule, Timezone: job.Timezone,
		Enabled: job.Enabled, TimeoutMS: job.Timeout.Milliseconds(), ModelProfileID: job.ModelProfileID,
		CreatedAt: timeMS(job.CreatedAt), UpdatedAt: timeMS(job.UpdatedAt), Revision: job.Revision, NextRunAt: timeMS(job.NextRunAt),
		RetryAt: optionalTimeMS(job.RetryAt), CircuitOpenUntil: optionalTimeMS(job.CircuitOpenUntil),
		ConsecutiveFailures: int64(job.ConsecutiveFailures), LastRunAt: optionalTimeMS(job.LastRunAt), LastScheduledFor: optionalTimeMS(job.LastScheduledFor),
		LastCoalesced: job.LastCoalesced, LastError: job.LastError, LeaseOwner: job.Lease.Owner, LeaseExpiresAt: optionalTimeMS(job.Lease.ExpiresAt),
		RunID: job.Run.ID, RunScheduledFor: optionalTimeMS(job.Run.ScheduledFor), RunStartedAt: optionalTimeMS(job.Run.StartedAt),
		RunDeadline: optionalTimeMS(job.Run.Deadline), RunCoalesced: job.Run.Coalesced, OwnerID: ipc.OwnerID(ctx),
	}
}

func jobAssignments(job store.CronJob) map[string]any {
	return map[string]any{
		"session_id": job.SessionID, "name": job.Name, "prompt": job.Prompt, "schedule": job.Schedule, "timezone": job.Timezone,
		"enabled": job.Enabled, "timeout_ms": job.TimeoutMS, "model_profile_id": job.ModelProfileID,
		"created_at": job.CreatedAt, "updated_at": job.UpdatedAt, "revision": job.Revision, "next_run_at": job.NextRunAt,
		"retry_at": job.RetryAt, "circuit_open_until": job.CircuitOpenUntil, "consecutive_failures": job.ConsecutiveFailures,
		"last_run_at": job.LastRunAt, "last_scheduled_for": job.LastScheduledFor, "last_coalesced": job.LastCoalesced, "last_error": job.LastError,
		"lease_owner": job.LeaseOwner, "lease_expires_at": job.LeaseExpiresAt, "run_id": job.RunID,
		"run_scheduled_for": job.RunScheduledFor, "run_started_at": job.RunStartedAt, "run_deadline": job.RunDeadline,
		"run_coalesced": job.RunCoalesced, "owner_id": job.OwnerID,
	}
}

func jobFromModel(row *store.CronJob) Job {
	job := Job{
		ID: row.ID, SessionID: row.SessionID, Name: row.Name, Prompt: row.Prompt, Schedule: row.Schedule, Timezone: row.Timezone,
		Enabled: row.Enabled, Timeout: time.Duration(row.TimeoutMS) * time.Millisecond, ModelProfileID: row.ModelProfileID,
		CreatedAt: time.UnixMilli(row.CreatedAt).UTC(), UpdatedAt: time.UnixMilli(row.UpdatedAt).UTC(), Revision: row.Revision,
		NextRunAt: time.UnixMilli(row.NextRunAt).UTC(), RetryAt: nullTimeMS(row.RetryAt), CircuitOpenUntil: nullTimeMS(row.CircuitOpenUntil),
		ConsecutiveFailures: int(row.ConsecutiveFailures), LastRunAt: nullTimeMS(row.LastRunAt), LastScheduledFor: nullTimeMS(row.LastScheduledFor),
		LastCoalesced: row.LastCoalesced, LastError: row.LastError,
		Lease: Lease{Owner: row.LeaseOwner}, Run: RunState{ID: row.RunID},
	}
	if job.Lease.Owner != "" {
		job.Lease.ExpiresAt = nullTimeMS(row.LeaseExpiresAt)
	}
	if job.Run.ID != "" {
		job.Run.ScheduledFor = nullTimeMS(row.RunScheduledFor)
		job.Run.StartedAt = nullTimeMS(row.RunStartedAt)
		job.Run.Deadline = nullTimeMS(row.RunDeadline)
		job.Run.Coalesced = row.RunCoalesced
	}
	return job
}

func cronOwnerConditions(ctx context.Context, table string) []gen.Condition {
	if ownerID := ipc.OwnerID(ctx); ownerID != "" {
		return []gen.Condition{field.NewString(table, "owner_id").Eq(ownerID)}
	}
	return nil
}

func timeMS(value time.Time) int64 {
	return value.UnixMilli()
}

func optionalTimeMS(value time.Time) *int64 {
	if value.IsZero() {
		return nil
	}
	stamp := value.UnixMilli()
	return &stamp
}

func nullTimeMS(value *int64) time.Time {
	if value == nil {
		return time.Time{}
	}
	return time.UnixMilli(*value).UTC()
}
