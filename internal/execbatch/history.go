package execbatch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

type History interface {
	ExecBatchHistorySave(context.Context, store.ExecBatchRow, []store.ExecBatchResultRow) error
}

func (s *Service) startHistory(ctx context.Context, ownerID string, request Request, started time.Time, targets []Target) {
	if s.history == nil {
		return
	}
	row := historyBatchRow(request, started, targets, store.ExecBatchStatusRunning, nil)
	s.saveHistory(ctx, ownerID, row, nil)
}

func (s *Service) finishWithHistory(ctx context.Context, ownerID string, request Request, started time.Time, targets []Target, results []Result) Batch {
	batch := s.finishedBatch(request, started, results)
	if s.history == nil {
		return batch
	}
	finishedAt := batch.FinishedAt
	row := historyBatchRow(request, started, targets, historyBatchStatus(results), &finishedAt)
	s.saveHistory(ctx, ownerID, row, historyResultRows(batch))
	return batch
}

func (s *Service) saveHistory(ctx context.Context, ownerID string, row store.ExecBatchRow, results []store.ExecBatchResultRow) {
	historyCtx, cancel := context.WithTimeout(execAuditContext(ctx, ownerID), 5*time.Second)
	defer cancel()
	if err := s.history.ExecBatchHistorySave(historyCtx, row, results); err != nil {
		s.onError(fmt.Errorf("record exec batch history %s: %w", row.ID, err))
	}
}

func historyBatchRow(request Request, started time.Time, targets []Target, status string, finishedAt *int64) store.ExecBatchRow {
	row := store.ExecBatchRow{
		ID: request.BatchID, CommandHash: commandDigest(request.Command), TargetSetHash: historyTargetSetHash(request.BatchID, targets),
		Status: status, TargetCount: int32(len(targets)), Production: historyProduction(targets),
		StartedAt: started.UnixMilli(), FinishedAt: finishedAt,
	}
	if request.Runbook != nil {
		row.RunbookID = request.Runbook.RunbookID
		row.RunID = request.Runbook.RunID
		row.StepNumber = request.Runbook.StepNumber
		row.StepCount = request.Runbook.StepCount
		row.GroupNumber = request.Runbook.GroupNumber
		row.GroupCount = request.Runbook.GroupCount
	}
	return row
}

func historyTargetSetHash(batchID string, targets []Target) string {
	allTargets := make([]ConfirmationTarget, 0, len(targets))
	for _, target := range targets {
		allTargets = append(allTargets, ConfirmationTarget{
			SessionID: target.SessionID, SessionName: target.SessionName, AssetID: target.AssetID,
			Hostname: target.Hostname, Environment: target.Environment,
		})
	}
	sort.Slice(allTargets, func(i, j int) bool {
		if allTargets[i].SessionID == allTargets[j].SessionID {
			return allTargets[i].AssetID < allTargets[j].AssetID
		}
		return allTargets[i].SessionID < allTargets[j].SessionID
	})
	canonical, _ := json.Marshal(struct {
		BatchID string               `json:"batchId"`
		Targets []ConfirmationTarget `json:"targets"`
	}{BatchID: batchID, Targets: allTargets})
	return digest("target-set-v2", canonical)
}

func historyProduction(targets []Target) bool {
	for _, target := range targets {
		if isProductionEnvironment(target.Environment) {
			return true
		}
	}
	return false
}

func historyBatchStatus(results []Result) string {
	failed := false
	for _, result := range results {
		if result.Status == StatusCanceled {
			return store.ExecBatchStatusCanceled
		}
		if result.Status != StatusSuccess {
			failed = true
		}
	}
	if failed {
		return store.ExecBatchStatusFailed
	}
	return store.ExecBatchStatusSuccess
}

func historyResultRows(batch Batch) []store.ExecBatchResultRow {
	rows := make([]store.ExecBatchResultRow, len(batch.Results))
	for index, result := range batch.Results {
		row := store.ExecBatchResultRow{
			SessionID: result.SessionID, Position: int32(index), SessionName: result.SessionName,
			AssetID: result.AssetID, Environment: result.environment, Status: string(result.Status), Signal: result.Signal,
			ErrorKind: historyErrorKind(result), DurationMS: result.DurationMS, Truncated: result.Truncated,
		}
		if result.ExitCode != nil {
			code := int32(*result.ExitCode)
			row.ExitCode = &code
		}
		if !result.startedAt.IsZero() {
			startedAt := result.startedAt.UnixMilli()
			row.StartedAt = &startedAt
		}
		finishedAt := batch.FinishedAt
		if !result.finishedAt.IsZero() {
			finishedAt = result.finishedAt.UnixMilli()
		}
		row.FinishedAt = &finishedAt
		rows[index] = row
	}
	return rows
}

func historyErrorKind(result Result) string {
	switch result.Status {
	case StatusSuccess:
		return ""
	case StatusExitError:
		return store.ExecBatchErrorRemoteExit
	case StatusTimeout:
		return store.ExecBatchErrorDeadlineExceeded
	case StatusCanceled:
		return store.ExecBatchErrorCanceled
	case StatusTransportError:
		if result.Error == base.ErrExitStatusMissing.Error() {
			return store.ExecBatchErrorExitStatusAbsent
		}
		return store.ExecBatchErrorTransport
	default:
		return store.ExecBatchErrorTransport
	}
}
