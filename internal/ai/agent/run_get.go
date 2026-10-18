package agent

import (
	"context"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func (r *Runner) RunGet(ctx context.Context, jobID string) (RunDTO, error) {
	if r.runs == nil {
		return RunDTO{}, ipc.NewError(ipc.CodeUnsupported, "AI 会话存储未配置")
	}
	row, err := r.runs.RunGet(ctx, jobID)
	if err != nil {
		return RunDTO{}, err
	}
	counts, err := r.runs.RunErrorCounts(ctx, []string{jobID})
	if err != nil {
		return RunDTO{}, err
	}
	return RunDTO{
		ID: row.ID, ConversationID: row.ConversationID, Status: row.Status, Attempt: row.Attempt, Seq: row.Seq,
		PlanMode: row.PlanMode, Source: row.Source, ProfileID: row.ProfileID, Answer: row.Answer, Turns: row.Turns,
		TokensIn: row.TokensIn, TokensOut: row.TokensOut, CacheCreationTokens: row.CacheCreationTokens, LatencyMS: row.LatencyMS,
		Retries: counts[jobID].Retries, Failures: counts[jobID].Failures, Error: row.Error,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, FinishedAt: row.FinishedAt,
	}, nil
}
