package cron

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
)

type SQLStore struct {
	db *sql.DB
}

var _ Store = (*SQLStore)(nil)

func NewSQLiteStore(ctx context.Context, db *sql.DB) (*SQLStore, error) {
	return newSQLStore(ctx, db)
}

func NewPostgresStore(ctx context.Context, db *sql.DB) (*SQLStore, error) {
	return newSQLStore(ctx, db)
}

func newSQLStore(ctx context.Context, db *sql.DB) (*SQLStore, error) {
	if db == nil {
		return nil, errors.New("cron: sql store requires a database handle")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM cron_job`).Scan(&count); err != nil {
		return nil, fmt.Errorf("cron: cron_job table is missing or unreadable; apply the cron migration first: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) List(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM cron_job ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *SQLStore) Create(ctx context.Context, job Job, maxPerSession int) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM cron_job WHERE id = ?`, job.ID).Scan(&existing); err != nil {
		return Job{}, err
	}
	if existing > 0 {
		return Job{}, ErrConflict
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM cron_job WHERE session_id = ?`, job.SessionID).Scan(&count); err != nil {
		return Job{}, err
	}
	if count >= maxPerSession {
		return Job{}, ErrJobLimit
	}
	job.Revision = 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO cron_job (`+jobColumns+`) VALUES (`+jobPlaceholders+`)`,
		jobValues(job)...); err != nil {
		if store.IsUniqueErr(err) {
			return Job{}, ErrConflict
		}
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *SQLStore) CompareAndSwap(ctx context.Context, job Job, expectedRevision uint64) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var sessionID string
	var revision uint64
	err = tx.QueryRowContext(ctx, `SELECT session_id, revision FROM cron_job WHERE id = ?`, job.ID).Scan(&sessionID, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if revision != expectedRevision || sessionID != job.SessionID {
		return Job{}, ErrConflict
	}
	job.Revision = revision + 1

	values := append(jobValues(job)[1:], job.ID, expectedRevision, job.SessionID)
	result, err := tx.ExecContext(ctx, `UPDATE cron_job SET `+jobAssignments+` WHERE id = ? AND revision = ? AND session_id = ?`, values...)
	if err != nil {
		return Job{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Job{}, err
	} else if affected != 1 {
		return Job{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *SQLStore) Delete(ctx context.Context, sessionID, jobID string, expectedRevision uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var currentSession string
	var revision uint64
	err = tx.QueryRowContext(ctx, `SELECT session_id, revision FROM cron_job WHERE id = ?`, jobID).Scan(&currentSession, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if currentSession != sessionID {
		return ErrNotFound
	}
	if revision != expectedRevision {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM cron_job WHERE id = ? AND revision = ? AND session_id = ?`, jobID, expectedRevision, sessionID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

const jobColumns = `id, session_id, name, prompt, schedule, timezone, enabled, timeout_ms, model_profile_id,
	created_at, updated_at, revision, next_run_at, retry_at, circuit_open_until,
	consecutive_failures, last_run_at, last_scheduled_for, last_coalesced, last_error,
	lease_owner, lease_expires_at, run_id, run_scheduled_for, run_started_at, run_deadline, run_coalesced`

const jobPlaceholders = `?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?`

const jobAssignments = `session_id = ?, name = ?, prompt = ?, schedule = ?, timezone = ?, enabled = ?, timeout_ms = ?, model_profile_id = ?,
	created_at = ?, updated_at = ?, revision = ?, next_run_at = ?, retry_at = ?, circuit_open_until = ?,
	consecutive_failures = ?, last_run_at = ?, last_scheduled_for = ?, last_coalesced = ?, last_error = ?,
	lease_owner = ?, lease_expires_at = ?, run_id = ?, run_scheduled_for = ?, run_started_at = ?, run_deadline = ?, run_coalesced = ?`

func jobValues(job Job) []any {
	return []any{
		job.ID, job.SessionID, job.Name, job.Prompt, job.Schedule, job.Timezone, boolInt(job.Enabled), job.Timeout.Milliseconds(), job.ModelProfileID,
		timeMS(job.CreatedAt), timeMS(job.UpdatedAt), job.Revision, timeMS(job.NextRunAt), optionalTimeMS(job.RetryAt), optionalTimeMS(job.CircuitOpenUntil),
		job.ConsecutiveFailures, optionalTimeMS(job.LastRunAt), optionalTimeMS(job.LastScheduledFor), boolInt(job.LastCoalesced), job.LastError,
		job.Lease.Owner, optionalTimeMS(job.Lease.ExpiresAt), job.Run.ID, optionalTimeMS(job.Run.ScheduledFor), optionalTimeMS(job.Run.StartedAt), optionalTimeMS(job.Run.Deadline), boolInt(job.Run.Coalesced),
	}
}

type jobScanner interface {
	Scan(...any) error
}

func scanJob(scanner jobScanner) (Job, error) {
	var job Job
	var enabled, lastCoalesced, runCoalesced int
	var timeoutMS int64
	var createdAt, updatedAt, nextRunAt int64
	var retryAt, circuitOpenUntil, lastRunAt, lastScheduledFor sql.NullInt64
	var leaseExpiresAt, runScheduledFor, runStartedAt, runDeadline sql.NullInt64
	err := scanner.Scan(
		&job.ID, &job.SessionID, &job.Name, &job.Prompt, &job.Schedule, &job.Timezone, &enabled, &timeoutMS, &job.ModelProfileID,
		&createdAt, &updatedAt, &job.Revision, &nextRunAt, &retryAt, &circuitOpenUntil,
		&job.ConsecutiveFailures, &lastRunAt, &lastScheduledFor, &lastCoalesced, &job.LastError,
		&job.Lease.Owner, &leaseExpiresAt, &job.Run.ID, &runScheduledFor, &runStartedAt, &runDeadline, &runCoalesced,
	)
	if err != nil {
		return Job{}, err
	}
	job.Enabled = enabled != 0
	job.Timeout = time.Duration(timeoutMS) * time.Millisecond
	job.CreatedAt = time.UnixMilli(createdAt).UTC()
	job.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	job.NextRunAt = time.UnixMilli(nextRunAt).UTC()
	job.RetryAt = nullTimeMS(retryAt)
	job.CircuitOpenUntil = nullTimeMS(circuitOpenUntil)
	job.LastRunAt = nullTimeMS(lastRunAt)
	job.LastScheduledFor = nullTimeMS(lastScheduledFor)
	job.LastCoalesced = lastCoalesced != 0
	if job.Lease.Owner != "" {
		job.Lease.ExpiresAt = nullTimeMS(leaseExpiresAt)
	}
	if job.Run.ID != "" {
		job.Run.ScheduledFor = nullTimeMS(runScheduledFor)
		job.Run.StartedAt = nullTimeMS(runStartedAt)
		job.Run.Deadline = nullTimeMS(runDeadline)
		job.Run.Coalesced = runCoalesced != 0
	}
	return job, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func timeMS(value time.Time) int64 {
	return value.UnixMilli()
}

func optionalTimeMS(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UnixMilli()
}

func nullTimeMS(value sql.NullInt64) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return time.UnixMilli(value.Int64).UTC()
}
