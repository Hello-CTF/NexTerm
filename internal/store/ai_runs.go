package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ProbiusOfficial/NexTerm/internal/ids"
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

type RunRow struct {
	ID                  string
	ConversationID      string
	Status              string
	Attempt             int64
	Seq                 uint64
	PlanMode            bool
	Source              string
	ProfileID           string
	Answer              string
	Turns               int
	TokensIn            int64
	TokensOut           int64
	CacheCreationTokens int64
	LatencyMS           int64
	Error               string
	CreatedAt           int64
	UpdatedAt           int64
	FinishedAt          *int64
}

type RunUsageSummaryRow struct {
	Source              string `json:"source"`
	ProfileID           string `json:"profileId"`
	Runs                int64  `json:"runs"`
	TokensIn            int64  `json:"tokensIn"`
	TokensOut           int64  `json:"tokensOut"`
	CacheCreationTokens int64  `json:"cacheCreationTokens"`
	AverageLatencyMS    int64  `json:"averageLatencyMs"`
}

type RunEventRow struct {
	RunID       string
	Seq         uint64
	Type        string
	PayloadJSON string
	CreatedAt   int64
}

type HitlRunRow struct {
	ID        string
	Data      []byte
	UpdatedAt int64
}

func scanRun(row rowScanner) (RunRow, error) {
	var result RunRow
	var planMode int
	err := row.Scan(&result.ID, &result.ConversationID, &result.Status, &result.Attempt, &result.Seq,
		&planMode, &result.Source, &result.ProfileID, &result.Answer, &result.Turns, &result.TokensIn, &result.TokensOut,
		&result.CacheCreationTokens, &result.LatencyMS, &result.Error, &result.CreatedAt, &result.UpdatedAt, &result.FinishedAt)
	result.PlanMode = planMode != 0
	return result, err
}

const runColumns = `id, conversation_id, status, attempt, seq, plan_mode, source, profile_id, answer, turns, tokens_in, tokens_out, cache_creation_tokens, latency_ms, error, created_at, updated_at, finished_at`

func (s *Store) RunInsert(ctx context.Context, run RunRow) error {
	planMode := 0
	if run.PlanMode {
		planMode = 1
	}
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_run(`+runColumns+`)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.ID, run.ConversationID, run.Status, run.Attempt, run.Seq, planMode, run.Source, run.ProfileID,
		run.Answer, run.Turns, run.TokensIn, run.TokensOut, run.CacheCreationTokens, run.LatencyMS, run.Error, run.CreatedAt, run.UpdatedAt, run.FinishedAt)
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) RunGet(ctx context.Context, id string) (RunRow, error) {
	row, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM ai_run WHERE id = ?`, id))
	if isNoRows(err) {
		return RunRow{}, notFound("AI 运行")
	}
	if err != nil {
		return RunRow{}, dbError(err)
	}
	return row, nil
}

func (s *Store) RunList(ctx context.Context, conversationID string, limit int) ([]RunRow, error) {
	query := `SELECT ` + runColumns + ` FROM ai_run WHERE source != ?`
	args := []any{RunSourceTitle}
	if conversationID != "" {
		query += ` AND conversation_id = ?`
		args = append(args, conversationID)
	}
	query += ` ORDER BY created_at DESC, id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []RunRow{}
	for rows.Next() {
		row, err := scanRun(rows)
		if err != nil {
			return nil, dbError(err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) RunsActive(ctx context.Context) ([]RunRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM ai_run WHERE status IN (?, ?) AND finished_at IS NULL ORDER BY created_at`, RunStatusRunning, RunStatusInterrupted)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []RunRow{}
	for rows.Next() {
		row, err := scanRun(rows)
		if err != nil {
			return nil, dbError(err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) RunAppendEvent(ctx context.Context, runID, eventType string, payload func(seq uint64) ([]byte, error)) (uint64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, dbError(err)
	}
	defer func() { _ = tx.Rollback() }()
	now := ids.NowMS()
	if _, err := tx.ExecContext(ctx, `UPDATE ai_run SET seq = seq + 1, updated_at = ? WHERE id = ?`, now, runID); err != nil {
		return 0, dbError(err)
	}
	var seq uint64
	if err := tx.QueryRowContext(ctx, `SELECT seq FROM ai_run WHERE id = ?`, runID).Scan(&seq); err != nil {
		if isNoRows(err) {
			return 0, notFound("AI 运行")
		}
		return 0, dbError(err)
	}
	encoded, err := payload(seq)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ai_run_event(run_id, seq, type, payload_json, created_at)
VALUES(?,?,?,?,?)`, runID, seq, eventType, string(encoded), now); err != nil {
		return 0, dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, dbError(err)
	}
	return seq, nil
}

func (s *Store) RunEventsAfter(ctx context.Context, runID string, afterSeq uint64) ([]RunEventRow, error) {
	return s.RunEventsAfterLimit(ctx, runID, afterSeq, 0)
}

func (s *Store) RunEventsAfterLimit(ctx context.Context, runID string, afterSeq uint64, limit int) ([]RunEventRow, error) {
	query := `SELECT run_id, seq, type, payload_json, created_at FROM ai_run_event
WHERE run_id = ? AND seq > ? ORDER BY seq`
	args := []any{runID, afterSeq}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []RunEventRow{}
	for rows.Next() {
		var row RunEventRow
		if err := rows.Scan(&row.RunID, &row.Seq, &row.Type, &row.PayloadJSON, &row.CreatedAt); err != nil {
			return nil, dbError(err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) RunErrorCounts(ctx context.Context, runIDs []string) (map[string]RunErrorCounts, error) {
	result := make(map[string]RunErrorCounts, len(runIDs))
	if len(runIDs) == 0 {
		return result, nil
	}
	query := `SELECT run_id, payload_json FROM ai_run_event WHERE type = 'error' AND run_id IN (?` + strings.Repeat(",?", len(runIDs)-1) + `)`
	args := make([]any, len(runIDs))
	for index, id := range runIDs {
		args[index] = id
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var runID string
		var payload string
		if err := rows.Scan(&runID, &payload); err != nil {
			return nil, dbError(err)
		}
		var event struct {
			Retryable bool `json:"retryable"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return nil, dbError(err)
		}
		counts := result[runID]
		if event.Retryable {
			counts.Retries++
		} else {
			counts.Failures++
		}
		result[runID] = counts
	}
	return result, rows.Err()
}

func (s *Store) RunDelete(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM ai_run WHERE id = ?`, id); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) RunUpdateStatus(ctx context.Context, runID, status string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE ai_run SET status = ?, updated_at = ? WHERE id = ?`, status, ids.NowMS(), runID)
	if err != nil {
		return dbError(err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return notFound("AI 运行")
	}
	return nil
}

func (s *Store) RunFinish(ctx context.Context, runID, status, answer, errMsg string, turns int, tokensIn, tokensOut int64) error {
	return s.RunFinishUsage(ctx, runID, status, answer, errMsg, turns, tokensIn, tokensOut, 0, 0)
}

func (s *Store) RunFinishUsage(ctx context.Context, runID, status, answer, errMsg string, turns int, tokensIn, tokensOut, cacheCreationTokens, latencyMS int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE ai_run SET status = ?, answer = ?, turns = ?, tokens_in = ?, tokens_out = ?, cache_creation_tokens = ?, latency_ms = ?, error = ?, updated_at = ?, finished_at = ?
WHERE id = ? AND finished_at IS NULL`,
		status, answer, turns, tokensIn, tokensOut, cacheCreationTokens, latencyMS, errMsg, ids.NowMS(), ids.NowMS(), runID)
	if err != nil {
		return dbError(err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		if _, getErr := s.RunGet(ctx, runID); getErr != nil {
			return getErr
		}
	}
	return nil
}

func (s *Store) RunUsageSummary(ctx context.Context) ([]RunUsageSummaryRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, profile_id, count(*), coalesce(sum(tokens_in), 0), coalesce(sum(tokens_out), 0), coalesce(sum(cache_creation_tokens), 0), CAST(round(coalesce(avg(latency_ms), 0)) AS INTEGER)
FROM ai_run WHERE finished_at IS NOT NULL GROUP BY source, profile_id ORDER BY source, profile_id`)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []RunUsageSummaryRow{}
	for rows.Next() {
		var row RunUsageSummaryRow
		if err := rows.Scan(&row.Source, &row.ProfileID, &row.Runs, &row.TokensIn, &row.TokensOut, &row.CacheCreationTokens, &row.AverageLatencyMS); err != nil {
			return nil, dbError(err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) CheckpointGet(ctx context.Context, id string) ([]byte, bool, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM ai_checkpoint WHERE id = ?`, id).Scan(&data)
	if isNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, dbError(err)
	}
	return append([]byte(nil), data...), true, nil
}

func (s *Store) CheckpointSet(ctx context.Context, id string, data []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_checkpoint(id, data, updated_at) VALUES(?,?,?)
ON CONFLICT(id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		id, data, ids.NowMS())
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) CheckpointDelete(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM ai_checkpoint WHERE id = ?`, id); err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) HitlRunSave(ctx context.Context, id string, data []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO ai_hitl_run(id, data, updated_at) VALUES(?,?,?)
ON CONFLICT(id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		id, data, ids.NowMS())
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) HitlRunList(ctx context.Context) ([]HitlRunRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, data, updated_at FROM ai_hitl_run ORDER BY updated_at`)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	result := []HitlRunRow{}
	for rows.Next() {
		var row HitlRunRow
		var data []byte
		if err := rows.Scan(&row.ID, &data, &row.UpdatedAt); err != nil {
			return nil, dbError(err)
		}
		row.Data = append([]byte(nil), data...)
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) HitlRunDelete(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM ai_hitl_run WHERE id = ?`, id); err != nil {
		return dbError(err)
	}
	return nil
}
