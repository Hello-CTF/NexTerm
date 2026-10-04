package store

import (
	"context"
	"encoding/json"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
	"github.com/ProbiusOfficial/NexTerm/internal/ipc"
	"github.com/ProbiusOfficial/NexTerm/internal/outcome"
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
		return ipc.WrapError(ipc.CodeInternal, "内部错误: JSON: "+err.Error(), err)
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
		return ipc.WrapError(ipc.CodeInternal, "内部错误: JSON: "+err.Error(), err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_log(ts, session_id, asset_id, source, kind, payload_json, exit_code, duration_ms)
VALUES(?,?,?,?,?,?,?,?)`, ids.NowMS(), input.SessionID, input.AssetID, input.Source, input.Kind,
		string(payload), input.ExitCode, input.DurationMS)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) AuditQuery(ctx context.Context, query AuditQuery) ([]AuditRow, error) {
	limit := query.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, session_id, asset_id, source, kind, payload_json, exit_code, duration_ms
FROM audit_log
WHERE (? IS NULL OR session_id = ?) AND (? IS NULL OR asset_id = ?)
AND (? IS NULL OR source = ?) AND (? IS NULL OR kind = ?)
ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?`, query.SessionID, query.SessionID,
		query.AssetID, query.AssetID, query.Source, query.Source, query.Kind, query.Kind, limit, query.Offset)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []AuditRow{}
	for rows.Next() {
		var row AuditRow
		if err := rows.Scan(&row.ID, &row.TS, &row.SessionID, &row.AssetID, &row.Source,
			&row.Kind, &row.PayloadJSON, &row.ExitCode, &row.DurationMS); err != nil {
			return nil, dbError(err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
