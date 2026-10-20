package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/guard"
	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ai/steer"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ai/usage"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/adk"
)

func (r *Runner) RecoverRuns(ctx context.Context) error {
	if r.runs == nil {
		return nil
	}
	active, err := r.runs.RunsActive(ctx)
	if err != nil {
		return err
	}
	if len(active) == 0 {
		return nil
	}
	blobs, err := hitlStoreBridge{r.runs}.ListRuns(ctx)
	if err != nil {
		return err
	}
	byID := make(map[string]hitl.RunBlob, len(blobs))
	for _, blob := range blobs {
		byID[blob.ID] = blob
	}
	for _, row := range active {
		r.recoverRun(ctx, row, byID[row.ID])
	}
	for _, blob := range blobs {
		if _, err := r.runs.RunGet(ctx, blob.ID); err != nil {
			_ = r.runs.HitlRunDelete(ctx, blob.ID)
			if deleter, ok := r.checkpoints.(adk.CheckPointDeleter); ok {
				_ = deleter.Delete(context.Background(), blob.ID)
			}
		}
	}
	return nil
}

func (r *Runner) recoverRun(ctx context.Context, row store.RunRow, blob hitl.RunBlob) {
	restored := false
	var snapshot hitl.Snapshot
	if len(blob.Data) > 0 {
		if err := r.hitl.Restore(blob); err == nil || errors.Is(err, hitl.ErrRunExists) {
			if current, err := r.hitl.Snapshot(row.ID); err == nil {
				restored = true
				snapshot = current
			}
		}
	}
	if restored && snapshot.Terminal == nil && len(snapshot.Pending) > 0 {
		if row.Status != store.RunStatusInterrupted {
			_ = r.runs.RunUpdateStatus(ctx, row.ID, store.RunStatusInterrupted)
		}
		r.watchRestoredRun(ctx, row)
		return
	}
	status := store.RunStatusInterrupted
	message := "AI 任务因应用重启而中断"
	retryable := true
	if restored && snapshot.Terminal != nil && snapshot.Terminal.Reason == hitl.TerminalExpired {
		status = store.RunStatusExpired
		message = "AI 确认请求已过期，请重新发送"
		retryable = false
	}
	r.finishRecoveredRun(ctx, row.ID, status, message, retryable)
	if deleter, ok := r.checkpoints.(adk.CheckPointDeleter); ok {
		_ = deleter.Delete(context.WithoutCancel(ctx), row.ID)
	}
}

func (r *Runner) finishRecoveredRun(ctx context.Context, runID, status, message string, retryable bool) {
	eventType := "error"
	event := Event{Type: "error", Message: message, Retryable: retryable}
	if status == store.RunStatusCanceled {
		eventType = "canceled"
		event = Event{Type: "canceled", Message: message}
	}
	_, _ = r.runs.RunAppendEvent(ctx, runID, eventType, func(seq uint64) ([]byte, error) {
		event.Seq = seq
		return json.Marshal(event)
	})
	total := r.recoveredUsage(ctx, runID)
	_ = r.runs.RunFinishUsage(ctx, runID, status, "", message, 0,
		usage.SaturatingInt64(total.PromptTokens), usage.SaturatingInt64(total.CompletionTokens),
		usage.SaturatingInt64(total.CacheCreationTokens), total.LatencyMS)
}

func (r *Runner) recoveredUsage(ctx context.Context, runID string) usage.Usage {
	events, err := r.runs.RunEventsAfter(ctx, runID, 0)
	if err != nil {
		return usage.Usage{}
	}
	var total usage.Usage
	var latencyTotal uint64
	seenCallIDs := map[string]bool{}
	for _, row := range events {
		if row.Type != "usage" {
			continue
		}
		var payload struct {
			CallID              string `json:"callId"`
			Channel             string `json:"channel"`
			PromptTokens        uint64 `json:"promptTokens"`
			CompletionTokens    uint64 `json:"completionTokens"`
			CacheCreationTokens uint64 `json:"cacheCreationTokens"`
			LatencyMS           uint64 `json:"latencyMs"`
		}
		if err := json.Unmarshal([]byte(row.PayloadJSON), &payload); err != nil {
			continue
		}
		if agentOwnedUsageCallID(payload.Channel, payload.CallID) {
			if seenCallIDs[payload.CallID] {
				continue
			}
			seenCallIDs[payload.CallID] = true
		}
		total.Accumulate(usage.Usage{PromptTokens: payload.PromptTokens, CompletionTokens: payload.CompletionTokens, CacheCreationTokens: payload.CacheCreationTokens})
		latencyTotal = usage.SaturatingAdd(latencyTotal, payload.LatencyMS)
	}
	total.LatencyMS = usage.SaturatingInt64(latencyTotal)
	return total
}

func agentOwnedUsageCallID(channel, callID string) bool {
	switch channel {
	case "main", "summary", "tool-subagent":
		return strings.HasPrefix(callID, channel+":")
	default:
		return false
	}
}

func (r *Runner) watchRestoredRun(ctx context.Context, row store.RunRow) {
	done, err := r.hitl.Done(row.ID)
	if err != nil {
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		<-done
		snapshot, err := r.hitl.Snapshot(row.ID)
		if err != nil || snapshot.Terminal == nil {
			return
		}
		if r.lookupJob(row.ID) != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if current, err := r.runs.RunGet(ctx, row.ID); err == nil && current.FinishedAt != nil {
			return
		}
		status := store.RunStatusInterrupted
		message := "AI 任务已结束"
		switch snapshot.Terminal.Reason {
		case hitl.TerminalExpired:
			status = store.RunStatusExpired
			message = "AI 确认请求已过期，请重新发送"
		case hitl.TerminalCanceled:
			status = store.RunStatusCanceled
			message = "AI 任务已停止"
		case hitl.TerminalFailed:
			status = store.RunStatusFailed
			message = snapshot.Terminal.Message
		case hitl.TerminalCompleted:
			status = store.RunStatusCompleted
			message = ""
		}
		retryable := status == store.RunStatusFailed || status == store.RunStatusInterrupted
		r.finishRecoveredRun(ctx, row.ID, status, message, retryable)
	}()
}

type scopeResolutionError struct {
	err error
}

func (e *scopeResolutionError) Error() string { return e.err.Error() }
func (e *scopeResolutionError) Unwrap() error { return e.err }

func restoreJobError(err error) error {
	var resolutionErr *scopeResolutionError
	if errors.As(err, &resolutionErr) {
		return resolutionErr.err
	}
	return ErrJobNotFound
}

func (r *Runner) restoreJob(ctx context.Context, jobID string, factory StreamFactory) (*job, error) {
	if r.runs == nil {
		return nil, ErrJobNotFound
	}
	if current := r.lookupJob(jobID); current != nil {
		return current, nil
	}
	if err := r.validateExecutionRequest(ctx); err != nil {
		return nil, err
	}
	row, err := r.runs.RunGet(ctx, jobID)
	if err != nil || row.Status != store.RunStatusInterrupted {
		return nil, ErrJobNotFound
	}
	snapshot, err := r.hitl.Snapshot(jobID)
	if err != nil || snapshot.Terminal != nil || len(snapshot.Pending) == 0 {
		return nil, ErrJobNotFound
	}
	conversation, err := r.store.ConvGet(ctx, row.ConversationID)
	if err != nil {
		return nil, ErrJobNotFound
	}
	executionCtx := conversationExecutionContext(ctx, conversation.OwnerID)
	scope, err := scopeFromConversation(conversation.ScopeJSON)
	if err != nil {
		return nil, err
	}
	scope, err = r.resolveScope(executionCtx, row.ConversationID, scope)
	if err != nil {
		return nil, &scopeResolutionError{err: err}
	}
	unlock := r.lockConversation(row.ConversationID)
	defer unlock()
	stream := Stream(discardStream{})
	if factory != nil {
		opened, err := factory(ctx, "", jobID)
		if err != nil {
			return nil, err
		}
		if opened != nil {
			stream = opened
		}
	}
	jobContext, cancel := context.WithCancel(context.WithoutCancel(executionCtx))
	jobContext = tools.WithNotifyOwner(jobContext, conversation.OwnerID)
	deliveryContext, forceCancel := context.WithCancel(context.WithoutCancel(jobContext))
	current := &job{
		id:          jobID,
		ownerID:     conversation.OwnerID,
		args:        ChatArgs{ConversationID: row.ConversationID, PlanMode: row.PlanMode, Scope: scope, Source: row.Source, ModelProfileID: row.ProfileID},
		ctx:         jobContext,
		cancel:      cancel,
		deliveryCtx: deliveryContext,
		forceCancel: forceCancel,
		stream:      r.wrapStream(stream, jobID),
		memory:      guard.NewMemory(),
		steer:       steer.NewQueue(r.config.MaxPendingSteers),
		cancelSync:  newCancelSync(),
		doneCh:      make(chan struct{}),
	}
	if err := r.reserveJobID(jobID); err != nil {
		cancel()
		forceCancel()
		_ = stream.Close()
		return nil, ErrJobNotFound
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		r.releaseJobID(jobID)
		cancel()
		forceCancel()
		_ = stream.Close()
		return nil, ErrJobNotFound
	}
	if existing := r.jobs[jobID]; existing != nil {
		r.mu.Unlock()
		r.releaseJobID(jobID)
		cancel()
		forceCancel()
		_ = stream.Close()
		return existing, nil
	}
	r.jobs[jobID] = current
	r.wg.Add(1)
	r.mu.Unlock()
	recovered := r.recoveredUsage(context.Background(), jobID)
	if err := r.initializeEino(current); err != nil {
		r.mu.Lock()
		if r.jobs[jobID] == current {
			delete(r.jobs, jobID)
		}
		r.mu.Unlock()
		cancel()
		forceCancel()
		_ = current.stream.Close()
		r.releaseJobID(jobID)
		r.wg.Done()
		return nil, err
	}
	current.eino.addUsage(recovered)
	go r.watchHITL(current)
	return current, nil
}

func scopeFromConversation(scopeJSON string) (tools.Scope, error) {
	var envelope struct {
		Scope tools.Scope `json:"scope"`
	}
	if err := json.Unmarshal([]byte(scopeJSON), &envelope); err != nil {
		return tools.Scope{}, err
	}
	return envelope.Scope, nil
}

func (r *Runner) parkForShutdown(current *job) {
	current.completeOnce.Do(func() {
		_ = current.stream.Close()
		current.cancel()
		if current.forceCancel != nil {
			current.forceCancel()
		}
		current.pendingMu.Lock()
		manager := current.subagents
		current.pendingMu.Unlock()
		if manager != nil {
			if err := manager.Close(); err != nil {
				r.appendShutdownErr(err)
			}
		}
		r.releaseJobID(current.id)
		if r.config.Tools != nil {
			r.config.Tools.Release(current.id)
		}
		current.markDone()
		r.mu.Lock()
		if r.jobs[current.id] == current {
			delete(r.jobs, current.id)
		}
		r.mu.Unlock()
		r.wg.Done()
	})
}
