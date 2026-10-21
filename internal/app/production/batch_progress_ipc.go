package production

import (
	"context"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/execbatch"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// execBatchProgressRunner is the slice of the execbatch Service the progress
// and rerun commands need; *execbatch.Service satisfies it.
type execBatchProgressRunner interface {
	BatchProgress(ownerID, batchID string) (execbatch.BatchProgress, []execbatch.TargetProgress, bool)
	Rerun(ctx context.Context, ownerID string, original execbatch.Request, results []execbatch.Result, newBatchID string) (execbatch.Batch, error)
}

type execBatchProgressRequest struct {
	BatchID string `json:"batchId"`
}

type execBatchProgressSnapshot struct {
	Batch   execbatch.BatchProgress    `json:"batch"`
	Targets []execbatch.TargetProgress `json:"targets"`
}

type execBatchRerunResult struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
}

type execBatchRerunRequest struct {
	BatchID                string                            `json:"batchId"`
	Command                string                            `json:"command"`
	TimeoutMS              int64                             `json:"timeoutMs,omitempty"`
	MaxConcurrent          int                               `json:"maxConcurrent,omitempty"`
	Results                []execBatchRerunResult            `json:"results"`
	ProductionConfirmation *execbatch.ProductionConfirmation `json:"productionConfirmation,omitempty"`
}

func registerExecBatchProgressCommands(dispatcher *ipc.Dispatcher, batches execBatchProgressRunner) error {
	if err := ipc.Register(dispatcher, "exec_batch_progress", func(ctx context.Context, _ *ipc.Call, input execBatchProgressRequest) (execBatchProgressSnapshot, error) {
		if strings.TrimSpace(input.BatchID) == "" {
			return execBatchProgressSnapshot{}, ipc.NewError(ipc.CodeBadParam, "batchId 不能为空")
		}
		batch, targets, ok := batches.BatchProgress(ipc.OwnerID(ctx), input.BatchID)
		if !ok {
			return execBatchProgressSnapshot{}, ipc.NewError(ipc.CodeNotFound, "批量执行不存在或已结束")
		}
		return execBatchProgressSnapshot{Batch: batch, Targets: targets}, nil
	}); err != nil {
		return err
	}
	return ipc.Register(dispatcher, "exec_batch_rerun", func(ctx context.Context, _ *ipc.Call, input execBatchRerunRequest) (execbatch.Batch, error) {
		if strings.TrimSpace(input.BatchID) == "" {
			return execbatch.Batch{}, ipc.NewError(ipc.CodeBadParam, "batchId 不能为空")
		}
		if len(input.Results) == 0 {
			return execbatch.Batch{}, ipc.NewError(ipc.CodeBadParam, "results 不能为空")
		}
		results := make([]execbatch.Result, len(input.Results))
		for index, result := range input.Results {
			if strings.TrimSpace(result.SessionID) == "" || strings.TrimSpace(result.Status) == "" {
				return execbatch.Batch{}, ipc.NewError(ipc.CodeBadParam, "result 缺少 sessionId 或 status")
			}
			results[index] = execbatch.Result{SessionID: result.SessionID, Status: execbatch.Status(result.Status)}
		}
		original := execbatch.Request{
			Command: input.Command, TimeoutMS: input.TimeoutMS, MaxConcurrent: input.MaxConcurrent,
			ProductionConfirmation: input.ProductionConfirmation,
		}
		batch, err := batches.Rerun(ctx, ipc.OwnerID(ctx), original, results, input.BatchID)
		if err != nil {
			return execbatch.Batch{}, execBatchIPCError(err)
		}
		return batch, nil
	})
}
