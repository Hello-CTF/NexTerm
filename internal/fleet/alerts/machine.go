package alerts

import "time"

// Sample is one valid metric reading for a single rule.
type Sample struct {
	TS    int64
	Value float64
}

// Verdict is the outcome of looking at a rule's recent samples.
type Verdict int

const (
	// VerdictUnknown means neither breach nor clear is established; it never causes a transition.
	VerdictUnknown Verdict = iota
	VerdictBreach
	VerdictClear
)

func pct(used, total *int64) (float64, bool) {
	if used == nil || total == nil || *total <= 0 {
		return 0, false
	}
	return float64(*used) / float64(*total) * 100, true
}

// Classify judges samples (ascending by TS) at now: breach when the value stayed above threshold for
// sustain, clear when it stayed at or below threshold for RecoveryHold. Stale data or sample gaps
// longer than gap give VerdictUnknown. The second result is the latest sample value.
func Classify(samples []Sample, now int64, threshold float64, sustain, gap time.Duration) (Verdict, float64) {
	if len(samples) == 0 {
		return VerdictUnknown, 0
	}
	latest := samples[len(samples)-1]
	if latest.TS > now || latest.TS < now-gap.Milliseconds() {
		return VerdictUnknown, latest.Value
	}
	switch {
	case holds(samples, now, sustain, gap, func(v float64) bool { return v > threshold }):
		return VerdictBreach, latest.Value
	case holds(samples, now, RecoveryHold, gap, func(v float64) bool { return v <= threshold }):
		return VerdictClear, latest.Value
	}
	return VerdictUnknown, latest.Value
}

// holds requires an anchor sample at or before now-span and every sample from it on to satisfy ok,
// with no consecutive gap above gap.
func holds(samples []Sample, now int64, span, gap time.Duration, ok func(float64) bool) bool {
	cutoff := now - span.Milliseconds()
	anchor := -1
	for i, s := range samples {
		if s.TS <= cutoff {
			anchor = i
		}
	}
	if anchor < 0 {
		return false
	}
	for i := anchor; i < len(samples); i++ {
		if !ok(samples[i].Value) {
			return false
		}
		if i > anchor && samples[i].TS-samples[i-1].TS > gap.Milliseconds() {
			return false
		}
	}
	return true
}

// State is the persisted position of a threshold rule.
type State struct {
	Status     string
	Since      int64
	FiredAt    int64
	ResolvedAt int64
	LastValue  *float64
}

// StepInput feeds one evaluation into StepThreshold.
type StepInput struct {
	Now       int64
	Enabled   bool
	Verdict   Verdict
	Value     float64
	Threshold float64
}

// Emit describes the event a transition produces.
type Emit struct {
	Kind       string
	Value      float64
	Threshold  float64
	DurationMS int64
}

// StepThreshold applies the threshold-rule transition table. It is pure: no clock, no storage.
func StepThreshold(st State, in StepInput) (State, *Emit) {
	if !in.Enabled {
		if st.Status != StatusOK {
			st = State{Status: StatusOK, ResolvedAt: st.ResolvedAt}
		}
		return st, nil
	}
	v := in.Value
	switch st.Status {
	case StatusFiring:
		if in.Verdict == VerdictClear {
			emit := &Emit{Kind: KindResolved, Value: v, Threshold: in.Threshold, DurationMS: in.Now - st.FiredAt}
			return State{Status: StatusOK, ResolvedAt: in.Now, LastValue: &v}, emit
		}
	default:
		if in.Verdict == VerdictBreach && (st.ResolvedAt == 0 || in.Now >= st.ResolvedAt+Cooldown.Milliseconds()) {
			emit := &Emit{Kind: KindFired, Value: v, Threshold: in.Threshold}
			return State{Status: StatusFiring, Since: in.Now, FiredAt: in.Now, ResolvedAt: st.ResolvedAt, LastValue: &v}, emit
		}
	}
	return st, nil
}
