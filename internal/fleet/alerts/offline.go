package alerts

import "time"

const (
	// DefaultOfflineMinutes is X: silence after which a device counts as offline.
	DefaultOfflineMinutes int64 = 10
	MinOfflineMinutes     int64 = 1
	MaxOfflineMinutes     int64 = 1440

	// Kinds of device that carry a device_agent row.
	deviceKindLocalHelper = "local-helper"
)

// SuspectAfter is S = 3*I + 2 min: the silence that makes a device suspect.
func SuspectAfter(interval time.Duration) time.Duration { return 3*interval + 2*time.Minute }

// ResolveOffline returns the effective enabled flag and alert delay of the offline rule for a device
// of the given kind. Local helpers go offline whenever the desktop app closes, so they default to off.
// An out-of-range OfflineMinutes falls back to the default.
func ResolveOffline(kind string, s *Settings) (enabled bool, minutes int64) {
	enabled, minutes = kind != deviceKindLocalHelper, DefaultOfflineMinutes
	if s == nil {
		return
	}
	if s.OfflineEnabled != nil {
		enabled = *s.OfflineEnabled
	}
	if m := s.OfflineMinutes; m != nil && *m >= MinOfflineMinutes && *m <= MaxOfflineMinutes {
		minutes = *m
	}
	return
}

// OfflineInput feeds one evaluation into StepOffline. Durations and times are in milliseconds.
type OfflineInput struct {
	Now int64
	// LastHeard is the real time the device was last heard from.
	LastHeard int64
	// Effective is max(LastHeard, evaluator start) so server downtime never counts as silence.
	Effective int64
	// Started is the evaluator start time.
	Started int64
	// QuietEnd is the latest quiet-hours end (0 when quiet hours are off): reaching A is timed from it
	// so a device does not alert the moment quiet hours end.
	QuietEnd int64
	Enabled  bool
	Muted    bool
	// S is the suspect threshold and A (>= S) the alert threshold.
	S, A int64
	// G is the gap tolerance: hearing a device within G keeps a recovery hold running.
	G int64
}

// OfflineEmit describes the event an offline transition produces.
type OfflineEmit struct {
	Kind       string
	DurationMS int64
	Muted      bool
}

// StepOffline applies the offline-rule transition table. It is pure: no clock, no storage.
//
// Escalation (ok -> suspect -> firing/muted) measures silence from Effective. Recovery measures it
// from the real LastHeard: after a restart Effective is the start time, which would otherwise make a
// device that is still silent look recovered and re-fire later.
func StepOffline(st State, in OfflineInput) (State, *OfflineEmit) {
	if !in.Enabled {
		if st.Status != StatusOK {
			st = State{Status: StatusOK, ResolvedAt: st.ResolvedAt}
		}
		return st, nil
	}
	silence := in.Now - in.Effective
	silenceA := in.Now - max(in.Effective, in.QuietEnd)
	heardAgo := in.Now - in.LastHeard
	switch st.Status {
	case StatusDormant:
		if heardAgo < in.S {
			return State{Status: StatusOK, ResolvedAt: st.ResolvedAt}, nil
		}
	case StatusSuspect:
		switch {
		case silence < in.S:
			return State{Status: StatusOK, ResolvedAt: st.ResolvedAt}, nil
		case silenceA >= in.A:
			if in.Muted {
				st.Status = StatusMuted
				return st, nil
			}
			return fire(st, in)
		}
	case StatusMuted:
		next, emit, done := recoverOffline(st, in, heardAgo)
		if done {
			return next, emit
		}
		st = next
		if silenceA >= in.A && !in.Muted {
			return fire(st, in)
		}
	case StatusFiring:
		next, emit, done := recoverOffline(st, in, heardAgo)
		if done {
			return next, emit
		}
		st = next
	default:
		// A device with no live alert state that was already gone for longer than A before this
		// evaluator started is a known-offline device, not a new outage.
		if in.LastHeard < in.Started-in.A {
			return State{Status: StatusDormant, ResolvedAt: st.ResolvedAt}, nil
		}
		if silence >= in.S {
			return State{Status: StatusSuspect, Since: in.LastHeard, ResolvedAt: st.ResolvedAt}, nil
		}
	}
	return st, nil
}

// recoverOffline runs the recovery hold of a firing or muted alert: the device must stay heard for
// RecoveryHold before the alert resolves, so a flapping device yields one fired and one resolved.
// done is false when the normal table should continue (the hold is not running).
func recoverOffline(st State, in OfflineInput, heardAgo int64) (State, *OfflineEmit, bool) {
	if st.RecoveringSince == 0 {
		if heardAgo < in.S {
			st.RecoveringSince = in.Now
			return st, nil, true
		}
		return st, nil, false
	}
	switch {
	case heardAgo > in.G:
		st.RecoveringSince = 0
		return st, nil, false
	case in.Now-st.RecoveringSince >= RecoveryHold.Milliseconds():
		emit := &OfflineEmit{Kind: KindResolved, DurationMS: st.RecoveringSince - st.Since, Muted: st.Status == StatusMuted}
		next := State{Status: StatusOK, ResolvedAt: st.ResolvedAt}
		if st.Status == StatusFiring {
			next.ResolvedAt = in.Now
		}
		return next, emit, true
	}
	return st, nil, true
}

func fire(st State, in OfflineInput) (State, *OfflineEmit) {
	emit := &OfflineEmit{Kind: KindFired, DurationMS: in.Now - st.Since}
	return State{Status: StatusFiring, Since: st.Since, FiredAt: in.Now, ResolvedAt: st.ResolvedAt}, emit
}
