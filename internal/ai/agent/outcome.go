package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ProbiusOfficial/NexTerm/internal/store"
	"github.com/cloudwego/eino/adk"
)

const RunStatusSuperseded = "superseded"

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
	rows, err := r.store.MsgList(ctx, conversationID)
	if err != nil {
		return err
	}
	var createdAt int64
	ownerRun := ""
	found := false
	for _, row := range rows {
		if row.ID != messageID {
			continue
		}
		var persisted historyRow
		if json.Unmarshal([]byte(row.ContentJSON), &persisted) != nil {
			return errors.New("消息内容损坏")
		}
		role := persisted.Role
		if role == "" {
			role = row.Role
		}
		if role != "user" {
			return errors.New("只能编辑用户消息")
		}
		createdAt = row.CreatedAt
		ownerRun = persisted.JobID
		found = true
		break
	}
	if !found {
		return errors.New("消息不存在")
	}
	if err := truncater.MsgTruncateAfter(ctx, conversationID, messageID); err != nil {
		return err
	}
	if r.runs == nil {
		return nil
	}
	runs, err := r.runs.RunList(ctx, conversationID, 0)
	if err != nil {
		return err
	}
	var owner *store.RunRow
	if ownerRun != "" {
		for index := range runs {
			if runs[index].ID == ownerRun {
				owner = &runs[index]
				break
			}
		}
	}
	for _, run := range runs {
		if !runSupersededByEdit(run, owner, createdAt) {
			continue
		}
		r.supersedeRun(ctx, run)
	}
	return nil
}

func runSupersededByEdit(run store.RunRow, owner *store.RunRow, messageCreatedAt int64) bool {
	if owner != nil {
		return run.CreatedAt > owner.CreatedAt || (run.CreatedAt == owner.CreatedAt && run.ID >= owner.ID)
	}
	return run.CreatedAt > messageCreatedAt
}

func (r *Runner) supersedeRun(ctx context.Context, run store.RunRow) {
	if run.FinishedAt == nil {
		_ = r.runs.RunFinishUsage(ctx, run.ID, RunStatusSuperseded, run.Answer, "", run.Turns, run.TokensIn, run.TokensOut, run.CacheCreationTokens, run.LatencyMS)
		if r.lookupJob(run.ID) != nil {
			_ = r.Cancel(run.ID)
		}
	} else {
		_ = r.runs.RunUpdateStatus(ctx, run.ID, RunStatusSuperseded)
	}
	_, _ = r.hitl.Cancel(run.ID)
	_ = r.runs.HitlRunDelete(ctx, run.ID)
	if deleter, ok := r.checkpoints.(adk.CheckPointDeleter); ok {
		_ = deleter.Delete(context.Background(), run.ID)
	}
}
