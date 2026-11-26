package selector

import (
	"testing"
	"time"
)

var epoch = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

type step struct {
	at     time.Duration
	sample Sample
	want   Action
	path   Path
}

func ok(rtt time.Duration) Sample {
	return Sample{LegUp: true, Ping: true, PingOK: true, PingRTT: rtt, RelayRTT: 40 * time.Millisecond, Mode: ModeAuto}
}

func lost() Sample {
	return Sample{LegUp: true, Ping: true, RelayRTT: 40 * time.Millisecond, Mode: ModeAuto}
}

func withMode(s Sample, mode Mode) Sample { s.Mode = mode; return s }

func withLoss(s Sample, loss float64) Sample { s.HasLoss, s.Loss = true, loss; return s }

func run(t *testing.T, sel *Selector, steps []step) {
	t.Helper()
	for i, st := range steps {
		got := sel.Decide(epoch.Add(st.at), st.sample)
		if got != st.want || sel.Path() != st.path {
			t.Fatalf("step %d at %s: action %s path %s, want %s path %s", i, st.at, got, sel.Path(), st.want, st.path)
		}
	}
}

func TestSelector(t *testing.T) {
	fast := 10 * time.Millisecond
	cases := []struct {
		name  string
		steps []step
	}{
		{"first answered ping switches to direct", []step{
			{0, Sample{LegUp: true, Mode: ModeAuto}, Stay, PathRelay},
			{time.Second, ok(fast), UseDirect, PathDirect},
			{2 * time.Second, ok(fast), Stay, PathDirect},
		}},
		{"a first leg slower than the relay stays on the relay", []step{
			{0, ok(300 * time.Millisecond), Stay, PathRelay},
		}},
		{"three lost pings leave direct at once, two do not", []step{
			{0, ok(fast), UseDirect, PathDirect},
			{time.Second, lost(), Stay, PathDirect},
			{2 * time.Second, lost(), Stay, PathDirect},
			{3 * time.Second, ok(fast), Stay, PathDirect},
			{4 * time.Second, lost(), Stay, PathDirect},
			{5 * time.Second, lost(), Stay, PathDirect},
			{6 * time.Second, lost(), UseRelay, PathRelay},
		}},
		{"a dead leg closes at once", []step{
			{0, ok(fast), UseDirect, PathDirect},
			{time.Second, Sample{Mode: ModeAuto}, CloseLeg, PathRelay},
			{2 * time.Second, Sample{Mode: ModeAuto}, Stay, PathRelay},
		}},
		{"loss above 8% degrades only after the dwell", []step{
			{0, ok(fast), UseDirect, PathDirect},
			{30 * time.Second, withLoss(ok(fast), 0.2), Stay, PathDirect},
			{59 * time.Second, withLoss(ok(fast), 0.2), Stay, PathDirect},
			{60 * time.Second, withLoss(ok(fast), 0.2), UseRelay, PathRelay},
		}},
		{"loss at the threshold is not a degradation", []step{
			{0, ok(fast), UseDirect, PathDirect},
			{90 * time.Second, withLoss(ok(fast), 0.08), Stay, PathDirect},
		}},
		{"a slower direct RTT must last 15 s", []step{
			{0, ok(fast), UseDirect, PathDirect},
			{61 * time.Second, ok(200 * time.Millisecond), Stay, PathDirect},
			{75 * time.Second, ok(200 * time.Millisecond), Stay, PathDirect},
			{76 * time.Second, ok(200 * time.Millisecond), UseRelay, PathRelay},
		}},
		{"80 ms worse but under 1.5x is not slower", []step{
			{0, Sample{LegUp: true, Ping: true, PingOK: true, PingRTT: 10 * time.Millisecond, RelayRTT: 200 * time.Millisecond, Mode: ModeAuto}, UseDirect, PathDirect},
			{61 * time.Second, Sample{LegUp: true, Ping: true, PingOK: true, PingRTT: 290 * time.Millisecond, RelayRTT: 200 * time.Millisecond, Mode: ModeAuto}, Stay, PathDirect},
			{90 * time.Second, Sample{LegUp: true, Ping: true, PingOK: true, PingRTT: 290 * time.Millisecond, RelayRTT: 200 * time.Millisecond, Mode: ModeAuto}, Stay, PathDirect},
		}},
		{"a return needs 30 s healthy and the dwell", []step{
			{0, ok(fast), UseDirect, PathDirect},
			{time.Second, lost(), Stay, PathDirect},
			{2 * time.Second, lost(), Stay, PathDirect},
			{3 * time.Second, lost(), UseRelay, PathRelay},
			{4 * time.Second, ok(fast), Stay, PathRelay},
			{34 * time.Second, ok(fast), Stay, PathRelay},
			{62 * time.Second, ok(fast), Stay, PathRelay},
			{63 * time.Second, ok(fast), UseDirect, PathDirect},
		}},
		{"a lost ping restarts the healthy window", []step{
			{0, ok(fast), UseDirect, PathDirect},
			{time.Second, lost(), Stay, PathDirect},
			{2 * time.Second, lost(), Stay, PathDirect},
			{3 * time.Second, lost(), UseRelay, PathRelay},
			{10 * time.Second, ok(fast), Stay, PathRelay},
			{50 * time.Second, lost(), Stay, PathRelay},
			{70 * time.Second, ok(fast), Stay, PathRelay},
			{99 * time.Second, ok(fast), Stay, PathRelay},
			{100 * time.Second, ok(fast), UseDirect, PathDirect},
		}},
		{"relay mode always closes the leg", []step{
			{0, ok(fast), UseDirect, PathDirect},
			{time.Second, withMode(ok(fast), ModeRelay), CloseLeg, PathRelay},
		}},
		{"direct mode ignores loss and dwell but not a dead path", []step{
			{0, withMode(ok(fast), ModeDirect), UseDirect, PathDirect},
			{time.Second, withMode(withLoss(ok(fast), 0.5), ModeDirect), Stay, PathDirect},
			{90 * time.Second, withMode(withLoss(ok(500*time.Millisecond), 0.5), ModeDirect), Stay, PathDirect},
			{91 * time.Second, withMode(lost(), ModeDirect), Stay, PathDirect},
			{92 * time.Second, withMode(lost(), ModeDirect), Stay, PathDirect},
			{93 * time.Second, withMode(lost(), ModeDirect), UseRelay, PathRelay},
			{94 * time.Second, withMode(ok(fast), ModeDirect), UseDirect, PathDirect},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run(t, &Selector{}, tc.steps)
		})
	}
}

// Three quality degradations within ten minutes pin the device to the relay for thirty minutes; direct mode is
// not pinned.
func TestSelectorPinsAfterRepeatedDegradation(t *testing.T) {
	sel := &Selector{}
	fast := 10 * time.Millisecond
	at := time.Duration(0)
	cycle := func(last bool) {
		t.Helper()
		run(t, sel, []step{{at, ok(fast), UseDirect, PathDirect}})
		want := UseRelay
		if last {
			want = CloseLeg
		}
		run(t, sel, []step{{at + MinDwell, withLoss(ok(fast), 0.5), want, PathRelay}})
		at += MinDwell
	}
	// The first switch needs no healthy window; later ones do.
	cycle(false)
	run(t, sel, []step{{at + time.Second, ok(fast), Stay, PathRelay}})
	at += time.Second + RecoverHealthy + MinDwell
	cycle(false)
	run(t, sel, []step{{at + time.Second, ok(fast), Stay, PathRelay}})
	at += time.Second + RecoverHealthy + MinDwell
	if at >= PinWindow {
		t.Fatalf("test cycles take %s, longer than the pin window", at)
	}
	cycle(true)
	pinnedAt := at
	if !sel.Pinned(epoch.Add(pinnedAt)) || sel.PinnedUntil() != epoch.Add(pinnedAt+PinDuration) {
		t.Fatalf("pinned until %s, want %s", sel.PinnedUntil(), epoch.Add(pinnedAt+PinDuration))
	}
	run(t, sel, []step{
		{pinnedAt + time.Minute, ok(fast), CloseLeg, PathRelay},
		{pinnedAt + PinDuration - time.Second, ok(fast), CloseLeg, PathRelay},
		// Direct mode overrides the pin.
		{pinnedAt + PinDuration - time.Millisecond, withMode(ok(fast), ModeDirect), UseDirect, PathDirect},
	})
	sel2 := &Selector{pinnedTill: epoch.Add(time.Hour), everDirect: true}
	run(t, sel2, []step{
		{0, ok(fast), CloseLeg, PathRelay},
		{time.Hour, ok(fast), Stay, PathRelay},
	})
	if sel2.Pinned(epoch.Add(time.Hour)) {
		t.Fatal("the pin did not end")
	}
}

// Leaving the leg for an outside reason is not a degradation: no pin, and the next leg returns after the
// healthy window and the dwell.
func TestSelectorLeave(t *testing.T) {
	sel := &Selector{}
	fast := 10 * time.Millisecond
	run(t, sel, []step{{0, ok(fast), UseDirect, PathDirect}})
	for i := range PinDegrades + 1 {
		sel.Leave(epoch.Add(time.Duration(i) * time.Second))
		if sel.Path() != PathRelay || sel.Reason() != "" || sel.Pinned(epoch.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("after leave %d: path %s reason %q pinned %v", i, sel.Path(), sel.Reason(), sel.Pinned(epoch))
		}
	}
	at := time.Duration(PinDegrades) * time.Second
	run(t, sel, []step{
		{at + time.Second, ok(fast), Stay, PathRelay},
		{at + time.Second + RecoverHealthy, ok(fast), Stay, PathRelay},
		{at + MinDwell, ok(fast), UseDirect, PathDirect},
	})
}

func TestParseMode(t *testing.T) {
	for text, want := range map[string]Mode{"": ModeAuto, "auto": ModeAuto, "relay": ModeRelay, "direct": ModeDirect, "bogus": ModeAuto} {
		if got := ParseMode(text); got != want {
			t.Errorf("ParseMode(%q) = %q, want %q", text, got, want)
		}
	}
}
