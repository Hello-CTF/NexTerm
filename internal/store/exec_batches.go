package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
	"gorm.io/gen"
	"gorm.io/gen/field"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ExecBatchStatusRunning  = "running"
	ExecBatchStatusSuccess  = "success"
	ExecBatchStatusFailed   = "failed"
	ExecBatchStatusCanceled = "canceled"
)

const (
	ExecBatchErrorRemoteExit       = "remote_exit"
	ExecBatchErrorDeadlineExceeded = "deadline_exceeded"
	ExecBatchErrorCanceled         = "canceled"
	ExecBatchErrorExitStatusAbsent = "exit_status_missing"
	ExecBatchErrorTransport        = "transport"
)

const (
	ExecBatchHistoryScopeSelf      = "self"
	ExecBatchHistoryScopeOwnerless = "ownerless"
)

type ExecBatchHistoryQuery struct {
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}

type ExecBatchHistoryItem struct {
	ID             string `json:"id"`
	CommandHash    string `json:"commandHash"`
	TargetSetHash  string `json:"targetSetHash"`
	Status         string `json:"status"`
	TargetCount    int32  `json:"targetCount"`
	Production     bool   `json:"production"`
	StartedAt      int64  `json:"startedAt"`
	FinishedAt     *int64 `json:"finishedAt"`
	RunbookID      string `json:"runbookId,omitempty"`
	RunID          string `json:"runId,omitempty"`
	StepNumber     int32  `json:"stepNumber,omitempty"`
	StepCount      int32  `json:"stepCount,omitempty"`
	GroupNumber    int32  `json:"groupNumber,omitempty"`
	GroupCount     int32  `json:"groupCount,omitempty"`
	RunbookVersion int32  `json:"runbookVersion,omitempty"`
	SyncOptIn      bool   `json:"syncOptIn"`
	OwnerScope     string `json:"ownerScope"`
}

type ExecBatchHistoryPage struct {
	Items  []ExecBatchHistoryItem `json:"items"`
	Total  int64                  `json:"total"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
}

type ExecBatchHistory struct {
	Batch   ExecBatchRow         `json:"batch"`
	Results []ExecBatchResultRow `json:"results"`
}

func (s *Store) ExecBatchHistorySave(ctx context.Context, row ExecBatchRow, results []ExecBatchResultRow) error {
	if err := validateExecBatchHistory(row, results); err != nil {
		return badParam(err)
	}
	row.OwnerID = ipc.OwnerID(ctx)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		queries := query.Use(tx)
		batch := queries.ExecBatchRow
		if err := batch.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "owner_id"}, {Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"command_hash", "target_set_hash", "status", "target_count", "production", "started_at", "finished_at",
				"runbook_id", "run_id", "step_number", "step_count", "group_number", "group_count",
			}),
		}).Create(&row); err != nil {
			return err
		}
		result := queries.ExecBatchResultRow
		if _, err := result.WithContext(ctx).
			Where(result.OwnerID.Eq(row.OwnerID), result.BatchID.Eq(row.ID)).
			Delete(); err != nil {
			return err
		}
		if len(results) == 0 {
			return nil
		}
		rows := make([]*ExecBatchResultRow, len(results))
		for index := range results {
			results[index].OwnerID = row.OwnerID
			results[index].BatchID = row.ID
			rows[index] = &results[index]
		}
		return result.WithContext(ctx).CreateInBatches(rows, 64)
	})
	if err != nil {
		return dbError(err)
	}
	return nil
}

func (s *Store) ExecBatchHistoryList(ctx context.Context, filter ExecBatchHistoryQuery) (ExecBatchHistoryPage, error) {
	limit := filter.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || filter.Offset < 0 {
		return ExecBatchHistoryPage{}, badParam(fmt.Errorf("invalid exec batch history pagination"))
	}
	batch := query.Use(s.db).ExecBatchRow
	owned := batch.WithContext(ctx).Where(execBatchHistoryOwnerConditions(ctx, batch.TableName())...)
	total, err := owned.Count()
	if err != nil {
		return ExecBatchHistoryPage{}, dbError(err)
	}
	rows := []ExecBatchRow{}
	if err := owned.Order(batch.StartedAt.Desc(), batch.ID.Desc(), batch.OwnerID.Desc()).
		Limit(limit).Offset(filter.Offset).Scan(&rows); err != nil {
		return ExecBatchHistoryPage{}, dbError(err)
	}
	items := make([]ExecBatchHistoryItem, len(rows))
	for index, row := range rows {
		items[index] = execBatchHistoryItem(row)
	}
	return ExecBatchHistoryPage{Items: items, Total: total, Limit: limit, Offset: filter.Offset}, nil
}

// ExecBatchStartupReconcile marks batches left running by a previous process as failed.
// It runs once at startup before the first retention pass: no batch can be genuinely
// active yet, and the finished timestamp makes the rows eligible for retention.
func (s *Store) ExecBatchStartupReconcile(ctx context.Context) (int64, error) {
	batch := query.Use(s.db).ExecBatchRow
	info, err := batch.WithContext(ctx).
		Where(batch.Status.Eq(ExecBatchStatusRunning), batch.FinishedAt.IsNull()).
		Updates(map[string]any{"status": ExecBatchStatusFailed, "finished_at": time.Now().UnixMilli()})
	if err != nil {
		return 0, dbError(err)
	}
	return info.RowsAffected, nil
}

func execBatchHistoryItem(row ExecBatchRow) ExecBatchHistoryItem {
	scope := ExecBatchHistoryScopeSelf
	if row.OwnerID == "" {
		scope = ExecBatchHistoryScopeOwnerless
	}
	return ExecBatchHistoryItem{
		ID: row.ID, CommandHash: row.CommandHash, TargetSetHash: row.TargetSetHash,
		Status: row.Status, TargetCount: row.TargetCount, Production: row.Production,
		StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
		RunbookID: row.RunbookID, RunID: row.RunID, StepNumber: row.StepNumber, StepCount: row.StepCount,
		GroupNumber: row.GroupNumber, GroupCount: row.GroupCount, RunbookVersion: row.RunbookVersion,
		SyncOptIn: row.SyncOptIn, OwnerScope: scope,
	}
}

func (s *Store) ExecBatchHistoryGet(ctx context.Context, batchID string, scopes ...string) (ExecBatchHistory, error) {
	if batchID == "" || len(batchID) > 128 || strings.TrimSpace(batchID) != batchID || len(scopes) > 1 {
		return ExecBatchHistory{}, badParam(fmt.Errorf("invalid exec batch history reference"))
	}
	scope := ExecBatchHistoryScopeSelf
	if len(scopes) == 1 && scopes[0] != "" {
		scope = scopes[0]
	}
	ownerID := ipc.OwnerID(ctx)
	switch scope {
	case ExecBatchHistoryScopeSelf:
	case ExecBatchHistoryScopeOwnerless:
		if ownerID != "" {
			role, ok := ipc.RoleFromContext(ctx)
			if !ok || role != "superadmin" {
				return ExecBatchHistory{}, notFound("批量执行历史")
			}
			ownerID = ""
		}
	default:
		return ExecBatchHistory{}, badParam(fmt.Errorf("invalid exec batch history owner scope"))
	}
	batch := query.Use(s.db).ExecBatchRow
	row, err := batch.WithContext(ctx).
		Where(batch.ID.Eq(batchID), batch.OwnerID.Eq(ownerID)).
		First()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ExecBatchHistory{}, notFound("批量执行历史")
	}
	if err != nil {
		return ExecBatchHistory{}, dbError(err)
	}
	result := query.Use(s.db).ExecBatchResultRow
	results := []ExecBatchResultRow{}
	if err := result.WithContext(ctx).
		Where(result.OwnerID.Eq(row.OwnerID), result.BatchID.Eq(row.ID)).
		Order(result.Position.Asc(), result.SessionID.Asc()).
		Scan(&results); err != nil {
		return ExecBatchHistory{}, dbError(err)
	}
	return ExecBatchHistory{Batch: *row, Results: results}, nil
}

func execBatchHistoryOwnerConditions(ctx context.Context, table string) []gen.Condition {
	ownerID := ipc.OwnerID(ctx)
	if ownerID == "" {
		return []gen.Condition{field.NewString(table, "owner_id").Eq("")}
	}
	if role, ok := ipc.RoleFromContext(ctx); ok && role == "superadmin" {
		return []gen.Condition{field.NewString(table, "owner_id").In(ownerID, "")}
	}
	return []gen.Condition{field.NewString(table, "owner_id").Eq(ownerID)}
}

func validateExecBatchHistory(row ExecBatchRow, results []ExecBatchResultRow) error {
	if row.ID == "" || len(row.ID) > 128 || strings.TrimSpace(row.ID) != row.ID {
		return fmt.Errorf("invalid exec batch id")
	}
	if !validExecBatchHash(row.CommandHash) || !validExecBatchHash(row.TargetSetHash) {
		return fmt.Errorf("invalid exec batch digest")
	}
	if row.TargetCount < 1 || row.TargetCount > 64 {
		return fmt.Errorf("invalid exec batch target count")
	}
	if row.RunbookID != "" || row.RunID != "" || row.StepNumber != 0 || row.StepCount != 0 || row.GroupNumber != 0 || row.GroupCount != 0 {
		if row.RunbookID == "" || len(row.RunbookID) > 128 || strings.TrimSpace(row.RunbookID) != row.RunbookID ||
			row.RunID == "" || len(row.RunID) > 128 || strings.TrimSpace(row.RunID) != row.RunID {
			return fmt.Errorf("invalid runbook history identity")
		}
		if row.StepNumber < 1 || row.StepCount < row.StepNumber || row.StepCount > 64 {
			return fmt.Errorf("invalid runbook history step")
		}
		if row.GroupNumber < 1 || row.GroupCount < row.GroupNumber || row.GroupCount > 64 {
			return fmt.Errorf("invalid runbook history command group")
		}
	}
	switch row.Status {
	case ExecBatchStatusRunning:
		if row.FinishedAt != nil || len(results) != 0 {
			return fmt.Errorf("running exec batch cannot have results or a finish time")
		}
	case ExecBatchStatusSuccess, ExecBatchStatusFailed, ExecBatchStatusCanceled:
		if row.FinishedAt == nil || len(results) != int(row.TargetCount) {
			return fmt.Errorf("finished exec batch requires all results and a finish time")
		}
	default:
		return fmt.Errorf("invalid exec batch status")
	}
	positions := make(map[int32]struct{}, len(results))
	sessions := make(map[string]struct{}, len(results))
	for _, result := range results {
		if result.SessionID == "" || len(result.SessionID) > 191 || result.Position < 0 || result.Position >= row.TargetCount {
			return fmt.Errorf("invalid exec batch result target")
		}
		if _, exists := positions[result.Position]; exists {
			return fmt.Errorf("duplicate exec batch result position")
		}
		if _, exists := sessions[result.SessionID]; exists {
			return fmt.Errorf("duplicate exec batch result session")
		}
		positions[result.Position] = struct{}{}
		sessions[result.SessionID] = struct{}{}
		switch result.Status {
		case "success", "exit_error", "transport_error", "timeout", "canceled":
		default:
			return fmt.Errorf("invalid exec batch result status")
		}
		switch result.ErrorKind {
		case "", ExecBatchErrorRemoteExit, ExecBatchErrorDeadlineExceeded, ExecBatchErrorCanceled,
			ExecBatchErrorExitStatusAbsent, ExecBatchErrorTransport:
		default:
			return fmt.Errorf("invalid exec batch error kind")
		}
		if len(result.Signal) > 64 || len(result.Environment) > 32 || len(result.SessionName) > 512 || len(result.AssetID) > 191 {
			return fmt.Errorf("exec batch result metadata is too large")
		}
		if result.DurationMS < 0 {
			return fmt.Errorf("invalid exec batch result duration")
		}
	}
	return nil
}

func validExecBatchHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
