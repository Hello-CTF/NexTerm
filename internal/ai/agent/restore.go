package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ai/hitl"
	"github.com/Hello-CTF/NexTerm/internal/ai/steer"
	"github.com/Hello-CTF/NexTerm/internal/ai/tools"
	"github.com/Hello-CTF/NexTerm/internal/ai/usage"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/cloudwego/eino/adk"
)

func (r *Runner) RecoverRuns(ctx context.Context) error {
	if r.runs == nil {
		return nil
	}
	// Recovery writes require the atomic expected-status event+finish
	// capability: without it the lifecycle guard is gone and a resumed
	// run could be terminally written, so the scan refuses visibly
	// instead of downgrading.
	if _, ok := r.atomicFinisher(); !ok {
		return ipc.NewError(ipc.CodeUnsupported, "AI 运行存储缺少条件完成能力")
	}
	active, err := r.activeRuns(ctx)
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
	var recoverErrs []error
	for _, row := range active {
		if err := r.recoverRun(ctx, row, byID[row.ID]); err != nil {
			recoverErrs = append(recoverErrs, err)
		}
	}
	for _, blob := range blobs {
		if _, err := r.runs.RunGet(ctx, blob.ID); err != nil {
			_ = r.runs.HitlRunDelete(ctx, blob.ID)
			if deleter, ok := r.checkpoints.(adk.CheckPointDeleter); ok {
				_ = deleter.Delete(context.Background(), blob.ID)
			}
		}
	}
	return errors.Join(recoverErrs...)
}

// activeRuns returns the owner's unfinished running and interrupted runs.
// When the store supports keyset paging it is used, so more than fifty
// active runs recover exactly like a handful and retained finished
// history is never refetched; otherwise it falls back to the legacy
// unpaged query.
func (r *Runner) activeRuns(ctx context.Context) ([]store.RunRow, error) {
	pager, ok := r.runs.(activeRunPager)
	if !ok {
		return r.runs.RunsActive(ctx)
	}
	var runs []store.RunRow
	var cursor *store.RunCursor
	for {
		page, err := pager.RunPage(ctx, store.RunPageFilter{
			Statuses:       []string{store.RunStatusRunning, store.RunStatusInterrupted},
			UnfinishedOnly: true,
			After:          cursor,
			Limit:          runRecoveryPageSize,
		})
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

func (r *Runner) recoverRun(ctx context.Context, row store.RunRow, blob hitl.RunBlob) error {
	restored := false
	var snapshot hitl.Snapshot
	if len(blob.Data) > 0 {
		err := r.hitl.Restore(blob)
		switch {
		case err == nil || errors.Is(err, hitl.ErrRunExists):
			if current, err := r.hitl.Snapshot(row.ID); err == nil {
				restored = true
				snapshot = current
			}
		case errors.Is(err, hitl.ErrInvalidArgument):
			// Explicitly unrecoverable input: terminalize below.
		default:
			// A retryable persistence failure: keep the active row, blob,
			// and checkpoint untouched for the next recovery and surface
			// the error instead of converting it into permanent data loss.
			return fmt.Errorf("AI 恢复运行 %s 失败: %w", row.ID, err)
		}
	}
	if restored && snapshot.Terminal == nil && len(snapshot.Pending) > 0 {
		if row.Status != store.RunStatusInterrupted {
			if err := r.runs.RunUpdateStatus(ctx, row.ID, store.RunStatusInterrupted); err != nil {
				slog.Warn("AI 恢复中断状态写入失败", "run", row.ID, "error", err)
			} else {
				// Carry the transition into the recovery state: the watch
				// and its terminal finish must expect the new status, not
				// the stale scanned one.
				row.Status = store.RunStatusInterrupted
			}
		}
		r.watchRestoredRun(ctx, row)
		return nil
	}
	status := store.RunStatusInterrupted
	message := "AI 任务因应用重启而中断"
	retryable := true
	if restored && snapshot.Terminal != nil && snapshot.Terminal.Reason == hitl.TerminalExpired {
		status = store.RunStatusExpired
		message = "AI 确认请求已过期，请重新发送"
		retryable = false
	}
	applied, err := r.finishRecoveredRun(ctx, row, status, message, retryable)
	if err != nil {
		return err
	}
	if !applied {
		return nil
	}
	if deleter, ok := r.checkpoints.(adk.CheckPointDeleter); ok {
		_ = deleter.Delete(context.WithoutCancel(ctx), row.ID)
	}
	return nil
}

func (r *Runner) finishRecoveredRun(ctx context.Context, row store.RunRow, status, message string, retryable bool) (bool, error) {
	eventType := "error"
	event := Event{Type: "error", Message: message, Retryable: retryable}
	if status == store.RunStatusCanceled {
		eventType = "canceled"
		event = Event{Type: "canceled", Message: message}
	}
	finisher, ok := r.atomicFinisher()
	if !ok {
		return false, ipc.NewError(ipc.CodeUnsupported, "AI 运行存储缺少条件完成能力")
	}
	total := r.recoveredUsage(ctx, row.ID)
	// The event and the conditional finish are one atomic operation: a
	// no-op appends nothing and leaves no partial terminal history.
	return finisher.RunFinishWithEventIfStatus(ctx, row.ID, row.Status, status, "", message, 0,
		usage.SaturatingInt64(total.PromptTokens), usage.SaturatingInt64(total.CompletionTokens),
		usage.SaturatingInt64(total.CacheCreationTokens), total.LatencyMS, eventType, func(seq uint64) ([]byte, error) {
			event.Seq = seq
			return json.Marshal(event)
		})
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
		if _, err := r.finishRecoveredRun(ctx, row, status, message, retryable); err != nil {
			slog.Warn("AI 恢复完成状态写入失败", "run", row.ID, "error", err)
		}
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

// restoreCallHandles rebuilds the run's call-ID→roster-handle mapping from
// its persisted toolCall journal events, so a resumed toolResult carries the
// same verified handle the original toolCall emitted. The mapping is never
// re-derived from the live target: an entry whose payload is corrupt or
// carries no handle leaves that call's handle unknown instead of guessing,
// and one call's handle is never used for another. A read failure degrades
// to all-unknown handles rather than failing the restore.
func (r *Runner) restoreCallHandles(ctx context.Context, current *job) {
	if r.runs == nil {
		return
	}
	rows, err := r.runs.RunEventsAfterLimit(ctx, current.id, 0, 0)
	if err != nil {
		return
	}
	for _, row := range rows {
		if row.Type != "toolCall" {
			continue
		}
		var event Event
		if err := json.Unmarshal(store.ParseJSONOr(row.PayloadJSON), &event); err != nil || event.ID == "" || event.Handle == "" {
			continue
		}
		current.rememberCallHandle(event.ID, event.Handle)
	}
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
		memory:      r.conversationMemory(conversation.OwnerID, row.ConversationID, scope),
		steer:       steer.NewQueue(r.config.MaxPendingSteers),
		cancelSync:  newCancelSync(),
		doneCh:      make(chan struct{}),
	}
	current.capture = r.newJobCapture(jobID, row.ConversationID)
	r.restoreCallHandles(ctx, current)
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
		r.sealJobCapture(context.WithoutCancel(current.ctx), current)
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
