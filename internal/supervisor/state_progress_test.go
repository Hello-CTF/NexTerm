package supervisor

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

func progressOf(state *SessionState) string {
	p := state.Progress
	if p == nil {
		return "none"
	}
	if p.Value == nil {
		return p.State
	}
	return p.State + ":" + strconv.Itoa(*p.Value)
}

// running puts the harness in the running state so later output reports only
// progress changes.
func (h *trackerHarness) running() {
	h.out(0, "work\r\n")
	h.changes = 0
}

func TestProgressIsReportedAndCleared(t *testing.T) {
	h := newHarness(t)
	h.running()
	h.out(time.Second, "\x1b]9;4;1;40\x07")
	if got := progressOf(h.state()); got != "normal:40" || h.changes != 1 {
		t.Fatalf("progress %s after %d changes", got, h.changes)
	}
	h.out(time.Second, "\x1b]9;4;3\x1b\\")
	if got := progressOf(h.state()); got != "indeterminate" {
		t.Fatalf("progress %s", got)
	}
	h.out(time.Second, "\x1b]9;4;0\x07")
	if got := progressOf(h.state()); got != "none" || h.changes != 3 {
		t.Fatalf("progress %s after %d changes", got, h.changes)
	}
	// A clear without progress is no change.
	h.out(time.Second, "\x1b]9;4;0\x07")
	if h.changes != 3 {
		t.Fatalf("clearing nothing reported a change")
	}
}

func TestProgressErrorAndPausedKeepTheValue(t *testing.T) {
	h := newHarness(t)
	h.running()
	h.out(time.Second, "\x1b]9;4;1;60\x07")
	h.out(time.Second, "\x1b]9;4;4\x07")
	if got := progressOf(h.state()); got != "paused:60" {
		t.Fatalf("progress %s", got)
	}
	h.out(time.Second, "\x1b]9;4;2\x07")
	if got := progressOf(h.state()); got != "error:60" {
		t.Fatalf("progress %s", got)
	}
	h.out(time.Second, "\x1b]9;4;3\x07\x1b]9;4;2\x07")
	if got := progressOf(h.state()); got != "error" {
		t.Fatalf("an error after indeterminate has no value, got %s", got)
	}
}

func TestProgressClearedByCommandBoundariesAndExit(t *testing.T) {
	for name, clear := range map[string]string{
		"command end":   "\x1b]133;D;0\x07",
		"command start": "\x1b]133;C\x07",
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.out(0, "\x1b]133;B\x07")
			if name == "command end" {
				h.out(0, "\x1b]133;C\x07")
			}
			h.out(time.Second, "\x1b]9;4;1;10\x07")
			h.changes = 0
			// Within the coalescing interval: the clear is still reported at once.
			h.out(time.Millisecond, clear)
			if got := progressOf(h.state()); got != "none" || h.changes == 0 {
				t.Fatalf("progress %s after %d changes", got, h.changes)
			}
		})
	}
	h := newHarness(t)
	h.out(0, "\x1b]9;4;1;10\x07")
	h.tracker.exit(nil, h.clock.advance(time.Second))
	if got := progressOf(h.state()); got != "none" {
		t.Fatalf("progress %s after exit", got)
	}
}

func TestProgressChangesAreCoalesced(t *testing.T) {
	h := newHarness(t)
	h.running()
	h.out(time.Second, "\x1b]9;4;1;1\x07")
	if h.changes != 1 {
		t.Fatalf("the first change is reported at once, changes %d", h.changes)
	}
	for i := 2; i <= 9; i++ {
		h.out(20*time.Millisecond, "\x1b]9;4;1;"+strconv.Itoa(i)+"\x07")
	}
	if h.changes != 1 {
		t.Fatalf("changes within the interval were reported: %d", h.changes)
	}
	// The snapshot is always current; only the change signal is held.
	if got := progressOf(h.state()); got != "normal:9" {
		t.Fatalf("progress %s", got)
	}
	due := h.tracker.progressDue
	if want := stateEpoch0.Add(time.Second + progressInterval); !due.Equal(want) {
		t.Fatalf("held change due %v, want %v", due, want)
	}
	h.tracker.progressTick(due.Add(-time.Millisecond))
	if h.changes != 1 {
		t.Fatalf("an early tick reported the held change")
	}
	h.tracker.progressTick(due)
	if h.changes != 2 {
		t.Fatalf("the held change was not reported, changes %d", h.changes)
	}
	h.tracker.progressTick(due.Add(progressInterval))
	if h.changes != 2 {
		t.Fatalf("a tick without a held change reported one")
	}
	// After a quiet interval the next change is reported at once again.
	h.clock.now = due.Add(progressInterval)
	h.out(0, "\x1b]9;4;1;50\x07")
	if h.changes != 3 {
		t.Fatalf("changes %d", h.changes)
	}
}

func TestProgressGoesStale(t *testing.T) {
	h := newHarness(t)
	h.running()
	h.out(time.Second, "\x1b]9;4;1;30\x07")
	// A repeated report keeps it fresh without reporting a change.
	h.out(10*time.Second, "\x1b]9;4;1;30\x07")
	if h.changes != 1 {
		t.Fatalf("a repeated report was a change: %d", h.changes)
	}
	h.tracker.progressTick(h.clock.advance(progressStale - time.Millisecond))
	if got := progressOf(h.state()); got != "normal:30" {
		t.Fatalf("refreshed progress went stale early: %s", got)
	}
	h.tracker.progressTick(h.clock.advance(time.Millisecond))
	if got := progressOf(h.state()); got != "none" || h.changes != 2 {
		t.Fatalf("stale progress %s after %d changes", got, h.changes)
	}
	if !h.tracker.progressDue.IsZero() {
		t.Fatalf("a timer stays armed without progress: %v", h.tracker.progressDue)
	}
}

func TestProgressErrorAsksOncePerProgress(t *testing.T) {
	h := newHarness(t)
	h.running()
	h.out(time.Second, "\x1b]9;4;1;20\x07")
	h.out(time.Millisecond, "\x1b]9;4;2;20\x07")
	state := h.state()
	if state.State != base.SessionNeedsMe || state.Reason != "progress_error" || state.Confidence != base.ConfidenceSignal {
		t.Fatalf("state %+v", state)
	}
	// The error is reported at once despite the coalescing interval.
	if h.changes != 2 {
		t.Fatalf("changes %d", h.changes)
	}
	// Later output does not replace the pending signal state.
	h.out(time.Second, "more output\r\n")
	if h.state().State != base.SessionNeedsMe {
		t.Fatalf("output replaced needs_me: %+v", h.state())
	}
	h.in(time.Second)
	// The same progress flipping back to error does not ask again.
	h.out(time.Second, "\x1b]9;4;1;30\x07\x1b]9;4;2;30\x07")
	if state := h.state(); state.State == base.SessionNeedsMe {
		t.Fatalf("a repeated error asked again: %+v", state)
	}
	// A new progress after a clear does.
	h.out(time.Second, "\x1b]9;4;0\x07\x1b]9;4;2\x07")
	if state := h.state(); state.State != base.SessionNeedsMe || progressOf(state) != "error" {
		t.Fatalf("state %+v progress %s", state, progressOf(state))
	}
}

func TestProgressClearErrorLoopIsCoalesced(t *testing.T) {
	h := newHarness(t)
	h.running()
	h.out(time.Second, "\x1b]9;4;2;1\x07")
	if h.state().State != base.SessionNeedsMe || h.changes != 1 {
		t.Fatalf("state %+v after %d changes", h.state(), h.changes)
	}
	h.clock.advance(time.Second)
	h.changes = 0
	for i := 2; i <= 50; i++ {
		h.out(time.Millisecond, "\x1b]9;4;0\x07\x1b]9;4;2;"+strconv.Itoa(i)+"\x07")
	}
	if h.changes > 1 {
		t.Fatalf("a clear and error loop within the interval reported %d changes", h.changes)
	}
	if state := h.state(); state.State != base.SessionNeedsMe || progressOf(state) != "error:50" {
		t.Fatalf("state %+v progress %s", state, progressOf(state))
	}
	h.tracker.progressTick(h.clock.advance(progressInterval))
	if h.changes > 2 {
		t.Fatalf("changes %d", h.changes)
	}
}

func TestProgressRealTimerReportsTheHeldChange(t *testing.T) {
	changes := make(chan struct{}, 16)
	tracker := newStateTracker(stateOptions{
		cols: 80, rows: 24, seq: new(atomic.Uint64), timers: true,
		onChange: func() {
			select {
			case changes <- struct{}{}:
			default:
			}
		},
	}, time.Now())
	defer tracker.exit(nil, time.Now())
	tracker.output([]byte("\x1b]9;4;1;1\x07"), time.Now())
	tracker.output([]byte("\x1b]9;4;1;2\x07"), time.Now())
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-changes:
			if progressOf(tracker.snapshot(false, 0)) == "normal:2" && !tracker.progressHeld() {
				return
			}
		case <-deadline:
			t.Fatalf("the held change was never reported")
		}
	}
}

// progressHeld reports whether a progress change waits for its report.
func (t *stateTracker) progressHeld() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.progressDirty
}
