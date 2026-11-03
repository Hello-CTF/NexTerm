package alerts

import "testing"

func TestStepOffline(t *testing.T) {
	const (
		now = int64(10_000_000_000)
		s   = 5 * minMS
		a   = 10 * minMS
	)
	in := func(silenceMin int64, muted bool) OfflineInput {
		heard := now - silenceMin*minMS
		return OfflineInput{Now: now, LastHeard: heard, Effective: heard, Enabled: true, Muted: muted, S: s, A: a}
	}
	since := now - 10*minMS
	cases := []struct {
		name       string
		st         State
		in         OfflineInput
		wantStatus string
		wantEmit   string // "" none
		wantMuted  bool
		wantDur    int64
	}{
		{"ok quiet", State{Status: StatusOK}, in(2, false), StatusOK, "", false, 0},
		{"ok to suspect", State{Status: StatusOK}, in(5, false), StatusSuspect, "", false, 0},
		{"suspect below S resets", State{Status: StatusSuspect, Since: since}, in(3, false), StatusOK, "", false, 0},
		{"suspect holds", State{Status: StatusSuspect, Since: since}, in(7, false), StatusSuspect, "", false, 0},
		{"suspect fires", State{Status: StatusSuspect, Since: since}, in(10, false), StatusFiring, KindFired, false, 10 * minMS},
		{"suspect mutes", State{Status: StatusSuspect, Since: since}, in(10, true), StatusMuted, "", false, 0},
		{"muted holds", State{Status: StatusMuted, Since: since}, in(12, true), StatusMuted, "", false, 0},
		{"muted below A while unmuted holds", State{Status: StatusMuted, Since: since}, in(7, false), StatusMuted, "", false, 0},
		{"muted ends still offline fires", State{Status: StatusMuted, Since: since}, in(12, false), StatusFiring, KindFired, false, 10 * minMS},
		{"muted recovers", State{Status: StatusMuted, Since: since}, in(0, true), StatusOK, KindResolved, true, 10 * minMS},
		{"firing holds", State{Status: StatusFiring, Since: since, FiredAt: now - minMS}, in(20, false), StatusFiring, "", false, 0},
		{"firing resolves", State{Status: StatusFiring, Since: since, FiredAt: now - minMS}, in(0, false), StatusOK, KindResolved, false, 10 * minMS},
		{"firing resolves while muted is not muted event", State{Status: StatusFiring, Since: since}, in(0, true), StatusOK, KindResolved, false, 10 * minMS},
		{"disabled resets suspect", State{Status: StatusSuspect, Since: since}, func() OfflineInput { i := in(7, false); i.Enabled = false; return i }(), StatusOK, "", false, 0},
		{"disabled resets firing silently", State{Status: StatusFiring, Since: since}, func() OfflineInput { i := in(7, false); i.Enabled = false; return i }(), StatusOK, "", false, 0},
	}
	for _, c := range cases {
		got, emit := StepOffline(c.st, c.in)
		if got.Status != c.wantStatus {
			t.Errorf("%s: status = %s, want %s", c.name, got.Status, c.wantStatus)
		}
		switch {
		case c.wantEmit == "" && emit != nil:
			t.Errorf("%s: unexpected emit %+v", c.name, emit)
		case c.wantEmit != "" && emit == nil:
			t.Errorf("%s: missing emit", c.name)
		case emit != nil && (emit.Kind != c.wantEmit || emit.Muted != c.wantMuted || emit.DurationMS != c.wantDur):
			t.Errorf("%s: emit = %+v", c.name, emit)
		}
	}
}

func TestStepOfflineSinceAndRestart(t *testing.T) {
	const now = int64(10_000_000_000)
	// Silence starts from the real last-heard time even when the effective time is clamped.
	got, _ := StepOffline(State{Status: StatusOK}, OfflineInput{Now: now, LastHeard: now - 3*60*minMS, Effective: now - 6*minMS, Enabled: true, S: 5 * minMS, A: 10 * minMS})
	if got.Status != StatusSuspect || got.Since != now-3*60*minMS {
		t.Fatalf("state = %+v", got)
	}
	// After a restart a still-silent device keeps its alert: only a real heartbeat resolves it.
	firing := State{Status: StatusFiring, Since: now - 60*minMS, FiredAt: now - 50*minMS}
	got, emit := StepOffline(firing, OfflineInput{Now: now, LastHeard: now - 60*minMS, Effective: now - 2*minMS, Enabled: true, S: 5 * minMS, A: 10 * minMS})
	if emit != nil || got.Status != StatusFiring {
		t.Fatalf("restart resolved a silent device: %+v %+v", got, emit)
	}
	// The resolve keeps no cooldown base for offline beyond ResolvedAt bookkeeping.
	got, emit = StepOffline(firing, OfflineInput{Now: now, LastHeard: now, Effective: now, Enabled: true, S: 5 * minMS, A: 10 * minMS})
	if emit == nil || got.Status != StatusOK || got.ResolvedAt != now || got.FiredAt != 0 || got.Since != 0 {
		t.Fatalf("resolve = %+v %+v", got, emit)
	}
}
