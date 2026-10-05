package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ProbiusOfficial/NexTerm/internal/ai/hitl"
	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/adk"
)

const RunStatusSuperseded = "superseded"

const editResendJobWait = 5 * time.Second

type maxIterationsError struct{ limit int }

func (e *maxIterationsError) Error() string {
	return fmt.Sprintf("AI 任务达到最大迭代次数（%d），已终止", e.limit)
}

type messageTruncateStore interface {
	MsgTruncateAfter(ctx context.Context, conversationID, messageID string) error
}

func (r *Runner) EditResend(ctx context.Context, conversationID, messageID string) error {
	if r.store == nil {
		return errors.New("AI 会话存储未配置")
	}
	truncater, ok := r.store.(messageTruncateStore)
	if !ok {
		return errors.New("AI 会话存储不支持消息截断")
	}
	unlock := r.lockConversation(conversationID)
	defer unlock()
	rows, targetIndex, err := r.editTarget(ctx, conversationID, messageID)
	if err != nil {
		return err
	}
	affected, err := r.editAffectedRuns(ctx, conversationID, rows, targetIndex)
	if err != nil {
		return err
	}
	if err := r.cancelAndWaitJobs(ctx, conversationID, affected); err != nil {
		return err
	}
	var errs []error
	for _, run := range affected {
		if err := r.supersedeRun(ctx, run.ID); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if editResendTestHook != nil {
		editResendTestHook()
	}
	return truncater.MsgTruncateAfter(ctx, conversationID, messageID)
}

var editResendTestHook func()

func (r *Runner) editTarget(ctx context.Context, conversationID, messageID string) ([]store.MessageRow, int, error) {
	rows, err := r.store.MsgList(ctx, conversationID)
	if err != nil {
		return nil, 0, err
	}
	for index, row := range rows {
		if row.ID != messageID {
			continue
		}
		var persisted historyRow
		if json.Unmarshal([]byte(row.ContentJSON), &persisted) != nil {
			return nil, 0, errors.New("消息内容损坏")
		}
		role := persisted.Role
		if role == "" {
			role = row.Role
		}
		if role != "user" {
			return nil, 0, errors.New("只能编辑用户消息")
		}
		return rows, index, nil
	}
	return nil, 0, errors.New("消息不存在")
}

func (r *Runner) editAffectedRuns(ctx context.Context, conversationID string, rows []store.MessageRow, targetIndex int) ([]store.RunRow, error) {
	if r.runs == nil {
		return nil, nil
	}
	runs, err := r.runs.RunList(ctx, conversationID, 0)
	if err != nil {
		return nil, err
	}
	anchor := make(map[string]int, len(runs))
	for index, row := range rows {
		var persisted historyRow
		if json.Unmarshal([]byte(row.ContentJSON), &persisted) != nil || persisted.JobID == "" {
			continue
		}
		if _, ok := anchor[persisted.JobID]; !ok {
			anchor[persisted.JobID] = index
		}
	}
	affected := make([]store.RunRow, 0, len(runs))
	for _, run := range runs {
		if index, ok := anchor[run.ID]; ok {
			if index >= targetIndex {
				affected = append(affected, run)
			}
			continue
		}
		// 无锚点消息的历史 run 按创建时间兜底
		if run.CreatedAt > rows[targetIndex].CreatedAt {
			affected = append(affected, run)
		}
	}
	return affected, nil
}

func (r *Runner) cancelAndWaitJobs(ctx context.Context, conversationID string, affected []store.RunRow) error {
	ids := make([]string, 0, len(affected))
	seen := make(map[string]struct{}, len(affected))
	for _, run := range affected {
		ids = append(ids, run.ID)
		seen[run.ID] = struct{}{}
	}
	r.mu.Lock()
	for id, current := range r.jobs {
		if current.args.ConversationID != conversationID {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		ids = append(ids, id)
	}
	r.mu.Unlock()
	var errs []error
	for _, id := range ids {
		if r.lookupJob(id) == nil {
			continue
		}
		if err := r.Cancel(id); err != nil && !errors.Is(err, ErrJobNotFound) {
			errs = append(errs, fmt.Errorf("取消 AI 任务 %s: %w", id, err))
		}
	}
	deadline := time.Now().Add(editResendJobWait)
	for {
		pending := ""
		for _, id := range ids {
			if r.lookupJob(id) != nil {
				pending = id
				break
			}
		}
		if pending == "" {
			return errors.Join(errs...)
		}
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("等待 AI 任务 %s 停止: %w", pending, err))
			return errors.Join(errs...)
		}
		if time.Now().After(deadline) {
			errs = append(errs, fmt.Errorf("等待 AI 任务 %s 停止超时", pending))
			return errors.Join(errs...)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *Runner) supersedeRun(ctx context.Context, runID string) error {
	fresh, err := r.runs.RunGet(ctx, runID)
	if err != nil {
		return fmt.Errorf("读取 AI 运行 %s: %w", runID, err)
	}
	var errs []error
	if fresh.FinishedAt == nil {
		if err := r.runs.RunFinishUsage(ctx, runID, RunStatusSuperseded, fresh.Answer, "", fresh.Turns, fresh.TokensIn, fresh.TokensOut, fresh.CacheCreationTokens, fresh.LatencyMS); err != nil {
			errs = append(errs, fmt.Errorf("标记 AI 运行 %s 已被替换: %w", runID, err))
		}
	} else {
		if err := r.runs.RunUpdateStatus(ctx, runID, RunStatusSuperseded); err != nil {
			errs = append(errs, fmt.Errorf("更新 AI 运行 %s 状态: %w", runID, err))
		}
	}
	if _, err := r.hitl.Cancel(runID); err != nil && !errors.Is(err, hitl.ErrRunNotFound) && !errors.Is(err, hitl.ErrRunFinished) {
		errs = append(errs, fmt.Errorf("结束 AI 任务 %s 交互状态: %w", runID, err))
	}
	if err := r.runs.HitlRunDelete(ctx, runID); err != nil {
		errs = append(errs, fmt.Errorf("清理 AI 任务 %s 交互记录: %w", runID, err))
	}
	if deleter, ok := r.checkpoints.(adk.CheckPointDeleter); ok {
		if err := deleter.Delete(context.Background(), runID); err != nil {
			errs = append(errs, fmt.Errorf("清理 AI 任务 %s 检查点: %w", runID, err))
		}
	}
	return errors.Join(errs...)
}
