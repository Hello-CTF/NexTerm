package supervisor

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/pty"
	"github.com/Hello-CTF/NexTerm/internal/terminal/shape"
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
	fg      pty.Foreground
	fgErr   error
}

func newHarness(t *testing.T) *trackerHarness {
	t.Helper()
	h := &trackerHarness{t: t, clock: &fakeClock{now: stateEpoch0}}
	h.fg = pty.Foreground{LeaderPID: 10, ProcessGroup: 20, Canonical: true, CanonicalKnown: true}
	h.tracker = newStateTracker(stateOptions{
		cols: 80, rows: 24,
		foreground: func() (pty.Foreground, error) { return h.fg, h.fgErr },
		seq:        new(atomic.Uint64),
		onChange:   func() { h.changes++ },
	}, h.clock.now)
	return h
}

func (h *trackerHarness) out(after time.Duration, data string) {
	h.tracker.output([]byte(data), h.clock.advance(after))
}

func (h *trackerHarness) in(after time.Duration) {
	h.tracker.input(h.clock.advance(after))
}

// idle advances past the idle threshold and runs the idle transition.
func (h *trackerHarness) idle() {
	h.t.Helper()
	if wait := h.tracker.tick(h.clock.advance(defaultStateIdle)); wait != 0 {
		h.t.Fatalf("tick after the idle threshold asked to wait %v", wait)
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
	got := h.expect(base.SessionIdle, base.ConfidenceSignal)
	if got.Prompt != nil {
		t.Fatalf("unexpected prompt %+v", got.Prompt)
	}
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
	epoch := got.PromptEpoch
	h.in(time.Second)
	got = h.expect(base.SessionIdle, base.ConfidenceSignal)
	if got.PromptEpoch == epoch {
		t.Fatal("input did not advance the prompt epoch")
	}
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

func TestStateHeuristicPrompt(t *testing.T) {
	h := newHarness(t)
	h.out(time.Second, "Overwrite config.yaml? [y/N] ")
	h.expect(base.SessionRunning, base.ConfidenceSignal)
	if h.state().Prompt != nil {
		t.Fatal("prompt reported while output is active")
	}
	h.idle()
	got := h.expect(base.SessionNeedsMe, base.ConfidenceHeuristic)
	if got.Prompt == nil || got.Prompt.Kind != string(shape.KindYesNo) || got.Prompt.Epoch != got.PromptEpoch {
		t.Fatalf("prompt = %+v", got.Prompt)
	}
	epoch := got.PromptEpoch
	// A redraw of the same prompt makes the session run again and hides
	// the prompt until the next idle inspection, which keeps the epoch.
	h.out(time.Second, "\r\x1b[KOverwrite config.yaml? [y/N] ")
	running := h.expect(base.SessionRunning, base.ConfidenceSignal)
	if running.Prompt != nil {
		t.Fatal("stale prompt published while output is active")
	}
	h.idle()
	got = h.expect(base.SessionNeedsMe, base.ConfidenceHeuristic)
	if got.PromptEpoch != epoch {
		t.Fatalf("redraw of an identical prompt changed the epoch %d -> %d", epoch, got.PromptEpoch)
	}
}

func TestStatePromptEpochRules(t *testing.T) {
	h := newHarness(t)
	h.out(time.Second, "Delete 3 files? [y/N] ")
	h.idle()
	first := h.state().PromptEpoch

	// Any input advances the epoch, even with the prompt unchanged.
	h.in(time.Second)
	afterInput := h.state()
	if afterInput.PromptEpoch <= first || afterInput.Prompt != nil {
		t.Fatalf("after input epoch %d (was %d), prompt %+v", afterInput.PromptEpoch, first, afterInput.Prompt)
	}
	h.idle()
	stillThere := h.expect(base.SessionNeedsMe, base.ConfidenceHeuristic)
	if stillThere.PromptEpoch != afterInput.PromptEpoch {
		t.Fatal("reinspecting an unchanged prompt after input changed the epoch again")
	}

	// The answer is echoed, the prompt disappears: none advances the epoch.
	h.out(10*time.Millisecond, "y\r\nDeleted.\r\n$ ")
	h.idle()
	gone := h.expect(base.SessionIdle, base.ConfidenceSignal)
	if gone.PromptEpoch <= stillThere.PromptEpoch {
		t.Fatal("prompt disappearing did not advance the epoch")
	}

	// An identical prompt shown again gets a new epoch.
	h.out(time.Second, "\r\nDelete 3 files? [y/N] ")
	h.idle()
	again := h.expect(base.SessionNeedsMe, base.ConfidenceHeuristic)
	if again.PromptEpoch <= gone.PromptEpoch || again.Prompt.Fingerprint != stillThere.Prompt.Fingerprint {
		t.Fatalf("identical prompt: epoch %d after %d, fingerprints %s/%s", again.PromptEpoch, gone.PromptEpoch, again.Prompt.Fingerprint, stillThere.Prompt.Fingerprint)
	}

	// A changed prompt without input in between advances the epoch.
	h.out(time.Second, "\r\x1b[KDelete 4 files? [y/N] ")
	h.idle()
	changed := h.state()
	if changed.PromptEpoch <= again.PromptEpoch || changed.Prompt.Fingerprint == again.Prompt.Fingerprint {
		t.Fatal("fingerprint change did not advance the epoch")
	}
}

func TestStateShellPromptSuppressesHeuristic(t *testing.T) {
	h := newHarness(t)
	h.out(time.Second, "\x1b]133;A\aproceed? [y/N] \x1b]133;B\a")
	h.idle()
	got := h.expect(base.SessionIdle, base.ConfidenceSignal)
	if got.Prompt == nil || got.Prompt.Tappable || got.Prompt.Blocked != base.TapBlockedShellPrompt {
		t.Fatalf("prompt at the shell prompt = %+v", got.Prompt)
	}
}

func TestStateExit(t *testing.T) {
	h := newHarness(t)
	h.out(time.Second, "Continue? [y/N] ")
	h.idle()
	epoch := h.state().PromptEpoch
	code := 3
	h.tracker.exit(&code, h.clock.advance(time.Second))
	got := h.expect(base.SessionExited, base.ConfidenceSignal)
	if got.ExitCode == nil || *got.ExitCode != 3 || got.Prompt != nil || got.PromptEpoch <= epoch {
		t.Fatalf("exited = %+v (epoch before %d)", got, epoch)
	}
	h.out(time.Second, "late output\a")
	h.in(time.Second)
	h.expect(base.SessionExited, base.ConfidenceSignal)
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

func TestStateCurrentPromptReinspects(t *testing.T) {
	h := newHarness(t)
	h.out(time.Second, "Apply? [y/N] ")
	if prompt := h.tracker.currentPrompt(); prompt == nil || prompt.Kind != string(shape.KindYesNo) {
		t.Fatalf("current prompt = %+v", prompt)
	}
	h.out(0, "\r\n$ ")
	if prompt := h.tracker.currentPrompt(); prompt != nil {
		t.Fatalf("current prompt after it scrolled away = %+v", prompt)
	}
}

func TestStateForegroundUnknownBlocksTaps(t *testing.T) {
	h := newHarness(t)
	h.fgErr = pty.ErrForegroundUnsupported
	h.out(time.Second, "Apply? [y/N] ")
	h.idle()
	got := h.expect(base.SessionNeedsMe, base.ConfidenceHeuristic)
	if got.Prompt.Tappable || got.Prompt.Blocked != base.TapBlockedForegroundUnknown {
		t.Fatalf("prompt = %+v", got.Prompt)
	}
}

func TestDescribePromptTapRules(t *testing.T) {
	yesNo := shape.DetectLines([]string{"Proceed? [Y/n] "}, 0, 16)
	marked := shape.DetectLines([]string{"Pick one:", "❯ 1. Alpha", "  2. Beta", "  3. Gamma"}, 0, 9)
	weak := shape.DetectLines([]string{"Pick one:", "> Alpha", "  Beta"}, 0, 9)
	numbered := shape.DetectLines([]string{"1) Alpha", "2) Beta", "", "Select:"}, 3, 8)
	question := shape.DetectLines([]string{"Password: "}, 0, 10)
	for name, p := range map[string]shape.Prompt{"yesno": yesNo, "marked": marked, "weak": weak, "numbered": numbered, "question": question} {
		if p.Kind == shape.KindNone {
			t.Fatalf("fixture %s detected no prompt", name)
		}
	}
	if !weak.WeakMarker || marked.WeakMarker || marked.CursorIndex != 0 || numbered.CursorIndex != -1 {
		t.Fatalf("fixture shapes: weak %+v marked %+v numbered %+v", weak, marked, numbered)
	}
	line := tapContext{foregroundKnown: true, canonical: true, canonicalKnown: true}
	raw := tapContext{foregroundKnown: true, canonicalKnown: true}
	unknown := tapContext{foregroundKnown: true}

	cases := []struct {
		name    string
		prompt  shape.Prompt
		ctx     tapContext
		blocked string
		submit  string
		sends   []string
	}{
		{name: "question never", prompt: question, ctx: line, blocked: base.TapBlockedQuestion},
		{name: "shell prompt", prompt: yesNo, ctx: tapContext{atShellPrompt: true, foregroundKnown: true, canonicalKnown: true, canonical: true}, blocked: base.TapBlockedShellPrompt},
		{name: "foreground unknown", prompt: yesNo, ctx: tapContext{}, blocked: base.TapBlockedForegroundUnknown},
		{name: "shell foreground", prompt: marked, ctx: tapContext{foregroundKnown: true, shellForeground: true}, blocked: base.TapBlockedShellForeground},
		{name: "yesno line mode", prompt: yesNo, ctx: line, submit: base.TapSubmitEnter, sends: []string{"y\r", "n\r"}},
		{name: "yesno raw mode", prompt: yesNo, ctx: raw, submit: base.TapSubmitNone, sends: []string{"y", "n"}},
		{name: "yesno mode unknown", prompt: yesNo, ctx: unknown, submit: base.TapSubmitSeparate, sends: []string{"y", "n"}},
		{name: "marked arrows", prompt: marked, ctx: raw, submit: base.TapSubmitEnter, sends: []string{"\r", "\x1b[B\r", "\x1b[B\x1b[B\r"}},
		{name: "marked arrows DECCKM", prompt: marked, ctx: tapContext{foregroundKnown: true, appCursor: true}, submit: base.TapSubmitEnter, sends: []string{"\r", "\x1bOB\r", "\x1bOB\x1bOB\r"}},
		{name: "weak marker unnumbered", prompt: weak, ctx: line, blocked: base.TapBlockedWeakMarker},
		{name: "numbered line mode", prompt: numbered, ctx: line, submit: base.TapSubmitEnter, sends: []string{"1\r", "2\r"}},
		{name: "numbered raw mode", prompt: numbered, ctx: raw, blocked: base.TapBlockedNotLineMode},
		{name: "numbered mode unknown", prompt: numbered, ctx: unknown, blocked: base.TapBlockedNotLineMode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := describePrompt(tc.prompt, tc.ctx)
			if tc.blocked != "" {
				if info.Tappable || info.Blocked != tc.blocked {
					t.Fatalf("tappable %v blocked %q, want blocked %q", info.Tappable, info.Blocked, tc.blocked)
				}
				for _, option := range info.Options {
					if option.Send != "" {
						t.Fatalf("blocked prompt carries input %q", option.Send)
					}
				}
				return
			}
			if !info.Tappable || info.Submit != tc.submit {
				t.Fatalf("tappable %v submit %q blocked %q, want submit %q", info.Tappable, info.Submit, info.Blocked, tc.submit)
			}
			if len(info.Options) != len(tc.sends) {
				t.Fatalf("options %+v", info.Options)
			}
			for i, want := range tc.sends {
				if info.Options[i].Send != want {
					t.Fatalf("option %d sends %q, want %q", i, info.Options[i].Send, want)
				}
			}
			if (tc.submit == base.TapSubmitSeparate) != (info.EnterKey == enterKey) {
				t.Fatalf("separate Enter key %q for submit %q", info.EnterKey, info.Submit)
			}
		})
	}
	// Moving up from a selection below uses the up arrow.
	below := shape.DetectLines([]string{"Pick one:", "  1. Alpha", "  2. Beta", "❯ 3. Gamma"}, 0, 9)
	info := describePrompt(below, raw)
	if info.Options[0].Send != "\x1b[A\x1b[A\r" {
		t.Fatalf("up navigation sends %q", info.Options[0].Send)
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
