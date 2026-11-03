// Package alerts evaluates device alert rules on the server and persists their state, events and audit rows.
package alerts

import (
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

type (
	Settings = model.DeviceAlertSettings
	Metric   = model.DeviceMetrics
)

// Rule is the stored rule key.
type Rule string

const (
	RuleDisk    Rule = "disk"
	RuleMem     Rule = "mem"
	RuleCPU     Rule = "cpu"
	RuleOffline Rule = "offline"
)

// Persisted state and event vocabulary.
const (
	StatusOK      = "ok"
	StatusFiring  = "firing"
	StatusSuspect = "suspect"
	StatusMuted   = "muted"
	// StatusDormant marks a device that was already long gone when the evaluator started: a known
	// offline device that never alerts until it is heard again.
	StatusDormant = "dormant"

	KindFired    = "fired"
	KindResolved = "resolved"
)

const (
	// DefaultInterval is the agent sync interval I; the server always asks agents for 60 s.
	DefaultInterval = 60 * time.Second
	// RecoveryHold is H: how long a value must stay at or below the threshold to resolve.
	RecoveryHold = 5 * time.Minute
	// Cooldown is C: after a resolve the same rule stays quiet for this long.
	Cooldown = 30 * time.Minute
	// EventRetention bounds how long alert events are kept.
	EventRetention = 30 * 24 * time.Hour

	DefaultThreshold = 90.0
	MinThreshold     = 1.0
	MaxThreshold     = 100.0
)

// GapTolerance is G = 2.5*I: the longest silence between samples, or since the latest one, that a
// threshold rule still treats as continuous data.
func GapTolerance(interval time.Duration) time.Duration { return interval * 5 / 2 }

// ThresholdRules lists the rules evaluated from device_metrics.
var ThresholdRules = []Rule{RuleDisk, RuleMem, RuleCPU}

// Sustain is N: how long the value must stay above the threshold before the rule fires.
func (r Rule) Sustain() time.Duration {
	if r == RuleCPU {
		return 15 * time.Minute
	}
	return 10 * time.Minute
}

// ValidThreshold reports whether v is an accepted threshold override.
func ValidThreshold(v float64) bool { return v >= MinThreshold && v <= MaxThreshold }

// ResolveThreshold returns the effective enabled flag and threshold of a threshold rule;
// a missing or invalid override falls back to the default.
func ResolveThreshold(rule Rule, s *Settings) (enabled bool, threshold float64) {
	enabled, threshold = true, DefaultThreshold
	if s == nil {
		return
	}
	var en *bool
	var th *float64
	switch rule {
	case RuleDisk:
		en, th = s.DiskEnabled, s.DiskThreshold
	case RuleMem:
		en, th = s.MemEnabled, s.MemThreshold
	case RuleCPU:
		en, th = s.CPUEnabled, s.CPUThreshold
	}
	if en != nil {
		enabled = *en
	}
	if th != nil && ValidThreshold(*th) {
		threshold = *th
	}
	return
}

// metricValue returns the rule's percentage for this sample; ok is false when the needed fields are
// missing or a total is not positive.
func metricValue(m *Metric, rule Rule) (float64, bool) {
	switch rule {
	case RuleCPU:
		if m.CPUPct == nil {
			return 0, false
		}
		return *m.CPUPct, true
	case RuleMem:
		return pct(m.MemUsed, m.MemTotal)
	case RuleDisk:
		return pct(m.DiskUsed, m.DiskTotal)
	}
	return 0, false
}
