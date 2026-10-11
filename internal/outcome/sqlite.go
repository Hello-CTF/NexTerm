package outcome

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, errors.New("outcome: db is required")
	}
	return &SQLiteStore{db: db}, nil
}

const outcomeColumns = `idempotence_key, authorization_id, kind, canonical_arguments, state, outcome,
	exit_code, result_error, audit_state, audit_error, audit_completed_at, revision, created_at, started_at, finished_at`

func (s *SQLiteStore) Reserve(ctx context.Context, record Record) (Record, bool, error) {
	result, err := s.db.ExecContext(ctx, `INSERT INTO outcome_record (owner_id, `+outcomeColumns+`)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(owner_id, idempotence_key) DO NOTHING`,
		ipc.OwnerID(ctx), record.IdempotenceKey, record.AuthorizationID, string(record.Kind), string(record.CanonicalArguments),
		string(record.State), string(record.Outcome), nullableInt(record.Result.ExitCode), record.Result.Error,
		string(record.Audit.State), record.Audit.Error, nullableTimeMS(record.Audit.CompletedAt), record.Revision,
		record.CreatedAt.UnixMilli(), nullableTimeMS(record.StartedAt), nullableTimeMS(record.FinishedAt))
	if err != nil {
		return Record{}, false, fmt.Errorf("outcome: reserve record: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Record{}, false, fmt.Errorf("outcome: reserve record: %w", err)
	}
	if affected == 1 {
		return cloneRecord(record), true, nil
	}
	stored, err := s.Get(ctx, record.IdempotenceKey)
	if err != nil {
		return Record{}, false, err
	}
	return stored, false, nil
}

func (s *SQLiteStore) Update(ctx context.Context, record Record, expectedRevision uint64) error {
	if record.Revision != expectedRevision+1 {
		return ErrRevisionConflict
	}
	condition, ownerArgs := ipc.OwnerCondition(ctx, "owner_id")
	args := []any{
		record.AuthorizationID, string(record.Kind), string(record.CanonicalArguments),
		string(record.State), string(record.Outcome), nullableInt(record.Result.ExitCode), record.Result.Error,
		string(record.Audit.State), record.Audit.Error, nullableTimeMS(record.Audit.CompletedAt), record.Revision,
		record.CreatedAt.UnixMilli(), nullableTimeMS(record.StartedAt), nullableTimeMS(record.FinishedAt),
		record.IdempotenceKey, expectedRevision,
	}
	args = append(args, ownerArgs...)
	result, err := s.db.ExecContext(ctx, `UPDATE outcome_record SET authorization_id=?, kind=?, canonical_arguments=?,
	state=?, outcome=?, exit_code=?, result_error=?, audit_state=?, audit_error=?, audit_completed_at=?,
	revision=?, created_at=?, started_at=?, finished_at=?
	WHERE idempotence_key = ? AND revision = ?`+condition, args...)
	if err != nil {
		return fmt.Errorf("outcome: update record: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("outcome: update record: %w", err)
	}
	if affected == 1 {
		return nil
	}
	var revision uint64
	condition, args = ipc.OwnerCondition(ctx, "owner_id")
	err = s.db.QueryRowContext(ctx, `SELECT revision FROM outcome_record WHERE idempotence_key = ?`+condition, append([]any{record.IdempotenceKey}, args...)...).Scan(&revision)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("outcome: update record: %w", err)
	default:
		return ErrRevisionConflict
	}
}

func (s *SQLiteStore) Get(ctx context.Context, key string) (Record, error) {
	condition, args := ipc.OwnerCondition(ctx, "owner_id")
	record, err := scanOutcomeRecord(s.db.QueryRowContext(ctx, `SELECT `+outcomeColumns+` FROM outcome_record WHERE idempotence_key = ?`+condition, append([]any{key}, args...)...))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("outcome: get record: %w", err)
	}
	return record, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanOutcomeRecord(row rowScanner) (Record, error) {
	var record Record
	var kind, state, outcomeValue, auditState string
	var canonical, resultError, auditError string
	var exitCode, auditCompletedAt, startedAt, finishedAt sql.NullInt64
	var createdAt int64
	err := row.Scan(&record.IdempotenceKey, &record.AuthorizationID, &kind, &canonical,
		&state, &outcomeValue, &exitCode, &resultError, &auditState, &auditError,
		&auditCompletedAt, &record.Revision, &createdAt, &startedAt, &finishedAt)
	if err != nil {
		return Record{}, err
	}
	record.Kind = Kind(kind)
	record.CanonicalArguments = json.RawMessage(canonical)
	record.State = ExecutionState(state)
	record.Outcome = Outcome(outcomeValue)
	record.Result = ExitResult{Error: resultError}
	if exitCode.Valid {
		code := int(exitCode.Int64)
		record.Result.ExitCode = &code
	}
	record.Audit = Audit{State: AuditState(auditState), Error: auditError}
	record.Audit.CompletedAt = timeFromMS(auditCompletedAt)
	record.CreatedAt = time.UnixMilli(createdAt).UTC()
	record.StartedAt = timeFromMS(startedAt)
	record.FinishedAt = timeFromMS(finishedAt)
	return record, nil
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableTimeMS(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UnixMilli()
}

func timeFromMS(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	stamp := time.UnixMilli(value.Int64).UTC()
	return &stamp
}
