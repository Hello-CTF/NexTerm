package supervisor

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/terminal/staterule"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

var stateEpoch0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// fakeClock drives a tracker without real timers.
type fakeClock struct{ now time.Time }

func (c *fakeClock) advance(d time.Duration) time.Time {
	c.now = c.now.Add(d)
	return c.now
}

type trackerHarness struct {
	t       *testing.T
	clock   *fakeClock
	tracker *stateTracker
	changes int
}

func newHarness(t *testing.T) *trackerHarness {
	t.Helper()
	h := &trackerHarness{t: t, clock: &fakeClock{now: stateEpoch0}}
	h.tracker = newStateTracker(stateOptions{
		cols: 80, rows: 24,
		seq:      new(atomic.Uint64),
		onChange: func() { h.changes++ },
	}, h.clock.now)
	return h
}

func (h *trackerHarness) out(after time.Duration, data string) {
	h.tracker.output([]byte(data), h.clock.advance(after))
}

func (h *trackerHarness) in(after time.Duration) {
	h.tracker.input(h.clock.advance(after))
}

// idle lets the output pause: it runs the idle inspection and, when the
// session keeps running without a matching rule, waits out the settle time.
func (h *trackerHarness) idle() {
	h.t.Helper()
	wait := h.tracker.tick(h.clock.advance(defaultStateIdle))
	if wait != 0 {
		if h.state().State != base.SessionRunning || wait > defaultStateSettle-defaultStateIdle {
			h.t.Fatalf("after the idle inspection: state %s, wait %v", h.state().State, wait)
		}
		if wait = h.tracker.tick(h.clock.advance(wait)); wait != 0 {
			h.t.Fatalf("tick after the settle time asked to wait %v", wait)
		}
	}
}

func (h *trackerHarness) state() *SessionState {
	return h.tracker.snapshot(true, 0)
}

func (h *trackerHarness) expect(state, confidence string) *SessionState {
	h.t.Helper()
	got := h.state()
	if got.State != state || got.Confidence != confidence {
		h.t.Fatalf("state = %s/%s (reason %s), want %s/%s", got.State, got.Confidence, got.Reason, state, confidence)
	}
	return got
}

func TestStateOutputRunsThenIdles(t *testing.T) {
	h := newHarness(t)
	h.expect(base.SessionIdle, base.ConfidenceSignal)
	h.out(time.Second, "building...\r\n")
	running := h.expect(base.SessionRunning, base.ConfidenceSignal)
	if !running.Since.Equal(h.clock.now) {
		t.Fatalf("since = %v, want %v", running.Since, h.clock.now)
	}
	// Activity is too recent: the tracker asks to wait instead of moving.
	if wait := h.tracker.tick(h.clock.advance(100 * time.Millisecond)); wait != defaultStateIdle-100*time.Millisecond {
		t.Fatalf("early tick wait = %v", wait)
	}
	h.expect(base.SessionRunning, base.ConfidenceSignal)
	h.idle()
	h.expect(base.SessionIdle, base.ConfidenceSignal)
	// A tick without activity since the last idle transition is a no-op.
	before := h.changes
	if wait := h.tracker.tick(h.clock.advance(time.Hour)); wait != 0 || h.changes != before {
		t.Fatalf("idle tick changed state: wait %v, changes %d -> %d", wait, before, h.changes)
	}
}

func TestStateBellNeedsMeUntilInput(t *testing.T) {
	h := newHarness(t)
	h.out(time.Second, "waiting for approval\a")
	h.expect(base.SessionNeedsMe, base.ConfidenceSignal)
	// Redraws after the bell do not hide it.
	h.out(50*time.Millisecond, "\r\x1b[Kwaiting for approval")
	h.idle()
	got := h.expect(base.SessionNeedsMe, base.ConfidenceSignal)
	if got.Reason != "bell" {
		t.Fatalf("reason = %q", got.Reason)
	}
	h.in(time.Second)
	got = h.expect(base.SessionIdle, base.ConfidenceSignal)
	var bells int
	for _, event := range got.Events {
		if event.Kind == base.SessionEventBell {
			bells++
		}
	}
	if bells != 1 {
		t.Fatalf("bell events = %d in %+v", bells, got.Events)
	}
}

func TestStateBellAnsweringInputIsNotAttention(t *testing.T) {
	h := newHarness(t)
	h.out(time.Second, "$ ")
	h.idle()
	h.in(time.Second)
	// A line editor beeps at a failed completion right after the keystroke.
	h.out(20*time.Millisecond, "\a")
	h.expect(base.SessionRunning, base.ConfidenceSignal)
	var bell *SessionEvent
	for _, event := range h.state().Events {
		if event.Kind == base.SessionEventBell {
			bell = &event
		}
	}
	if bell == nil || bell.Reason != "after_input" {
		t.Fatalf("bell event = %+v, want one marked after_input", bell)
	}
}

func TestStateNotifyClipsText(t *testing.T) {
	h := newHarness(t)
	long := strings.Repeat("é", maxEventText)
	h.out(time.Second, "\x1b]777;notify;Build;"+long+"\a")
	got := h.expect(base.SessionNeedsMe, base.ConfidenceSignal)
	var notify *SessionEvent
	for i := range got.Events {
		if got.Events[i].Kind == base.SessionEventNotify {
			notify = &got.Events[i]
		}
	}
	if notify == nil {
		t.Fatalf("no notify event in %+v", got.Events)
	}
	if notify.Source != "osc777" || notify.Title != "Build" || !notify.TextTruncated || len(notify.Body) > maxEventText || !strings.HasPrefix(long, notify.Body) {
		t.Fatalf("notify = %+v", notify)
	}
}

func TestStateCarriesNotificationText(t *testing.T) {
	h := newHarness(t)
	h.out(time.Second, "\x1b]9;build finished\a")
	got := h.expect(base.SessionNeedsMe, base.ConfidenceSignal)
	if got.Reason != "notify" || got.Title != "" || got.Body != "build finished" {
		t.Fatalf("state = reason %q title %q body %q", got.Reason, got.Title, got.Body)
	}
	since := got.Since
	// A second notification of the same needs_me state replaces the text.
	h.out(time.Second, "\x1b]777;notify;Deploy;done\a")
	got = h.expect(base.SessionNeedsMe, base.ConfidenceSignal)
	if !got.Since.Equal(since) || got.Title != "Deploy" || got.Body != "done" {
		t.Fatalf("state = since %v title %q body %q", got.Since, got.Title, got.Body)
	}
	// A bell keeps the state but the text no longer describes it.
	h.out(5*time.Second, "\a")
	got = h.expect(base.SessionNeedsMe, base.ConfidenceSignal)
	if got.Reason != "bell" || got.Title != "" || got.Body != "" {
		t.Fatalf("after a bell: reason %q title %q body %q", got.Reason, got.Title, got.Body)
	}
	h.out(time.Second, "\x1b]9;again\a")
	h.in(time.Second)
	got = h.expect(base.SessionIdle, base.ConfidenceSignal)
	if got.Title != "" || got.Body != "" {
		t.Fatalf("idle state kept the text: title %q body %q", got.Title, got.Body)
	}
}

func TestStateCommandEndJustFinished(t *testing.T) {
	h := newHarness(t)
	h.out(time.Second, "\x1b]133;A\a$ \x1b]133;B\a")
	h.in(time.Second)
	h.out(10*time.Millisecond, "make\r\n\x1b]133;C\a")
	h.expect(base.SessionRunning, base.ConfidenceSignal)
	h.out(12*time.Second, "done\r\n\x1b]133;D;2\a")
	got := h.expect(base.SessionJustFinished, base.ConfidenceSignal)
	if got.ExitCode == nil || *got.ExitCode != 2 || !got.Notable || got.DurationMS != 12000 {
		t.Fatalf("just finished = %+v", got)
	}
	// The next prompt is drawn without leaving just_finished.
	h.out(5*time.Millisecond, "\x1b]133;A\a$ \x1b]133;B\a")
	h.idle()
	h.expect(base.SessionJustFinished, base.ConfidenceSignal)
	// A short command finishes too, but is not notable.
	h.in(time.Second)
	h.out(10*time.Millisecond, "ls\r\n\x1b]133;C\afile\r\n\x1b]133;D;0\a")
	got = h.expect(base.SessionJustFinished, base.ConfidenceSignal)
	if got.Notable || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("short command = %+v", got)
	}
	var ends int
	for _, event := range got.Events {
		if event.Kind == base.SessionEventCommandEnd {
			ends++
		}
	}
	if ends != 2 {
		t.Fatalf("command_end events = %d", ends)
	}
}

func TestStateActivityWhileSettlingArmsTheIdleInspection(t *testing.T) {
	h := newHarness(t)
	h.withRules(testRuleSet(t, bottomRule(`Overwrite file\?`, base.SessionNeedsMe)))
	h.out(time.Second, "compiling\r\n")
	if wait := h.tracker.tick(h.clock.advance(defaultStateIdle)); wait != defaultStateSettle-defaultStateIdle {
		t.Fatalf("settle wait %v", wait)
	}
	h.out(150*time.Millisecond, "Overwrite file? [y/N] ")
	due, ok := h.tracker.due()
	if want := h.clock.now.Add(defaultStateIdle); !ok || !due.Equal(want) {
		t.Fatalf("armed for %v (armed %v), want the idle inspection at %v", due, ok, want)
	}
	h.tracker.tick(due)
	h.expect(base.SessionNeedsMe, base.ConfidenceRule)
}

func TestStateRealTimerRuleAfterSettlingPause(t *testing.T) {
	changes := make(chan struct{}, 64)
	compiled, err := staterule.Compile(testRuleSet(t, bottomRule(`Overwrite file\?`, base.SessionNeedsMe)))
	if err != nil {
		t.Fatal(err)
	}
	tracker := newStateTracker(stateOptions{
		cols: 80, rows: 24, idle: 50 * time.Millisecond, settle: 3 * time.Second, timers: true,
		rules: func() *staterule.Compiled { return compiled },
		onChange: func() {
			select {
			case changes <- struct{}{}:
			default:
			}
		},
	}, time.Now())
	defer tracker.exit(nil, time.Now())
	tracker.output([]byte("compiling\r\n"), time.Now())
	// Let the idle inspection find no rule match so the session settles.
	time.Sleep(150 * time.Millisecond)
	start := time.Now()
	tracker.output([]byte("Overwrite file? [y/N] "), start)
	timeout := time.After(10 * time.Second)
	for {
		if state := tracker.snapshot(false, 0); state.State == base.SessionNeedsMe {
			// The settle deadline is 3 s away; the idle inspection 50 ms.
			if elapsed := time.Since(start); elapsed >= 1500*time.Millisecond {
				t.Fatalf("needs_me after %v, want the idle inspection", elapsed)
			}
			return
		}
		select {
		case <-changes:
		case <-timeout:
			t.Fatal("rule state never reported")
		}
	}
}

// runUntil feeds a tracker its ticks at the armed deadlines up to now.
func (h *trackerHarness) runUntil(now time.Time) {
	for {
		due, ok := h.tracker.due()
		if !ok || due.After(now) {
			break
		}
		h.clock.now = due
		h.tracker.tick(due)
	}
	h.clock.now = now
}

func TestStateHeldRuleClearsUnderContinuousOutput(t *testing.T) {
	h := newHarness(t)
	h.withRules(testRuleSet(t, bottomRule(`Continue\?`, base.SessionNeedsMe)))
	h.out(time.Second, "Continue? [y/N] ")
	h.idle()
	held := h.expect(base.SessionNeedsMe, base.ConfidenceRule)
	start := h.clock.now
	var running time.Time
	for i := 0; i < 300; i++ {
		h.runUntil(h.clock.now.Add(100 * time.Millisecond))
		h.tracker.output([]byte("\x1b[H\x1b[2Jbuilding step "+twoDigits(i%100)+"\r\n"), h.clock.now)
		if got := h.state(); running.IsZero() && got.State == base.SessionRunning {
			running = h.clock.now
		}
	}
	if running.IsZero() {
		t.Fatal("held needs_me never cleared under 30 s of continuous output")
	}
	if late := running.Sub(start); late > defaultStateSettle+defaultStateIdle {
		t.Fatalf("held rule cleared after %v", late)
	}
	got := h.expect(base.SessionRunning, base.ConfidenceSignal)
	if events := got.LastEventSeq - held.LastEventSeq; events != 1 {
		t.Fatalf("%d events while the output went on, want the one move to running", events)
	}
	h.runUntil(h.clock.now.Add(defaultStateSettle + time.Second))
	h.expect(base.SessionIdle, base.ConfidenceSignal)
}

func twoDigits(n int) string {
	return string([]byte{byte('0' + n/10%10), byte('0' + n%10)})
}

func TestStateExit(t *testing.T) {
	h := newHarness(t)
	h.out(time.Second, "Continue? [y/N] ")
	h.idle()
	code := 3
	h.tracker.exit(&code, h.clock.advance(time.Second))
	got := h.expect(base.SessionExited, base.ConfidenceSignal)
	if got.ExitCode == nil || *got.ExitCode != 3 || got.RuleID != "" {
		t.Fatalf("exited = %+v", got)
	}
	h.out(time.Second, "late output\a")
	h.in(time.Second)
	h.expect(base.SessionExited, base.ConfidenceSignal)
}

func TestStateSeenFollowsTheStateItNames(t *testing.T) {
	h := newHarness(t)
	h.out(time.Second, "waiting for approval\a")
	asked := h.expect(base.SessionNeedsMe, base.ConfidenceSignal)
	if asked.Seen {
		t.Fatal("a new bell is already seen")
	}
	if h.tracker.markSeen(asked.Since.Add(-time.Second)) || h.state().Seen {
		t.Fatal("seen applied to a state that began at another time")
	}
	changes := h.changes
	if !h.tracker.markSeen(asked.Since) || !h.state().Seen || h.changes != changes+1 {
		t.Fatalf("seen not recorded: %+v, changes %d -> %d", h.state(), changes, h.changes)
	}
	if h.tracker.markSeen(asked.Since) {
		t.Fatal("marking seen twice reported a change")
	}

	// Another bell asks again, although the state stays needs_me.
	h.out(2*time.Second, "\a")
	again := h.expect(base.SessionNeedsMe, base.ConfidenceSignal)
	if again.Seen {
		t.Fatalf("a new bell kept the seen mark: %+v", again)
	}

	// The next state starts unseen, and only needs_me and just_finished
	// can be seen.
	h.in(time.Second)
	idle := h.expect(base.SessionIdle, base.ConfidenceSignal)
	if idle.Seen || h.tracker.markSeen(idle.Since) {
		t.Fatalf("idle state took a seen mark: %+v", h.state())
	}
	h.out(time.Second, "\x1b]133;C\x07work\r\n\x1b]133;D;0\x07")
	finished := h.expect(base.SessionJustFinished, base.ConfidenceSignal)
	if !h.tracker.markSeen(finished.Since) || !h.state().Seen {
		t.Fatalf("just_finished not marked seen: %+v", h.state())
	}
}

func TestStateEventRingAndSequences(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < maxStateEvents+8; i++ {
		// Bells more than the scanner's rate limit apart.
		h.out(2*time.Second, "\a")
	}
	all := h.state()
	if len(all.Events) != maxStateEvents || !all.EventsDropped {
		t.Fatalf("events = %d, dropped %v", len(all.Events), all.EventsDropped)
	}
	for i := 1; i < len(all.Events); i++ {
		if all.Events[i].Seq <= all.Events[i-1].Seq {
			t.Fatalf("sequences not increasing: %d then %d", all.Events[i-1].Seq, all.Events[i].Seq)
		}
	}
	if all.LastEventSeq != all.Events[len(all.Events)-1].Seq {
		t.Fatalf("last seq %d, newest event %d", all.LastEventSeq, all.Events[len(all.Events)-1].Seq)
	}
	recent := h.tracker.snapshot(true, all.Events[len(all.Events)-3].Seq)
	if len(recent.Events) != 2 || recent.EventsDropped {
		t.Fatalf("events after seq: %d, dropped %v", len(recent.Events), recent.EventsDropped)
	}
	none := h.tracker.snapshot(false, 0)
	if none.Events != nil || none.EventsDropped {
		t.Fatal("snapshot without events carried events")
	}
}

func TestStateSequencesSharedAcrossSessions(t *testing.T) {
	seq := new(atomic.Uint64)
	a := newStateTracker(stateOptions{seq: seq}, stateEpoch0)
	b := newStateTracker(stateOptions{seq: seq}, stateEpoch0)
	a.output([]byte("\a"), stateEpoch0)
	b.output([]byte("\a"), stateEpoch0)
	if a.snapshot(false, 0).LastEventSeq >= b.snapshot(false, 0).LastEventSeq {
		t.Fatal("event sequences are not monotonic across sessions")
	}
}

func TestTrimListEventsDropsOldestAcrossSessions(t *testing.T) {
	event := func(seq uint64) SessionEvent {
		return SessionEvent{Seq: seq, Kind: base.SessionEventNotify, Body: strings.Repeat("x", 100)}
	}
	infos := []Info{
		{ID: "a", State: &SessionState{Events: []SessionEvent{event(1), event(3), event(5)}}},
		{ID: "b", State: &SessionState{Events: []SessionEvent{event(2), event(4), event(6)}}},
		{ID: "c"},
	}
	trimListEvents(infos, 1<<20)
	if len(infos[0].State.Events) != 3 || infos[0].State.EventsDropped {
		t.Fatal("events within the budget were trimmed")
	}
	trimListEvents(infos, 3*200)
	a, b := infos[0].State, infos[1].State
	if len(a.Events)+len(b.Events) > 3 || !a.EventsDropped || !b.EventsDropped {
		t.Fatalf("trimmed a=%+v b=%+v", a.Events, b.Events)
	}
	for _, state := range []*SessionState{a, b} {
		for _, event := range state.Events {
			if event.Seq < 4 {
				t.Fatalf("kept old event %d while dropping newer ones", event.Seq)
			}
		}
	}
}

func TestStreamStateKeepsReportingDroppedEvents(t *testing.T) {
	var stream Stream
	stream.applyStateLocked(SessionState{State: base.SessionRunning, EventsDropped: true, Events: []SessionEvent{{Seq: 1}}})
	stream.applyStateLocked(SessionState{State: base.SessionIdle, Events: []SessionEvent{{Seq: 2}}})
	if got := stream.SessionState(); !got.EventsDropped || len(got.Events) != 2 {
		t.Fatalf("after a reported drop: %+v", got)
	}

	var local Stream
	for seq := uint64(1); seq <= maxStateEvents+1; seq++ {
		local.applyStateLocked(SessionState{State: base.SessionRunning, Events: []SessionEvent{{Seq: seq}}})
	}
	if got := local.SessionState(); !got.EventsDropped || len(got.Events) != maxStateEvents || got.Events[0].Seq != 2 {
		t.Fatalf("after trimming the stream's own ring: dropped %v, %d events", got.EventsDropped, len(got.Events))
	}
}

func TestHelperStateDetectionOption(t *testing.T) {
	env := func(value string) func(string) string {
		return func(key string) string {
			if key == StateDetectionEnv {
				return value
			}
			return ""
		}
	}
	cases := []struct {
		name     string
		config   HelperConfig
		env      string
		disabled bool
	}{
		{name: "default on", disabled: false},
		{name: "option off", config: HelperConfig{DisableStateDetection: true}, disabled: true},
		{name: "env off overrides the default", env: "off", disabled: true},
		{name: "env on overrides the option", config: HelperConfig{DisableStateDetection: true}, env: "on", disabled: false},
		{name: "unrecognised env is ignored", config: HelperConfig{DisableStateDetection: true}, env: "maybe", disabled: true},
	}
	for _, tc := range cases {
		args := helperSpawnArgs("/state", tc.config)
		if args[0] != HelperCommand {
			t.Fatalf("%s: spawn args %q", tc.name, args)
		}
		config, err := parseHelperArgs(args[1:], env(tc.env))
		if err != nil || config.StateDir != "/state" || config.DisableStateDetection != tc.disabled {
			t.Fatalf("%s: helper config %+v, %v", tc.name, config, err)
		}
	}
	if _, err := parseHelperArgs([]string{"--state-dir", "/state", "--state-detection", "sometimes"}, nil); err == nil {
		t.Fatal("invalid --state-detection value accepted")
	}
	if config, err := parseHelperArgs([]string{"--state-dir", "/state", "--state-detection", "on"}, nil); err != nil || config.DisableStateDetection {
		t.Fatalf("--state-detection on: %+v, %v", config, err)
	}
}
