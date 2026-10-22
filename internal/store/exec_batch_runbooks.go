package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

type ExecBatchRunHistory struct {
	RunbookID   string             `json:"runbookId"`
	RunID       string             `json:"runId"`
	Status      string             `json:"status"`
	ReachedStep int32              `json:"reachedStep"`
	StepCount   int32              `json:"stepCount"`
	Batches     []ExecBatchHistory `json:"batches"`
}

func (s *Store) ExecBatchRunHistoryGet(ctx context.Context, runID string) (ExecBatchRunHistory, error) {
	if runID == "" || len(runID) > 128 || strings.TrimSpace(runID) != runID {
		return ExecBatchRunHistory{}, badParam(fmt.Errorf("invalid runbook run id"))
	}
	batch := query.Use(s.db).ExecBatchRow
	rows := []ExecBatchRow{}
	if err := batch.WithContext(ctx).
		Where(batch.RunID.Eq(runID)).
		Where(execBatchHistoryOwnerConditions(ctx, batch.TableName())...).
		Order(batch.StepNumber.Asc(), batch.StartedAt.Asc(), batch.ID.Asc(), batch.OwnerID.Asc()).
		Scan(&rows); err != nil {
		return ExecBatchRunHistory{}, dbError(err)
	}
	if len(rows) == 0 {
		return ExecBatchRunHistory{}, notFound("Runbook 运行历史")
	}
	results, err := s.execBatchRunResults(ctx, rows)
	if err != nil {
		return ExecBatchRunHistory{}, err
	}
	history := ExecBatchRunHistory{
		RunbookID: rows[0].RunbookID, RunID: runID, StepCount: rows[0].StepCount,
		Status: ExecBatchStatusRunning, Batches: make([]ExecBatchHistory, 0, len(rows)),
	}
	completedGroups := make(map[int32]map[int32]bool)
	groupCounts := make(map[int32]int32)
	ownerID := rows[0].OwnerID
	running := false
	canceled := false
	failed := false
	for _, row := range rows {
		if row.OwnerID != ownerID || row.RunbookID != history.RunbookID || row.StepCount != history.StepCount ||
			row.StepNumber < 1 || row.StepNumber > row.StepCount || row.GroupNumber < 1 ||
			row.GroupCount < row.GroupNumber || row.GroupCount > 64 {
			return ExecBatchRunHistory{}, dbError(fmt.Errorf("inconsistent runbook history for run %s", runID))
		}
		if expected, exists := groupCounts[row.StepNumber]; exists && expected != row.GroupCount {
			return ExecBatchRunHistory{}, dbError(fmt.Errorf("inconsistent runbook group count for run %s", runID))
		}
		groupCounts[row.StepNumber] = row.GroupCount
		if row.StepNumber > history.ReachedStep {
			history.ReachedStep = row.StepNumber
		}
		terminal := true
		switch row.Status {
		case ExecBatchStatusRunning:
			running = true
			terminal = false
		case ExecBatchStatusCanceled:
			canceled = true
		case ExecBatchStatusFailed:
			failed = true
		case ExecBatchStatusSuccess:
		default:
			return ExecBatchRunHistory{}, dbError(fmt.Errorf("unknown runbook batch status %q", row.Status))
		}
		if terminal {
			if completedGroups[row.StepNumber] == nil {
				completedGroups[row.StepNumber] = make(map[int32]bool)
			}
			completedGroups[row.StepNumber][row.GroupNumber] = true
		}
		key := execBatchRunResultKey(row.OwnerID, row.ID)
		batchResults := results[key]
		if batchResults == nil {
			batchResults = []ExecBatchResultRow{}
		}
		history.Batches = append(history.Batches, ExecBatchHistory{Batch: row, Results: batchResults})
	}
	switch {
	case running:
		history.Status = ExecBatchStatusRunning
	case canceled:
		history.Status = ExecBatchStatusCanceled
	case failed:
		history.Status = ExecBatchStatusFailed
	case runHistoryComplete(groupCounts, completedGroups, history.StepCount, history.ReachedStep):
		history.Status = ExecBatchStatusSuccess
	}
	return history, nil
}

func runHistoryComplete(groupCounts map[int32]int32, completed map[int32]map[int32]bool, stepCount, reachedStep int32) bool {
	if reachedStep != stepCount {
		return false
	}
	for step := int32(1); step <= stepCount; step++ {
		expected := groupCounts[step]
		if expected < 1 || len(completed[step]) != int(expected) {
			return false
		}
		for group := int32(1); group <= expected; group++ {
			if !completed[step][group] {
				return false
			}
		}
	}
	return true
}

func (s *Store) execBatchRunResults(ctx context.Context, rows []ExecBatchRow) (map[string][]ExecBatchResultRow, error) {
	byOwner := make(map[string][]string)
	for _, row := range rows {
		byOwner[row.OwnerID] = append(byOwner[row.OwnerID], row.ID)
	}
	result := query.Use(s.db).ExecBatchResultRow
	output := make(map[string][]ExecBatchResultRow, len(rows))
	for ownerID, batchIDs := range byOwner {
		for start := 0; start < len(batchIDs); start += 64 {
			end := min(start+64, len(batchIDs))
			chunk := batchIDs[start:end]
			rows := []ExecBatchResultRow{}
			if err := result.WithContext(ctx).
				Where(result.OwnerID.Eq(ownerID), result.BatchID.In(chunk...)).
				Order(result.BatchID.Asc(), result.Position.Asc(), result.SessionID.Asc()).
				Scan(&rows); err != nil {
				return nil, dbError(err)
			}
			for _, item := range rows {
				key := execBatchRunResultKey(ownerID, item.BatchID)
				output[key] = append(output[key], item)
			}
		}
	}
	return output, nil
}

func execBatchRunResultKey(ownerID, batchID string) string {
	return ownerID + "\x00" + batchID
}
