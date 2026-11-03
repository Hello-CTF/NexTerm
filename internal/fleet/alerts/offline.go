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
	Enabled   bool
	Muted     bool
	// S is the suspect threshold and A (>= S) the alert threshold.
	S, A int64
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
	heardAgo := in.Now - in.LastHeard
	switch st.Status {
	case StatusSuspect:
		switch {
		case silence < in.S:
			return State{Status: StatusOK, ResolvedAt: st.ResolvedAt}, nil
		case silence >= in.A:
			if in.Muted {
				st.Status = StatusMuted
				return st, nil
			}
			return fire(st, in)
		}
	case StatusMuted:
		switch {
		case heardAgo < in.S:
			emit := &OfflineEmit{Kind: KindResolved, DurationMS: in.Now - st.Since, Muted: true}
			return State{Status: StatusOK, ResolvedAt: st.ResolvedAt}, emit
		case silence >= in.A && !in.Muted:
			return fire(st, in)
		}
	case StatusFiring:
		if heardAgo < in.S {
			emit := &OfflineEmit{Kind: KindResolved, DurationMS: in.Now - st.Since}
			return State{Status: StatusOK, ResolvedAt: in.Now}, emit
		}
	default:
		if silence >= in.S {
			return State{Status: StatusSuspect, Since: in.LastHeard, ResolvedAt: st.ResolvedAt}, nil
		}
	}
	return st, nil
}

func fire(st State, in OfflineInput) (State, *OfflineEmit) {
	emit := &OfflineEmit{Kind: KindFired, DurationMS: in.Now - st.Since}
	return State{Status: StatusFiring, Since: st.Since, FiredAt: in.Now, ResolvedAt: st.ResolvedAt}, emit
}
