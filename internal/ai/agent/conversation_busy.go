package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// CodeConversationBusy marks a request refused because the conversation still
// has a run that is executing or waiting for a confirmation.
const CodeConversationBusy ipc.Code = "conversation_busy"

var (
	ErrConversationBusy       = ipc.NewError(CodeConversationBusy, "该对话仍有任务在运行或等待确认，请在任务结束后再压缩")
	ErrConversationRunBusy    = ipc.NewError(CodeConversationBusy, "该对话仍有任务在运行或等待确认，请先停止该任务或等待它结束后再发送")
	ErrConversationDeleteBusy = ipc.NewError(CodeConversationBusy, "该对话仍有任务在运行或等待确认，请先停止该任务或等待它结束后再删除")
)

// staleRunMessage records a run whose row stayed unfinished although no job or
// pending interaction exists for it any more, for example after a failed
// status write.
const staleRunMessage = "AI 任务已中断（运行状态未能正常保存），可重试"

// IsConversationBusy reports whether err refuses a request because the
// conversation still has an active run.
func IsConversationBusy(err error) bool {
	var appErr *ipc.Error
	return errors.As(err, &appErr) && appErr.Code == CodeConversationBusy
}

// conversationHasJob reports a run of the conversation that is running or
// parked on a confirmation.
func (r *Runner) conversationHasJob(conversationID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, current := range r.jobs {
		if current.args.ConversationID == conversationID && !current.settled.Load() {
			return true
		}
	}
	return false
}

// activeJob reports a loaded job that has not recorded its final status.
func (r *Runner) activeJob(jobID string) bool {
	current := r.lookupJob(jobID)
	return current != nil && !current.settled.Load()
}

// conversationBusy reports whether the conversation has a live run: a job in
// memory, or an unfinished run whose confirmation is still pending although
// the job is not loaded yet (restored after a restart). A running row without
// a job is stale, left by a failed status write; it is finished as
// interrupted so it no longer blocks the conversation. Callers hold the
// conversation lock.
func (r *Runner) conversationBusy(ctx context.Context, conversationID string) (bool, error) {
	if conversationID == "" {
		return false, nil
	}
	if r.conversationHasJob(conversationID) {
		return true, nil
	}
	if r.runs == nil {
		return false, nil
	}
	rows, err := r.unfinishedConversationRuns(ctx, conversationID)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if r.runLive(row) {
			return true, nil
		}
	}
	for _, row := range rows {
		r.finishStaleRun(ctx, row)
	}
	return false, nil
}

// runLive reports whether an unfinished run row still has a job in memory or
// an interaction waiting for an answer.
func (r *Runner) runLive(row store.RunRow) bool {
	if r.activeJob(row.ID) {
		return true
	}
	snapshot, err := r.hitl.Snapshot(row.ID)
	return err == nil && snapshot.Terminal == nil && len(snapshot.Pending) > 0
}

// staleRunningRow reports a running row whose job is gone. Every live run
// keeps its job in memory until its final status is written, and a restart
// finishes running rows, so such a row only remains after a failed write.
func (r *Runner) staleRunningRow(row store.RunRow) bool {
	return row.FinishedAt == nil && row.Status == store.RunStatusRunning && r.lookupJob(row.ID) == nil
}

// reconcileRunDTO reports a stale running row as the interrupted, retryable
// run it effectively is.
func (r *Runner) reconcileRunDTO(dto RunDTO, row store.RunRow) RunDTO {
	if !r.staleRunningRow(row) {
		return dto
	}
	finished := row.UpdatedAt
	dto.Status = store.RunStatusInterrupted
	dto.FinishedAt = &finished
	if dto.Error == "" {
		dto.Error = staleRunMessage
	}
	return dto
}

// finishStaleRun finishes a stale running row as interrupted. Interrupted
// rows are left alone: one without a pending interaction is either being
// finished by its restore watcher or waits for the next startup recovery.
func (r *Runner) finishStaleRun(ctx context.Context, row store.RunRow) {
	if !r.staleRunningRow(row) {
		return
	}
	if _, ok := r.atomicFinisher(); !ok {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := r.finishRecoveredRun(writeCtx, row, store.RunStatusInterrupted, staleRunMessage, true); err != nil {
		slog.Warn("AI 过期运行状态修复失败", "run", row.ID, "error", err)
	}
}

func (r *Runner) unfinishedConversationRuns(ctx context.Context, conversationID string) ([]store.RunRow, error) {
	statuses := []string{store.RunStatusRunning, store.RunStatusInterrupted}
	if pager, ok := r.runs.(activeRunPager); ok {
		var runs []store.RunRow
		var cursor *store.RunCursor
		for {
			page, err := pager.RunPage(ctx, store.RunPageFilter{ConversationID: conversationID, Statuses: statuses, UnfinishedOnly: true, After: cursor, Limit: runRecoveryPageSize})
			if err != nil {
				return nil, err
			}
			runs = append(runs, page.Runs...)
			if page.NextCursor == nil {
				return runs, nil
			}
			cursor = page.NextCursor
		}
	}
	rows, err := r.runs.RunList(ctx, conversationID, 0)
	if err != nil {
		return nil, err
	}
	runs := make([]store.RunRow, 0, len(rows))
	for _, row := range rows {
		if row.FinishedAt == nil && (row.Status == store.RunStatusRunning || row.Status == store.RunStatusInterrupted) {
			runs = append(runs, row)
		}
	}
	return runs, nil
}

// DeleteConversation runs remove while the conversation is locked and has no
// active run, so a run never outlives its conversation's rows.
func (r *Runner) DeleteConversation(ctx context.Context, conversationID string, remove func(context.Context) error) error {
	unlock := r.lockConversation(conversationID)
	defer unlock()
	busy, err := r.conversationBusy(ctx, conversationID)
	if err != nil {
		return err
	}
	if busy {
		return ErrConversationDeleteBusy
	}
	return remove(ctx)
}
