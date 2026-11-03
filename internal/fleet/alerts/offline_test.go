package alerts

import "testing"

func TestStepOffline(t *testing.T) {
	const (
		now = int64(10_000_000_000)
		s   = 5 * minMS
		a   = 10 * minMS
		g   = 150_000
	)
	in := func(silenceMin int64, muted bool) OfflineInput {
		heard := now - silenceMin*minMS
		return OfflineInput{Now: now, LastHeard: heard, Effective: heard, Enabled: true, Muted: muted, S: s, A: a, G: g}
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
		{"muted recovers only after holding", State{Status: StatusMuted, Since: since}, in(0, true), StatusMuted, "", false, 0},
		{"firing holds", State{Status: StatusFiring, Since: since, FiredAt: now - minMS}, in(20, false), StatusFiring, "", false, 0},
		{"firing starts recovering", State{Status: StatusFiring, Since: since, FiredAt: now - minMS}, in(0, false), StatusFiring, "", false, 0},
		{"firing resolves after the hold", State{Status: StatusFiring, Since: since, FiredAt: now - 9*minMS, RecoveringSince: now - RecoveryHold.Milliseconds()}, in(0, false), StatusOK, KindResolved, false, 5 * minMS},
		{"firing resolves while muted is not muted event", State{Status: StatusFiring, Since: since, RecoveringSince: now - RecoveryHold.Milliseconds()}, in(0, true), StatusOK, KindResolved, false, 5 * minMS},
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
	firing.RecoveringSince = now - RecoveryHold.Milliseconds()
	got, emit = StepOffline(firing, OfflineInput{Now: now, LastHeard: now, Effective: now, Enabled: true, S: 5 * minMS, A: 10 * minMS, G: 150_000})
	if emit == nil || got.Status != StatusOK || got.ResolvedAt != now || got.FiredAt != 0 || got.Since != 0 {
		t.Fatalf("resolve = %+v %+v", got, emit)
	}
}

func TestStepOfflineDormant(t *testing.T) {
	const (
		now = int64(10_000_000_000)
		s   = 5 * minMS
		a   = 10 * minMS
	)
	started := now - 60*minMS
	in := func(heard int64, enabled bool) OfflineInput {
		return OfflineInput{Now: now, LastHeard: heard, Effective: max(heard, started), Started: started, Enabled: enabled, S: s, A: a}
	}
	longGone := started - 30*24*60*minMS
	cases := []struct {
		name       string
		st         State
		in         OfflineInput
		wantStatus string
	}{
		{"ok long gone goes dormant", State{Status: StatusOK}, in(longGone, true), StatusDormant},
		{"just beyond A before start goes dormant", State{Status: StatusOK}, in(started-a-1, true), StatusDormant},
		{"within A before start is normal", State{Status: StatusOK}, in(started-a+1, true), StatusSuspect},
		{"dormant stays while silent", State{Status: StatusDormant}, in(longGone, true), StatusDormant},
		{"dormant never escalates", State{Status: StatusDormant}, in(started-a-1, true), StatusDormant},
		{"dormant heard again returns to ok", State{Status: StatusDormant}, in(now-minMS, true), StatusOK},
		{"dormant disabled resets", State{Status: StatusDormant}, in(longGone, false), StatusOK},
		{"persisted firing is unaffected", State{Status: StatusFiring, Since: longGone}, in(longGone, true), StatusFiring},
		{"persisted suspect is unaffected", State{Status: StatusSuspect, Since: longGone}, in(longGone, true), StatusFiring},
	}
	for _, c := range cases {
		got, emit := StepOffline(c.st, c.in)
		if got.Status != c.wantStatus {
			t.Errorf("%s: status = %s, want %s", c.name, got.Status, c.wantStatus)
		}
		if emit != nil && c.wantStatus != StatusFiring {
			t.Errorf("%s: unexpected emit %+v", c.name, emit)
		}
	}
}

func TestStepOfflineRecoveryHold(t *testing.T) {
	const (
		now = int64(10_000_000_000)
		s   = 5 * minMS
		a   = 10 * minMS
		g   = int64(150_000)
	)
	hold := RecoveryHold.Milliseconds()
	since := now - 60*minMS
	in := func(heardAgo int64, muted bool) OfflineInput {
		heard := now - heardAgo
		return OfflineInput{Now: now, LastHeard: heard, Effective: heard, Enabled: true, Muted: muted, S: s, A: a, G: g}
	}
	cases := []struct {
		name     string
		st       State
		in       OfflineInput
		want     string
		wantRec  int64
		wantEmit bool
		wantDur  int64
		wantMute bool
	}{
		{"firing heard starts the hold", State{Status: StatusFiring, Since: since}, in(0, false), StatusFiring, now, false, 0, false},
		{"muted heard starts the hold", State{Status: StatusMuted, Since: since}, in(0, true), StatusMuted, now, false, 0, false},
		{"hold keeps its start", State{Status: StatusFiring, Since: since, RecoveringSince: now - 2*minMS}, in(0, false), StatusFiring, now - 2*minMS, false, 0, false},
		{"gap beyond G clears the hold", State{Status: StatusFiring, Since: since, RecoveringSince: now - 2*minMS}, in(g+1, false), StatusFiring, 0, false, 0, false},
		{"heard exactly G keeps the hold", State{Status: StatusFiring, Since: since, RecoveringSince: now - 2*minMS}, in(g, false), StatusFiring, now - 2*minMS, false, 0, false},
		{"just short of the hold", State{Status: StatusFiring, Since: since, RecoveringSince: now - hold + 1}, in(0, false), StatusFiring, now - hold + 1, false, 0, false},
		{"hold elapsed resolves", State{Status: StatusFiring, Since: since, RecoveringSince: now - hold}, in(0, false), StatusOK, 0, true, now - hold - since, false},
		{"muted hold elapsed resolves as muted", State{Status: StatusMuted, Since: since, RecoveringSince: now - hold}, in(0, true), StatusOK, 0, true, now - hold - since, true},
		{"muted does not escalate while recovering", State{Status: StatusMuted, Since: since, RecoveringSince: now - minMS}, in(0, false), StatusMuted, now - minMS, false, 0, false},
		{"muted unmuted after the hold broke fires", State{Status: StatusMuted, Since: since, RecoveringSince: now - minMS}, in(a+minMS, false), StatusFiring, 0, true, now - since, false},
	}
	for _, c := range cases {
		got, emit := StepOffline(c.st, c.in)
		if got.Status != c.want || got.RecoveringSince != c.wantRec {
			t.Errorf("%s: state = %+v, want %s/%d", c.name, got, c.want, c.wantRec)
		}
		if (emit != nil) != c.wantEmit {
			t.Errorf("%s: emit = %+v", c.name, emit)
			continue
		}
		if emit != nil && c.want == StatusOK && (emit.Kind != KindResolved || emit.DurationMS != c.wantDur || emit.Muted != c.wantMute) {
			t.Errorf("%s: emit = %+v", c.name, emit)
		}
	}
}

func TestStepOfflineQuietEnd(t *testing.T) {
	const (
		now = int64(10_000_000_000)
		s   = 5 * minMS
		a   = 10 * minMS
	)
	in := func(silenceMin, quietEndAgoMin int64) OfflineInput {
		heard := now - silenceMin*minMS
		return OfflineInput{Now: now, LastHeard: heard, Effective: heard, QuietEnd: now - quietEndAgoMin*minMS, Enabled: true, S: s, A: a, G: 150_000}
	}
	since := now - 10*minMS
	cases := []struct {
		name       string
		st         State
		in         OfflineInput
		wantStatus string
		wantEmit   bool
	}{
		{"muted waits A after the quiet end", State{Status: StatusMuted, Since: since}, in(30, 3), StatusMuted, false},
		{"muted fires A after the quiet end", State{Status: StatusMuted, Since: since}, in(30, 10), StatusFiring, true},
		{"suspect waits A after the quiet end", State{Status: StatusSuspect, Since: since}, in(7, 3), StatusSuspect, false},
		{"suspect fires A after the quiet end", State{Status: StatusSuspect, Since: since}, in(12, 10), StatusFiring, true},
		{"suspect is not reset by the quiet end", State{Status: StatusSuspect, Since: since}, in(7, 0), StatusSuspect, false},
		{"ok enters suspect by S regardless of the quiet end", State{Status: StatusOK}, in(5, 0), StatusSuspect, false},
		{"no quiet end keeps the old timing", State{Status: StatusSuspect, Since: since}, func() OfflineInput { i := in(10, 0); i.QuietEnd = 0; return i }(), StatusFiring, true},
	}
	for _, c := range cases {
		got, emit := StepOffline(c.st, c.in)
		if got.Status != c.wantStatus || (emit != nil) != c.wantEmit {
			t.Errorf("%s: %+v %+v, want %s emit=%v", c.name, got, emit, c.wantStatus, c.wantEmit)
		}
	}
}
