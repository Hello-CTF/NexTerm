package supervisor

import (
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// AttentionTracker derives a session's attention state outside a helper,
// for terminals that do not run in one (plain SSH exec, docker, WinRM, a
// regular local PTY). It applies the helper's rules to the output the client
// receives. Without access to the PTY it cannot read the foreground process,
// so detected prompts are never tappable.
type AttentionTracker struct {
	tracker *stateTracker
}

// NewAttentionTracker returns a tracker for a cols x rows screen. onChange
// runs, outside the tracker lock and possibly from a timer goroutine, after
// each change worth reporting.
func NewAttentionTracker(cols, rows int, onChange func()) *AttentionTracker {
	return &AttentionTracker{tracker: newStateTracker(stateOptions{
		cols: cols, rows: rows, onChange: onChange, timers: true,
	}, time.Now())}
}

// Output feeds one chunk of session output.
func (a *AttentionTracker) Output(p []byte) { a.tracker.output(p, time.Now()) }

// Input records that a client wrote to the session.
func (a *AttentionTracker) Input() { a.tracker.input(time.Now()) }

// Resize follows the terminal size.
func (a *AttentionTracker) Resize(cols, rows int) { a.tracker.resize(cols, rows) }

// Exit records the process exit and stops the tracker's timer.
func (a *AttentionTracker) Exit(code *int) { a.tracker.exit(code, time.Now()) }

// MarkSeen marks the needs_me or just_finished state that began at since as
// seen; it reports whether anything changed.
func (a *AttentionTracker) MarkSeen(since time.Time) bool { return a.tracker.markSeen(since) }

// State returns the current state without events.
func (a *AttentionTracker) State() *base.SessionState { return a.tracker.snapshot(false, 0) }
