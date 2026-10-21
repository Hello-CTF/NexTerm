package execbatch

import (
	"context"
	"fmt"
	"strings"
)

// RerunEligible reports whether a finished target status qualifies for a
// failed-target-only rerun. Successful targets are not rerun, and canceled
// targets are excluded on purpose: cancellation is a deliberate user action,
// not a failure to retry.
func RerunEligible(status Status) bool {
	switch status {
	case StatusExitError, StatusTransportError, StatusTimeout:
		return true
	default:
		return false
	}
}

// SelectRerunTargets returns the session IDs of the failed targets of a
// finished batch, preserving the original target order of the results. An
// empty result means the batch has no rerun candidates.
func SelectRerunTargets(results []Result) []string {
	sessionIDs := make([]string, 0, len(results))
	for _, result := range results {
		if RerunEligible(result.Status) {
			sessionIDs = append(sessionIDs, result.SessionID)
		}
	}
	return sessionIDs
}

// RerunRequest builds the rerun Request for the failed targets of a finished
// batch, preserving the command, timeout, and concurrency of the original
// request plus the caller's production confirmation, which must be fresh:
// the new batch ID and failed-only target set change the challenge hashes,
// so the original batch's confirmation never validates here. Runbook
// metadata is deliberately dropped: a manual rerun is not part of the
// original runbook run.
func RerunRequest(original Request, results []Result, newBatchID string) (Request, error) {
	if newBatchID == "" || len(newBatchID) > 128 || strings.TrimSpace(newBatchID) != newBatchID {
		return Request{}, fmt.Errorf("%w: invalid rerun batch id", ErrInvalid)
	}
	sessionIDs := SelectRerunTargets(results)
	if len(sessionIDs) == 0 {
		return Request{}, fmt.Errorf("%w: batch has no failed targets to rerun", ErrInvalid)
	}
	return Request{
		BatchID:                newBatchID,
		SessionIDs:             sessionIDs,
		Command:                original.Command,
		TimeoutMS:              original.TimeoutMS,
		MaxConcurrent:          original.MaxConcurrent,
		ProductionConfirmation: original.ProductionConfirmation,
	}, nil
}

// BatchExecutor executes a batch request; *Service satisfies it in
// production wiring.
type BatchExecutor interface {
	Execute(context.Context, string, Request) (Batch, error)
}

// ExecuteRerun reruns the failed targets of a finished batch through the
// given executor, reusing the service's production confirmation,
// cancellation, and history contracts unchanged.
func ExecuteRerun(ctx context.Context, executor BatchExecutor, ownerID string, original Request, results []Result, newBatchID string) (Batch, error) {
	if executor == nil {
		return Batch{}, fmt.Errorf("%w: no batch executor", ErrUnsupported)
	}
	request, err := RerunRequest(original, results, newBatchID)
	if err != nil {
		return Batch{}, err
	}
	return executor.Execute(ctx, ownerID, request)
}
