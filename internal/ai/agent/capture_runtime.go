package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// captureRegistryBound caps how many completed job captures are retained
// for post-completion review.
const captureRegistryBound = 64

// newJobCapture starts the evidence capture for a job through the
// recorder-side seal authority, or nil when evidence is not configured.
// Every recording incarnation gets an independent capture identity: a
// restored job records a fresh incarnation of the same job, and two
// incarnations of one job can produce identical event chains (a
// no-output recording seals only the deterministic lifecycle marker), so a
// shared capture ID would make the later legitimate anchor collide with
// the earlier one. The job ID stays the traceable prefix and the capture's
// JobID field.
func (r *Runner) newJobCapture(jobID, conversationID string) *Capture {
	recorder := r.evidenceRecorder()
	if recorder == nil {
		return nil
	}
	return recorder.NewRecording(jobID+":"+ids.New(), conversationID, jobID, time.Now().UnixMilli())
}

// recordToolResult appends one tool result to the job's evidence capture.
// Capture failures never fail the run: they are logged and left out of the
// evidence, never silently included as if recorded.
func (r *Runner) recordToolResult(current *job, text string, execFailed bool) {
	if current == nil || current.capture == nil {
		return
	}
	stream := "stdout"
	if execFailed {
		stream = "stderr"
	}
	at := time.Now().UnixMilli() - current.capture.StartedAt
	if err := current.capture.RecordOutput(at, stream, []byte(text)); err != nil {
		slog.Warn("AI 证据记录失败", "job", current.id, "error", err)
	}
}

// recordRunAnswer appends the run's final answer to its evidence
// capture, so every sealed capture carries the run's outcome even when
// no tools executed.
func (r *Runner) recordRunAnswer(current *job, answer string) {
	if current == nil || current.capture == nil || current.capture.Sealed || answer == "" {
		return
	}
	at := time.Now().UnixMilli() - current.capture.StartedAt
	if err := current.capture.RecordOutput(at, "stdout", []byte(answer)); err != nil {
		slog.Warn("AI 证据记录失败", "job", current.id, "error", err)
	}
}

// sealJobCapture seals the job's evidence capture through the recorder,
// persists its anchor, and moves it into the bounded review registry.
// Sealing happens exactly once; a seal failure is logged and the capture
// stays unavailable for review rather than presenting unverifiable
// evidence.
func (r *Runner) sealJobCapture(ctx context.Context, current *job) {
	if current == nil || current.capture == nil || current.capture.Sealed {
		return
	}
	recorder := r.evidenceRecorder()
	if recorder == nil {
		return
	}
	if _, err := recorder.Seal(ctx, current.capture); err != nil {
		slog.Warn("AI 证据封存失败", "job", current.id, "error", err)
		return
	}
	r.rememberCapture(current.id, current.capture)
}

// rememberCapture retains a sealed capture for post-completion review,
// evicting the oldest entry beyond the bound.
func (r *Runner) rememberCapture(jobID string, capture *Capture) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.captures == nil {
		r.captures = make(map[string]*Capture)
	}
	if _, exists := r.captures[jobID]; !exists {
		r.captureOrder = append(r.captureOrder, jobID)
	}
	r.captures[jobID] = capture
	for len(r.captureOrder) > captureRegistryBound {
		oldest := r.captureOrder[0]
		r.captureOrder = r.captureOrder[1:]
		delete(r.captures, oldest)
	}
}

// lookupCapture finds a job's capture from the live job or the review
// registry.
func (r *Runner) lookupCapture(jobID string) *Capture {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.jobs[jobID]; ok && current.capture != nil {
		return current.capture
	}
	return r.captures[jobID]
}

// SanitizeRunCapture returns the sanitized review copy of a job's sealed
// evidence capture with its signer-issued anchor. Tampered or unsealed
// captures fail closed instead of producing review evidence.
func (r *Runner) SanitizeRunCapture(ctx context.Context, jobID string) (*Capture, *ProvenanceAnchor, error) {
	verifier := r.evidenceVerifier()
	if verifier == nil {
		return nil, nil, ipc.NewError(ipc.CodeUnsupported, "evidence verifier 未配置")
	}
	capture := r.lookupCapture(jobID)
	if capture == nil {
		return nil, nil, ipc.NewError(ipc.CodeNotFound, "capture 不存在")
	}
	return verifier.Sanitize(ctx, capture)
}
