package outcome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen"
	"gorm.io/gen/field"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SQLiteStore struct {
	db *gorm.DB
}

func NewSQLiteStore(db *gorm.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, errors.New("outcome: db is required")
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Reserve(ctx context.Context, record Record) (Record, bool, error) {
	row := outcomeModel(ctx, record)
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "owner_id"}, {Name: "idempotence_key"}},
		DoNothing: true,
	}).Create(&row)
	if result.Error != nil {
		return Record{}, false, fmt.Errorf("outcome: reserve record: %w", result.Error)
	}
	if result.RowsAffected == 1 {
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
	row := outcomeModel(ctx, record)
	outcomeRecord := query.Use(s.db).OutcomeRecord
	info, err := outcomeRecord.WithContext(ctx).
		Where(outcomeRecord.IdempotenceKey.Eq(record.IdempotenceKey), outcomeRecord.Revision.Eq(expectedRevision)).
		Where(ownerConditions(ctx, outcomeRecord.TableName())...).
		UpdateColumns(map[string]any{
			"authorization_id": row.AuthorizationID, "kind": row.Kind, "canonical_arguments": row.CanonicalArgs,
			"state": row.State, "outcome": row.Outcome, "exit_code": row.ExitCode, "result_error": row.ResultError,
			"audit_state": row.AuditState, "audit_error": row.AuditError, "audit_completed_at": row.AuditCompletedAt,
			"revision": row.Revision, "created_at": row.CreatedAt, "started_at": row.StartedAt, "finished_at": row.FinishedAt,
		})
	if err != nil {
		return fmt.Errorf("outcome: update record: %w", err)
	}
	if info.RowsAffected == 1 {
		return nil
	}
	if _, err := s.Get(ctx, record.IdempotenceKey); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("outcome: update record: %w", err)
	}
	return ErrRevisionConflict
}

func (s *SQLiteStore) Get(ctx context.Context, key string) (Record, error) {
	outcomeRecord := query.Use(s.db).OutcomeRecord
	row, err := outcomeRecord.WithContext(ctx).
		Where(outcomeRecord.IdempotenceKey.Eq(key)).
		Where(ownerConditions(ctx, outcomeRecord.TableName())...).
		First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("outcome: get record: %w", err)
	}
	return outcomeFromModel(row), nil
}

func outcomeModel(ctx context.Context, record Record) model.OutcomeRecord {
	return model.OutcomeRecord{
		IdempotenceKey: record.IdempotenceKey, AuthorizationID: record.AuthorizationID, Kind: string(record.Kind),
		CanonicalArgs: string(record.CanonicalArguments), State: string(record.State), Outcome: string(record.Outcome),
		ExitCode: outcomeExitCode(record.Result.ExitCode), ResultError: record.Result.Error,
		AuditState: string(record.Audit.State), AuditError: record.Audit.Error, AuditCompletedAt: outcomeTimeMS(record.Audit.CompletedAt),
		Revision: record.Revision, CreatedAt: record.CreatedAt.UnixMilli(), StartedAt: outcomeTimeMS(record.StartedAt),
		FinishedAt: outcomeTimeMS(record.FinishedAt), OwnerID: ipc.OwnerID(ctx),
	}
}

func outcomeFromModel(row *model.OutcomeRecord) Record {
	record := Record{
		IdempotenceKey: row.IdempotenceKey, AuthorizationID: row.AuthorizationID, Kind: Kind(row.Kind),
		CanonicalArguments: json.RawMessage(row.CanonicalArgs), State: ExecutionState(row.State), Outcome: Outcome(row.Outcome),
		Result: ExitResult{Error: row.ResultError}, Audit: Audit{State: AuditState(row.AuditState), Error: row.AuditError},
		Revision: row.Revision, CreatedAt: time.UnixMilli(row.CreatedAt).UTC(),
	}
	if row.ExitCode != nil {
		code := int(*row.ExitCode)
		record.Result.ExitCode = &code
	}
	record.Audit.CompletedAt = outcomeTimeFromMS(row.AuditCompletedAt)
	record.StartedAt = outcomeTimeFromMS(row.StartedAt)
	record.FinishedAt = outcomeTimeFromMS(row.FinishedAt)
	return record
}

func outcomeExitCode(value *int) *int32 {
	if value == nil {
		return nil
	}
	code := int32(*value)
	return &code
}

func outcomeTimeMS(value *time.Time) *int64 {
	if value == nil {
		return nil
	}
	stamp := value.UnixMilli()
	return &stamp
}

func outcomeTimeFromMS(value *int64) *time.Time {
	if value == nil {
		return nil
	}
	stamp := time.UnixMilli(*value).UTC()
	return &stamp
}

func ownerConditions(ctx context.Context, table string) []gen.Condition {
	if ownerID := ipc.OwnerID(ctx); ownerID != "" {
		return []gen.Condition{field.NewString(table, "owner_id").Eq(ownerID)}
	}
	return nil
}
