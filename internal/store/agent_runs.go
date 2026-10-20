package store

import (
	"context"
	"errors"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gorm"
)

// RunCursor is a keyset pagination position in the stable run order
// (created_at DESC, id DESC). Writes never change a row's created_at or
// ID, so paging stays consistent while rows are being updated.
type RunCursor struct {
	CreatedAt int64  `json:"createdAt"`
	ID        string `json:"id"`
}

// RunPageFilter selects a page of runs. Title-source bookkeeping runs are
// always excluded, matching RunList. An empty Statuses list keeps every
// status; After pages forward from the cursor; UnfinishedOnly keeps only
// rows with no finish timestamp (the active set).
type RunPageFilter struct {
	ConversationID string
	Statuses       []string
	UnfinishedOnly bool
	After          *RunCursor
	Limit          int
}

// RunPage is one page of runs plus the cursor for the next page, nil when
// the page is the last one.
type RunPage struct {
	Runs       []RunRow
	NextCursor *RunCursor
}

const (
	runPageDefaultLimit = 50
	runPageMaxLimit     = 200
)

// RunFinishUsageIfStatus finishes a run only when it is still unfinished
// AND still in the expected scanned status, reporting whether the
// transition actually applied. The status guard makes the transition
// atomic against a concurrent resume: a run that moved on (resumed,
// completed, canceled) is never terminally written from a stale probe.
// A false result with a nil error means the row moved past the scanned
// state: the stored state stands and callers must account for it rather
// than claiming the write.
func (s *Store) RunFinishUsageIfStatus(ctx context.Context, runID, expectedStatus, status, answer, errMsg string, turns int, tokensIn, tokensOut, cacheCreationTokens, latencyMS int64) (bool, error) {
	if _, err := s.RunGet(ctx, runID); err != nil {
		return false, err
	}
	now := ids.NowMS()
	run := query.Use(s.db).RunRow
	info, err := run.WithContext(ctx).
		Where(run.ID.Eq(runID), run.Status.Eq(expectedStatus), run.FinishedAt.IsNull()).
		Where(s.aiHistoryOwnerConditions(ctx, run.TableName())...).
		UpdateColumns(map[string]any{
			"status": status, "answer": answer, "turns": turns, "tokens_in": tokensIn, "tokens_out": tokensOut,
			"cache_creation_tokens": cacheCreationTokens, "latency_ms": latencyMS, "error": errMsg,
			"updated_at": now, "finished_at": now,
		})
	if err != nil {
		return false, dbError(err)
	}
	if info.RowsAffected == 0 {
		if _, getErr := s.RunGet(ctx, runID); getErr != nil {
			return false, getErr
		}
		return false, nil
	}
	return true, nil
}

// RunFinishWithEventIfStatus atomically finishes a run under the
// expected-status guard and appends the terminal event in the same
// transaction. The event exists only when the conditional transition
// applies: a no-op or conflict appends nothing and leaves no partial
// terminal history.
func (s *Store) RunFinishWithEventIfStatus(ctx context.Context, runID, expectedStatus, status, answer, errMsg string, turns int, tokensIn, tokensOut, cacheCreationTokens, latencyMS int64, eventType string, eventPayload func(seq uint64) ([]byte, error)) (bool, error) {
	if _, err := s.RunGet(ctx, runID); err != nil {
		return false, err
	}
	now := ids.NowMS()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		run := queries.RunRow
		info, err := run.WithContext(ctx).
			Where(run.ID.Eq(runID), run.Status.Eq(expectedStatus), run.FinishedAt.IsNull()).
			Where(s.aiHistoryOwnerConditions(ctx, run.TableName())...).
			UpdateColumns(map[string]any{
				"status": status, "answer": answer, "turns": turns, "tokens_in": tokensIn, "tokens_out": tokensOut,
				"cache_creation_tokens": cacheCreationTokens, "latency_ms": latencyMS, "error": errMsg,
				"updated_at": now, "finished_at": now,
			})
		if err != nil {
			return dbError(err)
		}
		if info.RowsAffected == 0 {
			return errRunFinishNotApplied
		}
		if _, err := run.WithContext(ctx).
			Where(run.ID.Eq(runID)).
			Where(s.aiHistoryOwnerConditions(ctx, run.TableName())...).
			UpdateColumns(map[string]any{"seq": gorm.Expr("seq + 1"), "updated_at": now}); err != nil {
			return dbError(err)
		}
		row, err := run.WithContext(ctx).
			Where(run.ID.Eq(runID)).
			Where(s.aiHistoryOwnerConditions(ctx, run.TableName())...).
			First()
		if isNoRows(err) {
			return notFound("AI 运行")
		}
		if err != nil {
			return dbError(err)
		}
		encoded, err := eventPayload(row.Seq)
		if err != nil {
			return err
		}
		event := queries.RunEventRow
		if err := event.WithContext(ctx).Create(&RunEventRow{
			RunID: runID, Seq: row.Seq, Type: eventType, PayloadJSON: string(encoded), CreatedAt: now, OwnerID: row.OwnerID,
		}); err != nil {
			return dbError(err)
		}
		return nil
	})
	if errors.Is(err, errRunFinishNotApplied) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

var errRunFinishNotApplied = errors.New("run finish not applied")

// RunPage returns one keyset-paginated page of runs in the stable order
// (created_at DESC, id DESC), scoped by the same owner conditions as the
// rest of the AI history.
func (s *Store) RunPage(ctx context.Context, filter RunPageFilter) (RunPage, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = runPageDefaultLimit
	}
	if limit > runPageMaxLimit {
		limit = runPageMaxLimit
	}
	run := query.Use(s.db).RunRow
	q := run.WithContext(ctx).
		Where(run.Source.Neq(RunSourceTitle)).
		Where(s.aiHistoryOwnerConditions(ctx, run.TableName())...)
	if filter.ConversationID != "" {
		q = q.Where(run.ConversationID.Eq(filter.ConversationID))
	}
	if len(filter.Statuses) > 0 {
		q = q.Where(run.Status.In(filter.Statuses...))
	}
	if filter.UnfinishedOnly {
		q = q.Where(run.FinishedAt.IsNull())
	}
	db := q.UnderlyingDB()
	if filter.After != nil {
		db = db.Where(gorm.Expr(
			"(created_at < ?) OR (created_at = ? AND id < ?)",
			filter.After.CreatedAt, filter.After.CreatedAt, filter.After.ID,
		))
	}
	rows := []RunRow{}
	if err := db.Order("created_at DESC, id DESC").Limit(limit + 1).Scan(&rows).Error; err != nil {
		return RunPage{}, dbError(err)
	}
	page := RunPage{Runs: rows}
	if len(rows) > limit {
		page.Runs = rows[:limit]
		last := page.Runs[limit-1]
		page.NextCursor = &RunCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}
