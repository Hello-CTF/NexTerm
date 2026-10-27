package supervisor

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/pty"
	"github.com/Hello-CTF/NexTerm/internal/terminal"
	"github.com/Hello-CTF/NexTerm/internal/terminal/shape"
	"github.com/Hello-CTF/NexTerm/internal/terminal/signals"
	"github.com/Hello-CTF/NexTerm/internal/terminal/staterule"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// Session attention state types; see base.SessionState.
type (
	SessionState        = base.SessionState
	SessionPrompt       = base.SessionPrompt
	SessionPromptOption = base.SessionPromptOption
	SessionEvent        = base.SessionEvent
)

const (
	// defaultStateIdle is how long output must pause before the session
	// counts as idle and the screen is inspected for a prompt.
	defaultStateIdle = 400 * time.Millisecond
	// defaultStateSettle is how long a session without a prompt stays
	// running after its output pauses, so a build printing every second does
	// not flap between running and idle.
	defaultStateSettle = 3 * time.Second
	// defaultNotableCommand is the command run time from which a finished
	// command is worth a notification.
	defaultNotableCommand = 10 * time.Second
	// maxStateEvents bounds the recent events kept per session.
	maxStateEvents = 32
	// maxEventText bounds each of a notification's title and body as kept in
	// the event ring.
	maxEventText = 256
	// enterKey is what a terminal sends for Enter.
	enterKey = "\r"
	// bellEchoWindow is how long after input a bell counts as the reaction
	// to that input, such as a line editor's failed completion, rather than
	// a request for attention.
	bellEchoWindow = time.Second
)

// stateOptions configures a stateTracker.
type stateOptions struct {
	cols, rows int
	// idle and notableAfter default to defaultStateIdle and
	// defaultNotableCommand.
	idle         time.Duration
	settle       time.Duration
	notableAfter time.Duration
	// shellLeader reports that the session process is an interactive
	// shell, so its own process group in the terminal foreground means the
	// shell, not a program it started, reads the input.
	shellLeader bool
	// foreground reads the PTY foreground state; nil where unsupported.
	foreground func() (pty.Foreground, error)
	// appCursor reports DECCKM (application cursor keys); nil means off.
	appCursor func() bool
	// screen, when set, reads the screen of a terminal model the caller
	// already keeps; the tracker then parses output only for signals and
	// keeps no screen of its own. It is called once per inspection, with
	// the tracker lock held, and must not call back into the tracker.
	screen func() terminal.Snapshot
	// seq numbers events; it is shared by every session of a supervisor
	// so sequences are monotonic across sessions.
	seq *atomic.Uint64
	// onChange runs, outside the tracker lock, after a change clients
	// should see: a state change, a new event or a prompt appearing or
	// going stale.
	onChange func()
	// timers arms a real timer for the idle transition; tests leave it off
	// and call tick with their own clock.
	timers bool
	// rules returns the state rules of rulesOwner, the rule owner of the
	// client that created the session; nil means none.
	rules      func() *staterule.Compiled
	rulesOwner string
}

// stateTracker derives a session's attention state from its output and
// input. It parses the output with a signal scanner and a scrollback-free
// screen emulator, and inspects the screen for a prompt shape only when the
// output goes idle, never on a poll.
//
// Rules:
//   - output makes the session running unless a signal state is pending or
//     a detected prompt holds it in needs_me: a redraw keeps the prompt until
//     the next inspection finds none or a different one, and output that
//     goes on for settle without a pause is inspected anyway;
//   - BEL or a notification makes it needs_me (confidence signal) until
//     input arrives, a command starts or ends, or the process exits; a bell
//     within bellEchoWindow of input or at the shell prompt is only recorded;
//   - OSC 133 D makes it just_finished until input arrives or a command
//     starts; the state is notable when the command ran notableAfter or
//     longer;
//   - when output pauses for idle, the screen is inspected once: a prompt
//     shape makes it needs_me (confidence heuristic) unless OSC 133 puts the
//     shell at its own prompt; otherwise it is idle at once at the shell
//     prompt, or after settle of quiet elsewhere;
//   - a user state rule matching the screen at that inspection decides the
//     state (confidence rule) ahead of the prompt shape and the shell prompt;
//     only a pending signal state wins over it. A rule's needs_me is held
//     through redraws like a detected prompt;
//   - input clears needs_me and just_finished and inspects again after the
//     next pause;
//   - process exit makes it exited.
type stateTracker struct {
	opts stateOptions

	mu      sync.Mutex
	scanner *signals.Scanner
	// emu is the tracker's own screen; nil when opts.screen supplies it.
	emu *terminal.Emulator

	state      string
	confidence string
	reason     string
	since      time.Time
	exitCode   *int
	durationMS int64
	notable    bool
	// seen is set when a client showed the current needs_me or
	// just_finished state to its user.
	seen bool
	// ruleID and ruleKeys name the state rule that set the state.
	ruleID   string
	ruleKeys string

	// active is set by output or input since the last idle inspection and
	// lastActivity is when it happened. settling is set when that inspection
	// found no prompt and the session waits out settle before it is idle.
	active       bool
	settling     bool
	lastActivity time.Time
	lastInput    time.Time
	// signalPending and finishedPending hold needs_me (signal) and
	// just_finished against later output.
	signalPending   bool
	finishedPending bool
	// heldCheck is when output that keeps a held prompt busy without a
	// pause is inspected anyway; zero when not due.
	heldCheck time.Time

	// rule is the state rule the last inspection matched, when ruled.
	rule  staterule.Match
	ruled bool

	// prompt is the result of the last inspection, info its published form.
	// fresh reports that nothing was written or input since.
	prompt shape.Prompt
	info   *SessionPrompt
	fresh  bool
	epoch  uint64

	events []SessionEvent
	// lastSeq is the newest event sequence; evictedSeq the newest one
	// dropped from the ring.
	lastSeq    uint64
	evictedSeq uint64

	// deadline is when tick next has work, zero when nothing is armed; with
	// timers a real timer fires then.
	deadline time.Time
	timer    *time.Timer
	exited   bool

	// writes counts changes to the screen, so an inspection prepared
	// outside the lock can tell whether the screen moved on since.
	writes uint64
	// inspections counts prompt detector runs.
	inspections uint64
}

func newStateTracker(opts stateOptions, now time.Time) *stateTracker {
	if opts.idle <= 0 {
		opts.idle = defaultStateIdle
	}
	if opts.settle < opts.idle {
		opts.settle = max(defaultStateSettle, opts.idle)
	}
	if opts.notableAfter <= 0 {
		opts.notableAfter = defaultNotableCommand
	}
	if opts.seq == nil {
		opts.seq = new(atomic.Uint64)
	}
	var emu *terminal.Emulator
	if opts.screen == nil {
		emu = terminal.NewEmulator(opts.cols, opts.rows)
	}
	return &stateTracker{
		opts:       opts,
		scanner:    signals.NewScanner(signals.Config{}),
		emu:        emu,
		state:      base.SessionIdle,
		confidence: base.ConfidenceSignal,
		reason:     "start",
		since:      now,
		prompt:     noPrompt(),
		epoch:      1,
	}
}

func noPrompt() shape.Prompt { return shape.Prompt{Kind: shape.KindNone, CursorIndex: -1} }

// output feeds one chunk of session output.
func (t *stateTracker) output(p []byte, now time.Time) {
	if len(p) == 0 {
		return
	}
	t.mu.Lock()
	if t.exited {
		t.mu.Unlock()
		return
	}
	if t.emu != nil {
		t.emu.Write(p)
	}
	t.writes++
	changed := false
	for _, event := range t.scanner.Feed(p, now) {
		if t.signalLocked(event, now) {
			changed = true
		}
	}
	// A held prompt stays published through redraws; a tap re-inspects the
	// screen before it sends anything.
	if t.fresh && !t.heldLocked() {
		t.fresh, changed = false, true
	}
	if !t.signalPending && !t.finishedPending && !t.heldLocked() && t.setLocked(base.SessionRunning, base.ConfidenceSignal, "output", now) {
		changed = true
	}
	if t.heldLocked() && t.heldCheck.IsZero() {
		t.heldCheck = now.Add(t.opts.settle)
	}
	t.activityLocked(now)
	t.mu.Unlock()
	if changed {
		t.changed()
	}
}

func (t *stateTracker) signalLocked(event signals.Event, now time.Time) bool {
	switch event.Kind {
	case signals.KindBell:
		// A bell answering recent input or rung by the shell's line editor
		// is kept as an event but does not ask for attention.
		switch {
		case event.AtPrompt:
			t.addEventLocked(SessionEvent{Kind: base.SessionEventBell, At: event.At, Reason: "shell_prompt"})
			return true
		case !t.lastInput.IsZero() && now.Sub(t.lastInput) < bellEchoWindow:
			t.addEventLocked(SessionEvent{Kind: base.SessionEventBell, At: event.At, Reason: "after_input"})
			return true
		}
		t.addEventLocked(SessionEvent{Kind: base.SessionEventBell, At: event.At})
		t.signalPending, t.finishedPending, t.seen = true, false, false
		t.setLocked(base.SessionNeedsMe, base.ConfidenceSignal, "bell", now)
	case signals.KindNotify:
		title, cutTitle := clipText(event.Title)
		body, cutBody := clipText(event.Body)
		t.addEventLocked(SessionEvent{
			Kind: base.SessionEventNotify, At: event.At, Source: event.Source,
			Title: title, Body: body, TextTruncated: cutTitle || cutBody,
		})
		t.signalPending, t.finishedPending, t.seen = true, false, false
		t.setLocked(base.SessionNeedsMe, base.ConfidenceSignal, "notify", now)
	case signals.KindCommandStart:
		t.signalPending, t.finishedPending = false, false
		t.setLocked(base.SessionRunning, base.ConfidenceSignal, "command_start", now)
	case signals.KindCommandEnd:
		var code *int
		if event.HasExitCode {
			value := event.ExitCode
			code = &value
		}
		duration := event.Duration.Milliseconds()
		notable := event.Duration >= t.opts.notableAfter
		t.addEventLocked(SessionEvent{
			Kind: base.SessionEventCommandEnd, At: event.At,
			ExitCode: copyInt(code), DurationMS: duration, Notable: notable,
		})
		t.signalPending, t.finishedPending = false, true
		t.forceLocked(base.SessionJustFinished, base.ConfidenceSignal, "command_end", now)
		t.exitCode, t.durationMS, t.notable = code, duration, notable
	default:
		return false
	}
	return true
}

// input records bytes written to the session by any client. Every write
// advances the prompt epoch, so a prompt answered once can never be
// answered again with the same epoch, and clears pending signal states.
func (t *stateTracker) input(now time.Time) {
	t.mu.Lock()
	if t.exited {
		t.mu.Unlock()
		return
	}
	t.epoch++
	t.lastInput = now
	changed := t.fresh
	t.fresh = false
	t.signalPending, t.finishedPending = false, false
	if t.state == base.SessionNeedsMe || t.state == base.SessionJustFinished {
		changed = t.setLocked(base.SessionIdle, base.ConfidenceSignal, "input", now) || changed
	}
	// Re-inspect once the program has reacted: a prompt that ignored the
	// input is reported again with the new epoch.
	t.activityLocked(now)
	t.mu.Unlock()
	if changed {
		t.changed()
	}
}

// heldLocked reports that a detected prompt holds the session in needs_me.
func (t *stateTracker) heldLocked() bool {
	return t.state == base.SessionNeedsMe && (t.confidence == base.ConfidenceHeuristic || t.confidence == base.ConfidenceRule)
}

// activityLocked starts a new pause. The armed deadline is kept when it is
// due no later than the idle inspection, and otherwise, such as while
// settling, brought forward to it.
func (t *stateTracker) activityLocked(now time.Time) {
	t.active, t.settling = true, false
	t.lastActivity = now
	if inspect := now.Add(t.opts.idle); t.deadline.IsZero() || t.deadline.After(inspect) {
		t.armLocked(now, t.opts.idle)
	}
}

func (t *stateTracker) armLocked(now time.Time, d time.Duration) {
	t.deadline = now.Add(d)
	if !t.opts.timers {
		return
	}
	if t.timer == nil {
		t.timer = time.AfterFunc(d, t.fire)
		return
	}
	t.timer.Reset(d)
}

func (t *stateTracker) fire() {
	t.tick(time.Now())
}

// tick runs the idle transition when activity paused for the idle
// threshold: the screen is inspected once, and a prompt makes the session
// need its user. Without a prompt the session is idle at once when the
// shell shows its own prompt, and otherwise after the settle time. tick
// returns how long to wait for the next step, or zero when none is due.
func (t *stateTracker) tick(now time.Time) time.Duration {
	pre := t.prepare(now)
	t.mu.Lock()
	changed := false
	wait := time.Duration(0)
	quiet := now.Sub(t.lastActivity)
	switch {
	case t.exited:
	case t.active && quiet < t.opts.idle && t.heldLocked() && !t.heldCheck.IsZero() && !now.Before(t.heldCheck):
		// Output has kept a held prompt busy for settle: check that the
		// prompt is still there.
		before := t.prompt
		changed = t.inspectLocked(pre)
		t.heldCheck = now.Add(t.opts.settle)
		switch {
		case t.signalPending, t.finishedPending:
		case t.ruled:
			changed = t.applyRuleLocked(now) || changed
		case t.prompt.Kind != shape.KindNone && !t.scanner.AtPrompt():
			if before.Kind != t.prompt.Kind || before.Fingerprint != t.prompt.Fingerprint {
				t.forceLocked(base.SessionNeedsMe, base.ConfidenceHeuristic, "prompt", now)
				t.heldCheck = now.Add(t.opts.settle)
				changed = true
			}
		default:
			changed = t.setLocked(base.SessionRunning, base.ConfidenceSignal, "output", now) || changed
		}
		if t.heldLocked() && t.heldCheck.IsZero() {
			t.heldCheck = now.Add(t.opts.settle)
		}
		wait = t.activeWaitLocked(now, quiet)
	case t.active && quiet < t.opts.idle:
		wait = t.activeWaitLocked(now, quiet)
	case t.active:
		t.active, t.heldCheck = false, time.Time{}
		before := t.prompt
		ruleGone := t.confidence == base.ConfidenceRule
		changed = t.inspectLocked(pre)
		ruleGone = ruleGone && !t.ruled
		held, atPrompt := t.heldLocked(), t.scanner.AtPrompt()
		switch {
		case t.signalPending, t.finishedPending:
		case t.ruled:
			changed = t.applyRuleLocked(now) || changed
		case t.prompt.Kind != shape.KindNone && !atPrompt:
			if held && (before.Kind != t.prompt.Kind || before.Fingerprint != t.prompt.Fingerprint) {
				// A different prompt replaced the held one.
				t.forceLocked(base.SessionNeedsMe, base.ConfidenceHeuristic, "prompt", now)
				changed = true
				break
			}
			changed = t.setLocked(base.SessionNeedsMe, base.ConfidenceHeuristic, "prompt", now) || changed
		case held && !atPrompt && quiet < t.opts.settle:
			// The held prompt is gone while output went on: the program runs.
			changed = t.setLocked(base.SessionRunning, base.ConfidenceSignal, "output", now) || changed
			t.settling = true
			wait = t.opts.settle - quiet
		case atPrompt || quiet >= t.opts.settle || t.state != base.SessionRunning:
			changed = t.setLocked(base.SessionIdle, base.ConfidenceSignal, "quiet", now) || changed
		default:
			if ruleGone {
				// The rule that set the state no longer matches: the session
				// counts as running until it settles.
				t.forceLocked(base.SessionRunning, base.ConfidenceSignal, "output", now)
				changed = true
			}
			t.settling = true
			wait = t.opts.settle - quiet
		}
	case t.settling && quiet < t.opts.settle:
		wait = t.opts.settle - quiet
	case t.settling:
		t.settling = false
		changed = t.setLocked(base.SessionIdle, base.ConfidenceSignal, "quiet", now)
	}
	t.deadline = time.Time{}
	if wait > 0 {
		t.armLocked(now, wait)
	}
	t.mu.Unlock()
	if changed {
		t.changed()
	}
	return wait
}

// activeWaitLocked is how long tick waits while output is active: until the
// idle inspection, or the check of a held prompt when that comes first.
func (t *stateTracker) activeWaitLocked(now time.Time, quiet time.Duration) time.Duration {
	wait := t.opts.idle - quiet
	if t.heldLocked() && !t.heldCheck.IsZero() {
		wait = min(wait, max(t.heldCheck.Sub(now), time.Millisecond))
	}
	return wait
}

// due returns the armed deadline, when tick next has work and a real timer
// fires, or false when nothing is armed.
func (t *stateTracker) due() (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.deadline, !t.deadline.IsZero()
}

// inspection is the screen evaluated for one inspection: the state rule that
// matched and the detected prompt, for the screen at writes.
type inspection struct {
	writes   uint64
	rule     staterule.Match
	ruled    bool
	detected shape.Prompt
}

// prepare evaluates the screen for an inspection tick is about to make, so
// the rule match and prompt detection run outside the tracker lock and do
// not stall the output pump. It returns nil when no inspection is due.
func (t *stateTracker) prepare(now time.Time) *inspection {
	t.mu.Lock()
	quiet := now.Sub(t.lastActivity)
	heldDue := t.heldLocked() && !t.heldCheck.IsZero() && !now.Before(t.heldCheck)
	if t.exited || !t.active || (quiet < t.opts.idle && !heldDue) {
		t.mu.Unlock()
		return nil
	}
	snap := t.screenLocked()
	writes := t.writes
	t.mu.Unlock()
	return t.evaluate(snap, writes)
}

func (t *stateTracker) evaluate(snap terminal.Snapshot, writes uint64) *inspection {
	result := &inspection{writes: writes, detected: shape.Detect(snap)}
	if t.opts.rules != nil {
		result.rule, result.ruled = t.opts.rules().Match(staterule.Screen{Lines: snap.Lines, CursorRow: snap.CursorRow, Title: snap.Title})
	}
	return result
}

// inspectLocked applies an inspection of the current screen: pre when it
// was made for the screen as it is, otherwise one made now. It updates the
// matched rule, the prompt and its epoch, and reports whether the published
// prompt changed.
func (t *stateTracker) inspectLocked(pre *inspection) bool {
	t.inspections++
	if pre == nil || pre.writes != t.writes {
		pre = t.evaluate(t.screenLocked(), t.writes)
	}
	t.rule, t.ruled = pre.rule, pre.ruled
	detected := pre.detected
	wasFresh := t.fresh
	before := t.prompt
	if (before.Kind == shape.KindNone) != (detected.Kind == shape.KindNone) ||
		(detected.Kind != shape.KindNone && detected.Fingerprint != before.Fingerprint) {
		t.epoch++
	}
	t.prompt = detected
	t.info = t.promptInfoLocked(detected)
	t.fresh = true
	return !wasFresh && detected.Kind != shape.KindNone || before.Kind != detected.Kind || before.Fingerprint != detected.Fingerprint
}

func (t *stateTracker) screenLocked() terminal.Snapshot {
	if t.emu != nil {
		return t.emu.Snapshot()
	}
	return t.opts.screen()
}

// currentPrompt inspects the screen now, as a tap validation must, and
// returns the published prompt (nil for none) with its epoch.
func (t *stateTracker) currentPrompt() *SessionPrompt {
	t.mu.Lock()
	changed := false
	var prompt *SessionPrompt
	if !t.exited {
		changed = t.inspectLocked(nil)
		prompt = t.publishedPromptLocked()
	}
	t.mu.Unlock()
	if changed {
		t.changed()
	}
	return prompt
}

// rulesChanged re-evaluates a quiet session after the state rules changed,
// as if its output had just paused; a session with output in flight picks
// the new rules up at its next inspection anyway.
func (t *stateTracker) rulesChanged(now time.Time) {
	t.mu.Lock()
	if t.exited || t.active {
		t.mu.Unlock()
		return
	}
	t.active = true
	t.lastActivity = now.Add(-t.opts.idle)
	t.mu.Unlock()
	t.tick(now)
}

func (t *stateTracker) resize(cols, rows int) {
	t.mu.Lock()
	if t.emu != nil {
		t.emu.Resize(cols, rows)
	}
	t.writes++
	t.mu.Unlock()
}

// exit records the process exit and stops the idle timer.
func (t *stateTracker) exit(code *int, now time.Time) {
	t.mu.Lock()
	if t.exited {
		t.mu.Unlock()
		return
	}
	if t.prompt.Kind != shape.KindNone {
		t.epoch++
	}
	t.prompt, t.info, t.fresh = noPrompt(), nil, false
	t.signalPending, t.finishedPending = false, false
	t.forceLocked(base.SessionExited, base.ConfidenceSignal, "exit", now)
	t.exitCode = copyInt(code)
	t.exited, t.active, t.settling = true, false, false
	t.deadline = time.Time{}
	if t.timer != nil {
		t.timer.Stop()
	}
	t.mu.Unlock()
	t.changed()
}

// markSeen records that a client showed the needs_me or just_finished state
// that began at since to its user. It reports whether anything changed; a
// state that has moved on since is left alone.
func (t *stateTracker) markSeen(since time.Time) bool {
	t.mu.Lock()
	changed := !t.seen && t.since.Equal(since) &&
		(t.state == base.SessionNeedsMe || t.state == base.SessionJustFinished)
	if changed {
		t.seen = true
	}
	t.mu.Unlock()
	if changed {
		t.changed()
	}
	return changed
}

func (t *stateTracker) changed() {
	if t.opts.onChange != nil {
		t.opts.onChange()
	}
}

// setLocked moves to state unless the session is already there with the
// same confidence; it records a state event and reports whether it moved.
func (t *stateTracker) setLocked(state, confidence, reason string, now time.Time) bool {
	if t.state == state && t.confidence == confidence {
		return false
	}
	t.forceLocked(state, confidence, reason, now)
	return true
}

func (t *stateTracker) forceLocked(state, confidence, reason string, now time.Time) {
	t.forceRuleLocked(state, confidence, reason, staterule.Match{}, now)
}

func (t *stateTracker) forceRuleLocked(state, confidence, reason string, rule staterule.Match, now time.Time) {
	t.state, t.confidence, t.reason, t.since = state, confidence, reason, now
	t.exitCode, t.durationMS, t.notable, t.seen = nil, 0, false, false
	t.ruleID, t.ruleKeys = rule.ID, rule.Keys
	t.heldCheck = time.Time{}
	t.addEventLocked(SessionEvent{Kind: base.SessionEventState, At: now, State: state, Confidence: confidence, Reason: reason, RuleID: rule.ID})
}

// applyRuleLocked moves to the state of the rule the last inspection
// matched unless that rule already set the current state.
func (t *stateTracker) applyRuleLocked(now time.Time) bool {
	if t.state == t.rule.State && t.confidence == base.ConfidenceRule && t.ruleID == t.rule.ID && t.ruleKeys == t.rule.Keys {
		return false
	}
	t.forceRuleLocked(t.rule.State, base.ConfidenceRule, "rule", t.rule, now)
	return true
}

func (t *stateTracker) addEventLocked(event SessionEvent) {
	event.Seq = t.opts.seq.Add(1)
	t.lastSeq = event.Seq
	if len(t.events) == maxStateEvents {
		t.evictedSeq = t.events[0].Seq
		copy(t.events, t.events[1:])
		t.events = t.events[:len(t.events)-1]
	}
	t.events = append(t.events, event)
}

// snapshot returns the state. With events it includes the retained events
// after the given sequence.
func (t *stateTracker) snapshot(withEvents bool, after uint64) *SessionState {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := &SessionState{
		State:        t.state,
		Since:        t.since,
		Confidence:   t.confidence,
		Reason:       t.reason,
		RuleID:       t.ruleID,
		RuleKeys:     t.ruleKeys,
		ExitCode:     copyInt(t.exitCode),
		DurationMS:   t.durationMS,
		Notable:      t.notable,
		Seen:         t.seen,
		PromptEpoch:  t.epoch,
		Prompt:       t.publishedPromptLocked(),
		LastEventSeq: t.lastSeq,
	}
	if withEvents {
		for _, event := range t.events {
			if event.Seq > after {
				event.ExitCode = copyInt(event.ExitCode)
				state.Events = append(state.Events, event)
			}
		}
		state.EventsDropped = t.evictedSeq > after
	}
	return state
}

func (t *stateTracker) publishedPromptLocked() *SessionPrompt {
	if !t.fresh || t.info == nil {
		return nil
	}
	prompt := *t.info
	prompt.Options = append([]SessionPromptOption(nil), t.info.Options...)
	prompt.Epoch = t.epoch
	return &prompt
}

// promptInfoLocked describes a detected prompt with its tap metadata.
func (t *stateTracker) promptInfoLocked(p shape.Prompt) *SessionPrompt {
	if p.Kind == shape.KindNone {
		return nil
	}
	ctx := tapContext{atShellPrompt: t.scanner.AtPrompt()}
	if t.opts.appCursor != nil {
		ctx.appCursor = t.opts.appCursor()
	}
	if t.opts.foreground != nil {
		if fg, err := t.opts.foreground(); err == nil {
			ctx.foregroundKnown = true
			ctx.shellForeground = t.opts.shellLeader && fg.LeaderInForeground()
			ctx.canonical, ctx.canonicalKnown = fg.Canonical, fg.CanonicalKnown
		}
	}
	return describePrompt(p, ctx)
}

// tapContext is what tap gating needs to know about the terminal.
type tapContext struct {
	// atShellPrompt: OSC 133 says the shell shows its own prompt.
	atShellPrompt bool
	// foregroundKnown: the foreground process group could be read; false
	// on Windows ConPTY, where no prompt is tappable.
	foregroundKnown bool
	// shellForeground: the session's interactive shell, not a program it
	// started, owns the terminal foreground.
	shellForeground bool
	// canonical is ICANON on the terminal when canonicalKnown.
	canonical      bool
	canonicalKnown bool
	// appCursor is DECCKM: arrows are sent as SS3 instead of CSI.
	appCursor bool
}

// describePrompt applies the tap rules to a detected prompt:
//   - a question (free text, including passwords) is never tappable;
//   - nothing is tappable while the shell is at its prompt or owns the
//     terminal foreground, or when the foreground cannot be read;
//   - yes/no sends the answer key, followed by Enter when the terminal is in
//     canonical (line) mode. Otherwise the key is sent alone and Enter is
//     offered separately: a raw-mode foreground may be a relay such as ssh,
//     docker exec or a tmux client, whose mode says nothing about the remote
//     program;
//   - a menu with a selection marker is answered by moving the selection
//     with the arrow keys (encoded per DECCKM) and pressing Enter, except a
//     menu whose only marker is "*" or ">" without numbering;
//   - a numbered menu without a marker is answered by its number and Enter,
//     only in canonical mode.
func describePrompt(p shape.Prompt, ctx tapContext) *SessionPrompt {
	info := &SessionPrompt{
		Kind:        string(p.Kind),
		Question:    p.Question,
		CursorIndex: p.CursorIndex,
		Fingerprint: strconv.FormatUint(p.Fingerprint, 16),
		Options:     make([]SessionPromptOption, len(p.Options)),
	}
	numbered := false
	for i, option := range p.Options {
		info.Options[i] = SessionPromptOption{Label: option.Label, Keys: option.Keys, Default: option.Default}
		if option.Keys != "" {
			numbered = true
		}
	}
	block := func(reason string) *SessionPrompt {
		info.Blocked = reason
		return info
	}
	switch {
	case p.Kind == shape.KindQuestion:
		return block(base.TapBlockedQuestion)
	case ctx.atShellPrompt:
		return block(base.TapBlockedShellPrompt)
	case !ctx.foregroundKnown:
		return block(base.TapBlockedForegroundUnknown)
	case ctx.shellForeground:
		return block(base.TapBlockedShellForeground)
	}
	switch p.Kind {
	case shape.KindYesNo:
		info.Method = base.TapMethodKeys
		suffix := ""
		if ctx.canonicalKnown && ctx.canonical {
			info.Submit, suffix = base.TapSubmitEnter, enterKey
		} else {
			info.Submit, info.EnterKey = base.TapSubmitSeparate, enterKey
		}
		for i := range info.Options {
			info.Options[i].Send = info.Options[i].Keys + suffix
		}
	case shape.KindChoice:
		if p.CursorIndex >= 0 {
			if p.WeakMarker && !numbered {
				return block(base.TapBlockedWeakMarker)
			}
			up, down := "\x1b[A", "\x1b[B"
			if ctx.appCursor {
				up, down = "\x1bOA", "\x1bOB"
			}
			info.Method, info.Submit = base.TapMethodArrows, base.TapSubmitEnter
			for i := range info.Options {
				steps := i - p.CursorIndex
				key := down
				if steps < 0 {
					steps, key = -steps, up
				}
				info.Options[i].Send = strings.Repeat(key, steps) + enterKey
			}
			break
		}
		if !numbered || !ctx.canonicalKnown || !ctx.canonical {
			return block(base.TapBlockedNotLineMode)
		}
		info.Method, info.Submit = base.TapMethodKeys, base.TapSubmitEnter
		for i := range info.Options {
			info.Options[i].Send = info.Options[i].Keys + enterKey
		}
	default:
		return info
	}
	info.Tappable = true
	return info
}

func clipText(s string) (string, bool) {
	if len(s) <= maxEventText {
		return s, false
	}
	cut := maxEventText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
