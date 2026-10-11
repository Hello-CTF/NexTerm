package store

import (
	"context"
	"encoding/json"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/outcome"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

const OutcomeAuditKind = "outcome"

func (s *Store) OutcomeAuditor() outcome.Auditor {
	return outcomeAuditor{store: s}
}

type outcomeAuditor struct {
	store *Store
}

func (a outcomeAuditor) Append(ctx context.Context, record outcome.Record) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
	}
	input := AuditInput{Source: "ai", Kind: OutcomeAuditKind, Payload: json.RawMessage(payload)}
	if record.Result.ExitCode != nil {
		code := int32(*record.Result.ExitCode)
		input.ExitCode = &code
	}
	if record.StartedAt != nil && record.FinishedAt != nil {
		duration := record.FinishedAt.Sub(*record.StartedAt).Milliseconds()
		input.DurationMS = &duration
	}
	return a.store.AuditInsert(ctx, input)
}

func (s *Store) AuditInsert(ctx context.Context, input AuditInput) error {
	payload, err := json.Marshal(input.Payload)
	if err != nil {
		return ipc.WrapError(ipc.CodeInternal, "数据编码失败: "+err.Error(), err)
	}
	row := AuditRow{
		TS: ids.NowMS(), SessionID: input.SessionID, AssetID: input.AssetID, Source: input.Source, Kind: input.Kind,
		PayloadJSON: string(payload), ExitCode: input.ExitCode, DurationMS: input.DurationMS, OwnerID: ipc.OwnerID(ctx),
	}
	if err := query.Use(s.db).AuditRow.WithContext(ctx).Create(&row); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) AuditCount(ctx context.Context, filter AuditQuery) (int64, error) {
	audit := query.Use(s.db).AuditRow
	q := audit.WithContext(ctx).Where(ownerConditions(ctx, audit.TableName())...)
	if filter.SessionID != nil {
		q = q.Where(audit.SessionID.Eq(*filter.SessionID))
	}
	if filter.AssetID != nil {
		q = q.Where(audit.AssetID.Eq(*filter.AssetID))
	}
	if filter.Source != nil {
		q = q.Where(audit.Source.Eq(*filter.Source))
	}
	if filter.Kind != nil {
		q = q.Where(audit.Kind.Eq(*filter.Kind))
	}
	count, err := q.Count()
	if err != nil {
		return 0, dbError(err)
	}
	return count, nil
}

func (s *Store) AuditQuery(ctx context.Context, filter AuditQuery) ([]AuditRow, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	audit := query.Use(s.db).AuditRow
	q := audit.WithContext(ctx).Where(ownerConditions(ctx, audit.TableName())...)
	if filter.SessionID != nil {
		q = q.Where(audit.SessionID.Eq(*filter.SessionID))
	}
	if filter.AssetID != nil {
		q = q.Where(audit.AssetID.Eq(*filter.AssetID))
	}
	if filter.Source != nil {
		q = q.Where(audit.Source.Eq(*filter.Source))
	}
	if filter.Kind != nil {
		q = q.Where(audit.Kind.Eq(*filter.Kind))
	}
	result := []AuditRow{}
	err := q.Order(audit.TS.Desc(), audit.ID.Desc()).
		Limit(int(limit)).
		Offset(int(filter.Offset)).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}
