package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"time"
)

// Decisions reported by interactionResolved besides the confirm decisions.
const (
	interactionAnswered = "answered"
	interactionExpired  = "expired"
)

func interactionResolvedEvent(callID, decision string) Event {
	return Event{Type: "interactionResolved", CallID: callID, Decision: decision}
}

func (j *job) addPendingCall(callID string) {
	if callID == "" {
		return
	}
	j.pendingCallsMu.Lock()
	defer j.pendingCallsMu.Unlock()
	if !slices.Contains(j.pendingCalls, callID) {
		j.pendingCalls = append(j.pendingCalls, callID)
	}
}

func (j *job) resolvePendingCall(callID string) {
	j.pendingCallsMu.Lock()
	defer j.pendingCallsMu.Unlock()
	j.pendingCalls = slices.DeleteFunc(j.pendingCalls, func(pending string) bool { return pending == callID })
}

func (j *job) takePendingCalls() []string {
	j.pendingCallsMu.Lock()
	defer j.pendingCallsMu.Unlock()
	calls := j.pendingCalls
	j.pendingCalls = nil
	return calls
}

// queueResolved records an answered interaction; the resumed consumer emits
// it ahead of the resumed events.
func (j *job) queueResolved(callID, decision string) {
	j.resolvePendingCall(callID)
	j.pendingCallsMu.Lock()
	j.resolved = append(j.resolved, interactionResolvedEvent(callID, decision))
	j.pendingCallsMu.Unlock()
}

func (r *Runner) emitQueuedResolved(current *job) {
	current.pendingCallsMu.Lock()
	events := current.resolved
	current.resolved = nil
	current.pendingCallsMu.Unlock()
	for _, event := range events {
		r.emitInteractionResolved(current, event.CallID, event.Decision)
	}
}

// emitInteractionResolved settles the card of a resolved interaction on the
// run's stream and journal, so every client and a later replay see it.
func (r *Runner) emitInteractionResolved(current *job, callID, decision string) {
	current.resolvePendingCall(callID)
	parent := current.deliveryCtx
	if parent == nil {
		parent = context.WithoutCancel(current.ctx)
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	if err := current.emit(ctx, interactionResolvedEvent(callID, decision)); err != nil {
		slog.Warn("AI 交互结果事件发送失败", "job", current.id, "call", callID, "error", err)
	}
}

// emitExpiredInteractions settles every card still shown when the run's
// interaction expired.
func (r *Runner) emitExpiredInteractions(current *job) {
	for _, callID := range current.takePendingCalls() {
		r.emitInteractionResolved(current, callID, interactionExpired)
	}
}

// journalExpiredInteractions records the expiry of every interaction the
// run's journal shows as requested and not resolved, for a restored run that
// expired while no job was loaded to emit it.
func (r *Runner) journalExpiredInteractions(ctx context.Context, runID string) {
	if r.runs == nil {
		return
	}
	rows, err := r.runs.RunEventsAfter(ctx, runID, 0)
	if err != nil {
		slog.Warn("AI 交互过期事件读取失败", "run", runID, "error", err)
		return
	}
	var open []string
	for _, row := range rows {
		var event struct {
			ID     string `json:"id"`
			CallID string `json:"callId"`
		}
		switch row.Type {
		case "confirmRequired", "questionRequired":
			if json.Unmarshal([]byte(row.PayloadJSON), &event) == nil && event.ID != "" && !slices.Contains(open, event.ID) {
				open = append(open, event.ID)
			}
		case "interactionResolved":
			if json.Unmarshal([]byte(row.PayloadJSON), &event) == nil {
				open = slices.DeleteFunc(open, func(callID string) bool { return callID == event.CallID })
			}
		}
	}
	for _, callID := range open {
		event := interactionResolvedEvent(callID, interactionExpired)
		if _, err := r.runs.RunAppendEvent(ctx, runID, event.Type, func(seq uint64) ([]byte, error) {
			event.Seq = seq
			return json.Marshal(event)
		}); err != nil {
			slog.Warn("AI 交互过期事件写入失败", "run", runID, "call", callID, "error", err)
		}
	}
}
