package store

import (
	"context"
	"encoding/json"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

const (
	RunStatusRunning     = "running"
	RunStatusInterrupted = "interrupted"
	RunStatusCompleted   = "completed"
	RunStatusCanceled    = "canceled"
	RunStatusFailed      = "failed"
	RunStatusExpired     = "expired"
)

const RunSourceTitle = "title"

type RunErrorCounts struct {
	Retries  int64
	Failures int64
}

type RunRow = model.RunRow

type RunUsageSummaryRow struct {
	Source              string `json:"source"`
	ProfileID           string `json:"profileId"`
	Runs                int64  `json:"runs"`
	TokensIn            int64  `json:"tokensIn"`
	TokensOut           int64  `json:"tokensOut"`
	CacheCreationTokens int64  `json:"cacheCreationTokens"`
	AverageLatencyMS    int64  `json:"averageLatencyMs"`
}

type RunEventRow = model.RunEventRow

type HitlRunRow = model.HitlRunRow

type CheckpointRow = model.CheckpointRow

func (s *Store) RunInsert(ctx context.Context, run RunRow) error {
	if run.Source == "" {
		run.Source = "chat"
	}
	now := ids.NowMS()
	if run.CreatedAt == 0 {
		run.CreatedAt = now
	}
	if run.UpdatedAt == 0 {
		run.UpdatedAt = now
	}
	if _, err := s.ConvGet(ctx, run.ConversationID); err != nil {
		return err
	}
	run.OwnerID = ipc.OwnerID(ctx)
	if err := query.Use(s.db).RunRow.WithContext(ctx).Create(&run); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) RunGet(ctx context.Context, id string) (RunRow, error) {
	run := query.Use(s.db).RunRow
	row, err := run.WithContext(ctx).
		Where(run.ID.Eq(id)).
		Where(ownerConditions(ctx, run.TableName())...).
		First()
	if isNoRows(err) {
		return RunRow{}, notFound("AI 运行")
	}
	if err != nil {
		return RunRow{}, dbError(err)
	}
	return *row, nil
}

func (s *Store) RunList(ctx context.Context, conversationID string, limit int) ([]RunRow, error) {
	run := query.Use(s.db).RunRow
	q := run.WithContext(ctx).
		Where(run.Source.Neq(RunSourceTitle)).
		Where(ownerConditions(ctx, run.TableName())...)
	if conversationID != "" {
		q = q.Where(run.ConversationID.Eq(conversationID))
	}
	q = q.Order(run.CreatedAt.Desc(), run.ID.Desc())
	if limit > 0 {
		q = q.Limit(limit)
	}
	result := []RunRow{}
	if err := q.Scan(&result); err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func (s *Store) RunsActive(ctx context.Context) ([]RunRow, error) {
	run := query.Use(s.db).RunRow
	result := []RunRow{}
	err := run.WithContext(ctx).
		Where(run.Status.In(RunStatusRunning, RunStatusInterrupted)).
		Where(run.FinishedAt.IsNull()).
		Where(ownerConditions(ctx, run.TableName())...).
		Order(run.CreatedAt).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func (s *Store) RunAppendEvent(ctx context.Context, runID, eventType string, payload func(seq uint64) ([]byte, error)) (uint64, error) {
	if _, err := s.RunGet(ctx, runID); err != nil {
		return 0, err
	}
	var seq uint64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		run := queries.RunRow
		now := ids.NowMS()
		info, err := run.WithContext(ctx).
			Where(run.ID.Eq(runID)).
			Where(ownerConditions(ctx, run.TableName())...).
			UpdateColumns(map[string]any{"seq": gorm.Expr("seq + 1"), "updated_at": now})
		if err != nil {
			return dbError(err)
		}
		if info.RowsAffected == 0 {
			return notFound("AI 运行")
		}
		row, err := run.WithContext(ctx).
			Where(run.ID.Eq(runID)).
			Where(ownerConditions(ctx, run.TableName())...).
			First()
		if isNoRows(err) {
			return notFound("AI 运行")
		}
		if err != nil {
			return dbError(err)
		}
		seq = row.Seq
		encoded, err := payload(seq)
		if err != nil {
			return err
		}
		event := queries.RunEventRow
		if err := event.WithContext(ctx).Create(&RunEventRow{
			RunID: runID, Seq: seq, Type: eventType, PayloadJSON: string(encoded), CreatedAt: now, OwnerID: ipc.OwnerID(ctx),
		}); err != nil {
			return dbError(err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

func (s *Store) RunEventsAfter(ctx context.Context, runID string, afterSeq uint64) ([]RunEventRow, error) {
	return s.RunEventsAfterLimit(ctx, runID, afterSeq, 0)
}

func (s *Store) RunEventsAfterLimit(ctx context.Context, runID string, afterSeq uint64, limit int) ([]RunEventRow, error) {
	if _, err := s.RunGet(ctx, runID); err != nil {
		return nil, err
	}
	event := query.Use(s.db).RunEventRow
	q := event.WithContext(ctx).
		Where(event.RunID.Eq(runID), event.Seq.Gt(afterSeq)).
		Where(ownerConditions(ctx, event.TableName())...).
		Order(event.Seq)
	if limit > 0 {
		q = q.Limit(limit)
	}
	result := []RunEventRow{}
	if err := q.Scan(&result); err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func (s *Store) RunErrorCounts(ctx context.Context, runIDs []string) (map[string]RunErrorCounts, error) {
	result := make(map[string]RunErrorCounts, len(runIDs))
	if len(runIDs) == 0 {
		return result, nil
	}
	event := query.Use(s.db).RunEventRow
	rows := []RunEventRow{}
	err := event.WithContext(ctx).
		Where(event.Type.Eq("error"), event.RunID.In(runIDs...)).
		Where(ownerConditions(ctx, event.TableName())...).
		Scan(&rows)
	if err != nil {
		return nil, dbError(err)
	}
	for _, row := range rows {
		var event struct {
			Retryable bool `json:"retryable"`
		}
		if err := json.Unmarshal([]byte(row.PayloadJSON), &event); err != nil {
			return nil, dbError(err)
		}
		counts := result[row.RunID]
		if event.Retryable {
			counts.Retries++
		} else {
			counts.Failures++
		}
		result[row.RunID] = counts
	}
	return result, nil
}

func (s *Store) RunDelete(ctx context.Context, id string) error {
	if _, err := s.RunGet(ctx, id); err != nil {
		return err
	}
	run := query.Use(s.db).RunRow
	_, err := run.WithContext(ctx).
		Where(run.ID.Eq(id)).
		Where(ownerConditions(ctx, run.TableName())...).
		Delete()
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) RunUpdateStatus(ctx context.Context, runID, status string) error {
	if _, err := s.RunGet(ctx, runID); err != nil {
		return err
	}
	run := query.Use(s.db).RunRow
	info, err := run.WithContext(ctx).
		Where(run.ID.Eq(runID)).
		Where(ownerConditions(ctx, run.TableName())...).
		UpdateColumns(map[string]any{"status": status, "updated_at": ids.NowMS()})
	if err != nil {
		return dbError(err)
	}
	if info.RowsAffected == 0 {
		return notFound("AI 运行")
	}
	return nil
}

func (s *Store) RunFinish(ctx context.Context, runID, status, answer, errMsg string, turns int, tokensIn, tokensOut int64) error {
	return s.RunFinishUsage(ctx, runID, status, answer, errMsg, turns, tokensIn, tokensOut, 0, 0)
}

func (s *Store) RunFinishUsage(ctx context.Context, runID, status, answer, errMsg string, turns int, tokensIn, tokensOut, cacheCreationTokens, latencyMS int64) error {
	if _, err := s.RunGet(ctx, runID); err != nil {
		return err
	}
	now := ids.NowMS()
	run := query.Use(s.db).RunRow
	info, err := run.WithContext(ctx).
		Where(run.ID.Eq(runID), run.FinishedAt.IsNull()).
		Where(ownerConditions(ctx, run.TableName())...).
		UpdateColumns(map[string]any{
			"status": status, "answer": answer, "turns": turns, "tokens_in": tokensIn, "tokens_out": tokensOut,
			"cache_creation_tokens": cacheCreationTokens, "latency_ms": latencyMS, "error": errMsg,
			"updated_at": now, "finished_at": now,
		})
	if err != nil {
		return dbError(err)
	}
	if info.RowsAffected == 0 {
		if _, getErr := s.RunGet(ctx, runID); getErr != nil {
			return getErr
		}
	}
	return nil
}

func (s *Store) RunUsageSummary(ctx context.Context) ([]RunUsageSummaryRow, error) {
	run := query.Use(s.db).RunRow
	q := run.WithContext(ctx).
		Where(run.FinishedAt.IsNotNull()).
		Where(ownerConditions(ctx, run.TableName())...)
	result := []RunUsageSummaryRow{}
	err := q.UnderlyingDB().
		Select(`source, profile_id, COUNT(*) AS runs, COALESCE(SUM(tokens_in), 0) AS tokens_in, COALESCE(SUM(tokens_out), 0) AS tokens_out, COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens, CAST(ROUND(COALESCE(AVG(latency_ms), 0)) AS INTEGER) AS average_latency_ms`).
		Group("source, profile_id").
		Order("source, profile_id").
		Scan(&result).Error
	if err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func (s *Store) CheckpointGet(ctx context.Context, id string) ([]byte, bool, error) {
	checkpoint := query.Use(s.db).CheckpointRow
	row, err := checkpoint.WithContext(ctx).
		Where(checkpoint.ID.Eq(id)).
		Where(ownerConditions(ctx, checkpoint.TableName())...).
		First()
	if isNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, dbError(err)
	}
	return append([]byte(nil), row.Data...), true, nil
}

func (s *Store) CheckpointSet(ctx context.Context, id string, data []byte) error {
	row := CheckpointRow{ID: id, Data: data, UpdatedAt: ids.NowMS(), OwnerID: ipc.OwnerID(ctx)}
	return s.upsert(ctx, &row, []string{"data", "updated_at"})
}

func (s *Store) CheckpointDelete(ctx context.Context, id string) error {
	checkpoint := query.Use(s.db).CheckpointRow
	_, err := checkpoint.WithContext(ctx).
		Where(checkpoint.ID.Eq(id)).
		Where(ownerConditions(ctx, checkpoint.TableName())...).
		Delete()
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) HitlRunSave(ctx context.Context, id string, data []byte) error {
	row := HitlRunRow{ID: id, Data: data, UpdatedAt: ids.NowMS(), OwnerID: ipc.OwnerID(ctx)}
	return s.upsert(ctx, &row, []string{"data", "updated_at"})
}

func (s *Store) HitlRunList(ctx context.Context) ([]HitlRunRow, error) {
	hitl := query.Use(s.db).HitlRunRow
	result := []HitlRunRow{}
	err := hitl.WithContext(ctx).
		Where(ownerConditions(ctx, hitl.TableName())...).
		Order(hitl.UpdatedAt).
		Scan(&result)
	if err != nil {
		return nil, dbError(err)
	}
	for index := range result {
		result[index].Data = append([]byte(nil), result[index].Data...)
	}
	return result, nil
}

func (s *Store) HitlRunDelete(ctx context.Context, id string) error {
	hitl := query.Use(s.db).HitlRunRow
	_, err := hitl.WithContext(ctx).
		Where(hitl.ID.Eq(id)).
		Where(ownerConditions(ctx, hitl.TableName())...).
		Delete()
	if err != nil {
		return dbError(err)
	}
	return nil
}
