package execbatch

import (
	"fmt"
	"sync"
	"time"
)

// maxProgressEvents bounds the retained event log of one batch. When the
// bound is exceeded the oldest events are dropped and Snapshot reports
// DroppedEvents so streaming consumers can detect the gap and fall back to a
// full Snapshot.
const maxProgressEvents = 4096

// ProgressPhase is the lifecycle of the batch and of each target within it.
type ProgressPhase string

const (
	ProgressPhasePending  ProgressPhase = "pending"
	ProgressPhaseRunning  ProgressPhase = "running"
	ProgressPhaseFinished ProgressPhase = "finished"
)

// TargetProgress is the point-in-time state of one batch target, including
// the bounded stdout/stderr retained so far and the final result fields once
// the target has finished.
type TargetProgress struct {
	SessionID  string               `json:"sessionId"`
	Position   int                  `json:"position"`
	Phase      ProgressPhase        `json:"phase"`
	Status     Status               `json:"status,omitempty"`
	ExitCode   *int                 `json:"exitCode,omitempty"`
	Signal     string               `json:"signal,omitempty"`
	Error      string               `json:"error,omitempty"`
	Output     TargetOutputSnapshot `json:"output"`
	StartedAt  int64                `json:"startedAt,omitempty"`
	FinishedAt int64                `json:"finishedAt,omitempty"`
	DurationMS int64                `json:"durationMs,omitempty"`
}

// BatchProgress rolls up the per-target states of one batch. Failed counts
// finished targets that neither succeeded nor were canceled, matching the
// history batch status semantics.
type BatchProgress struct {
	BatchID       string        `json:"batchId"`
	Phase         ProgressPhase `json:"phase"`
	Total         int           `json:"total"`
	Pending       int           `json:"pending"`
	Running       int           `json:"running"`
	Succeeded     int           `json:"succeeded"`
	Failed        int           `json:"failed"`
	Canceled      int           `json:"canceled"`
	FinishedAt    int64         `json:"finishedAt,omitempty"`
	DroppedEvents int64         `json:"droppedEvents,omitempty"`
}

// ProgressEventKind identifies the lifecycle transition an event reports.
type ProgressEventKind string

const (
	ProgressEventTargetStarted  ProgressEventKind = "target_started"
	ProgressEventTargetOutput   ProgressEventKind = "target_output"
	ProgressEventTargetFinished ProgressEventKind = "target_finished"
	ProgressEventBatchFinished  ProgressEventKind = "batch_finished"
)

// ProgressEvent is one sequenced transition of a batch. Seq is strictly
// increasing within the batch; Target carries the target state at the event
// time and is nil for batch-level events.
type ProgressEvent struct {
	Seq     int64             `json:"seq"`
	BatchID string            `json:"batchId"`
	Kind    ProgressEventKind `json:"kind"`
	Target  *TargetProgress   `json:"target,omitempty"`
}

type targetProgressState struct {
	progress TargetProgress
	output   *TargetOutput
}

// ProgressTracker tracks per-target streaming progress of one batch. It is
// safe for concurrent use: the wiring layer calls Start, WriteStdout,
// WriteStderr, and Finish from the execution workers, and Snapshot or
// Events from the progress consumer. The tracker lives exactly as long as
// the batch; the service keys it per batch alongside the owner so progress
// stays inside the existing owner isolation boundary.
type ProgressTracker struct {
	mu            sync.Mutex
	batchID       string
	order         []string
	targets       map[string]*targetProgressState
	events        []ProgressEvent
	droppedEvents int64
	nextSeq       int64
	phase         ProgressPhase
	finishedAt    int64
}

// NewProgressTracker registers every target in pending state. Duplicate
// session IDs collapse onto their first position.
func NewProgressTracker(batchID string, sessionIDs []string) *ProgressTracker {
	tracker := &ProgressTracker{
		batchID: batchID,
		order:   make([]string, 0, len(sessionIDs)),
		targets: make(map[string]*targetProgressState, len(sessionIDs)),
		phase:   ProgressPhasePending,
	}
	for _, sessionID := range sessionIDs {
		if _, exists := tracker.targets[sessionID]; exists {
			continue
		}
		tracker.order = append(tracker.order, sessionID)
		tracker.targets[sessionID] = &targetProgressState{
			progress: TargetProgress{SessionID: sessionID, Position: len(tracker.order) - 1, Phase: ProgressPhasePending},
			output:   NewTargetOutput(DefaultOutputRetention),
		}
	}
	return tracker
}

// Start moves one target from pending to running.
func (t *ProgressTracker) Start(sessionID string, now time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	state, err := t.targetStateLocked(sessionID)
	if err != nil {
		return err
	}
	if state.progress.Phase != ProgressPhasePending {
		return fmt.Errorf("%w: target %s already started", ErrInvalid, sessionID)
	}
	state.progress.Phase = ProgressPhaseRunning
	state.progress.StartedAt = now.UnixMilli()
	if t.phase == ProgressPhasePending {
		t.phase = ProgressPhaseRunning
	}
	t.appendEventLocked(ProgressEventTargetStarted, &state.progress)
	return nil
}

// WriteStdout appends one stdout chunk of a running target.
func (t *ProgressTracker) WriteStdout(sessionID string, chunk []byte) error {
	return t.writeOutput(sessionID, chunk, true)
}

// WriteStderr appends one stderr chunk of a running target.
func (t *ProgressTracker) WriteStderr(sessionID string, chunk []byte) error {
	return t.writeOutput(sessionID, chunk, false)
}

func (t *ProgressTracker) writeOutput(sessionID string, chunk []byte, stdout bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	state, err := t.targetStateLocked(sessionID)
	if err != nil {
		return err
	}
	if state.progress.Phase != ProgressPhaseRunning {
		return fmt.Errorf("%w: target %s is not running", ErrInvalid, sessionID)
	}
	if stdout {
		state.output.WriteStdout(chunk)
	} else {
		state.output.WriteStderr(chunk)
	}
	state.progress.Output = state.output.Snapshot()
	t.appendEventLocked(ProgressEventTargetOutput, &state.progress)
	return nil
}

// Finish records the final result of one target. Targets that never started
// (for example unstarted targets of a canceled batch) finish directly from
// pending with their canceled result, mirroring the service contract: their
// StartedAt stays zero, exactly like the zero startedAt that history
// persists as NULL for canceledResult. The result's Stdout and Stderr are
// not copied; the tracker already retains the bounded streams written
// during execution.
func (t *ProgressTracker) Finish(sessionID string, result Result, now time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	state, err := t.targetStateLocked(sessionID)
	if err != nil {
		return err
	}
	if state.progress.Phase == ProgressPhaseFinished {
		return fmt.Errorf("%w: target %s already finished", ErrInvalid, sessionID)
	}
	state.progress.Phase = ProgressPhaseFinished
	state.progress.Status = result.Status
	state.progress.ExitCode = result.ExitCode
	state.progress.Signal = result.Signal
	state.progress.Error = result.Error
	state.progress.DurationMS = result.DurationMS
	state.progress.FinishedAt = now.UnixMilli()
	state.progress.Output = state.output.Snapshot()
	t.appendEventLocked(ProgressEventTargetFinished, &state.progress)
	t.finishBatchLocked(now)
	return nil
}

// Snapshot returns the batch rollup and a copy of every target state in
// batch order.
func (t *ProgressTracker) Snapshot() (BatchProgress, []TargetProgress) {
	t.mu.Lock()
	defer t.mu.Unlock()
	targets := make([]TargetProgress, len(t.order))
	for index, sessionID := range t.order {
		targets[index] = t.targets[sessionID].progress
	}
	return t.batchProgressLocked(), targets
}

// Events returns the retained events with a sequence number greater than
// afterSeq, in ascending order. When DroppedEvents is non-zero the log has a
// gap and consumers should re-read Snapshot instead of replaying.
func (t *ProgressTracker) Events(afterSeq int64) []ProgressEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	events := make([]ProgressEvent, 0, len(t.events))
	for _, event := range t.events {
		if event.Seq > afterSeq {
			events = append(events, event)
		}
	}
	return events
}

func (t *ProgressTracker) targetStateLocked(sessionID string) (*targetProgressState, error) {
	state, exists := t.targets[sessionID]
	if !exists {
		return nil, fmt.Errorf("%w: unknown target %s", ErrInvalid, sessionID)
	}
	return state, nil
}

func (t *ProgressTracker) batchProgressLocked() BatchProgress {
	progress := BatchProgress{
		BatchID:       t.batchID,
		Phase:         t.phase,
		Total:         len(t.order),
		DroppedEvents: t.droppedEvents,
		FinishedAt:    t.finishedAt,
	}
	for _, sessionID := range t.order {
		switch phase := t.targets[sessionID].progress; {
		case phase.Phase == ProgressPhasePending:
			progress.Pending++
		case phase.Phase == ProgressPhaseRunning:
			progress.Running++
		case phase.Status == StatusSuccess:
			progress.Succeeded++
		case phase.Status == StatusCanceled:
			progress.Canceled++
		default:
			progress.Failed++
		}
	}
	return progress
}

func (t *ProgressTracker) appendEventLocked(kind ProgressEventKind, target *TargetProgress) {
	t.nextSeq++
	event := ProgressEvent{Seq: t.nextSeq, BatchID: t.batchID, Kind: kind}
	if target != nil {
		snapshot := *target
		event.Target = &snapshot
	}
	t.events = append(t.events, event)
	if excess := len(t.events) - maxProgressEvents; excess > 0 {
		t.events = append(t.events[:0], t.events[excess:]...)
		t.droppedEvents += int64(excess)
	}
}

func (t *ProgressTracker) finishBatchLocked(now time.Time) {
	if t.phase == ProgressPhaseFinished {
		return
	}
	for _, sessionID := range t.order {
		if t.targets[sessionID].progress.Phase != ProgressPhaseFinished {
			return
		}
	}
	t.phase = ProgressPhaseFinished
	t.finishedAt = now.UnixMilli()
	t.appendEventLocked(ProgressEventBatchFinished, nil)
}
