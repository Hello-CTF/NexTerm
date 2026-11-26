// Package selector decides, from measurements only, whether a device's streams should use the direct leg or the
// relay (design 4.4). It does no I/O and reads no clock: the caller passes the time with every sample, so the
// thresholds and hysteresis can be tested with synthetic samples.
package selector

import "time"

// Thresholds from design 4.4. They are starting values without field data; the diagnostics view shows them.
const (
	// PingIntervalActive and PingIntervalIdle pace the direct leg's application pings (the idle one also keeps
	// NAT bindings alive); PingTimeout is one ping's budget.
	PingIntervalActive = time.Second
	PingIntervalIdle   = 5 * time.Second
	PingTimeout        = 1500 * time.Millisecond
	// RelayRTTInterval paces the relay round trip measured over the signal bridge.
	RelayRTTInterval = 10 * time.Second

	// DegradeTimeouts consecutive lost pings move the streams to the relay.
	DegradeTimeouts = 3
	// DegradeLoss is the loss fraction over LossWindow above which the streams move to the relay.
	DegradeLoss = 0.08
	LossWindow  = 10 * time.Second
	// The direct RTT is worse than the relay when it exceeds the relay RTT by RTTPenalty and by RTTRatio, for
	// RTTSustain.
	RTTPenalty = 80 * time.Millisecond
	RTTRatio   = 1.5
	RTTSustain = 15 * time.Second

	// RecoverHealthy is how long the leg must stay healthy, and no slower than the relay, before streams return.
	RecoverHealthy = 30 * time.Second
	// MinDwell is the least time between two quality-driven switches.
	MinDwell = 60 * time.Second
	// PinDegrades degradations within PinWindow keep the device on the relay for PinDuration.
	PinDegrades = 3
	PinWindow   = 10 * time.Minute
	PinDuration = 30 * time.Minute
)

// Mode is the per-device connection mode.
type Mode string

const (
	ModeAuto   Mode = "auto"
	ModeRelay  Mode = "relay"
	ModeDirect Mode = "direct"
)

// ParseMode returns the mode text names; anything else, the empty string included, is ModeAuto.
func ParseMode(text string) Mode {
	switch Mode(text) {
	case ModeRelay, ModeDirect:
		return Mode(text)
	}
	return ModeAuto
}

// Path is where the streams should be.
type Path string

const (
	PathRelay  Path = "relay"
	PathDirect Path = "direct"
)

// Action is what Decide asks the caller to do.
type Action int

const (
	// Stay keeps the streams where they are.
	Stay Action = iota
	// UseDirect moves the streams to the direct leg.
	UseDirect
	// UseRelay moves the streams to the relay and keeps the leg for probing.
	UseRelay
	// CloseLeg moves the streams to the relay and closes the leg: it is dead, or the device is pinned to the relay.
	CloseLeg
)

func (a Action) String() string {
	switch a {
	case UseDirect:
		return "use_direct"
	case UseRelay:
		return "use_relay"
	case CloseLeg:
		return "close_leg"
	}
	return "stay"
}

// Reason explains a switch to the relay; the strings are the path status reasons.
const (
	ReasonUnstable = "unstable"
	ReasonFailed   = "connect_failed"
)

// Sample is one observation.
type Sample struct {
	// LegUp reports that a direct leg exists and is not known dead.
	LegUp bool
	// Ping is set when a direct ping finished: PingOK with PingRTT, or a timeout.
	Ping    bool
	PingOK  bool
	PingRTT time.Duration
	// Loss is the leg's packet loss over LossWindow, when HasLoss.
	HasLoss bool
	Loss    float64
	// RelayRTT is the latest relay round trip; zero when unknown.
	RelayRTT time.Duration
	// Mode is the device's stored connection mode.
	Mode Mode
}

// Selector holds the hysteresis state of one device. The zero value starts on the relay.
type Selector struct {
	path       Path
	lastSwitch time.Time
	// timeouts counts consecutive lost pings.
	timeouts int
	// rtt is the latest successful direct ping.
	rtt time.Duration
	// healthySince is when the leg last became healthy and no slower than the relay; zero while it is not.
	healthySince time.Time
	// slowSince is when the direct RTT started to exceed the relay's; zero while it does not.
	slowSince time.Time
	// everDirect reports a first switch to direct happened; later switches need RecoverHealthy.
	everDirect bool
	degrades   []time.Time
	pinnedTill time.Time
	reason     string
}

// Path is where the streams currently belong.
func (s *Selector) Path() Path {
	if s.path == "" {
		return PathRelay
	}
	return s.path
}

// Reason is why the streams are on the relay after a degradation; empty otherwise.
func (s *Selector) Reason() string { return s.reason }

// PinnedUntil is when a pin to the relay ends; zero when the device is not pinned.
func (s *Selector) PinnedUntil() time.Time { return s.pinnedTill }

// Reset forgets the leg measurements, for a new leg; the pin and the degradation history stay.
func (s *Selector) Reset() {
	s.timeouts, s.rtt = 0, 0
	s.healthySince, s.slowSince = time.Time{}, time.Time{}
}

// Pinned reports whether the device is held on the relay at now.
func (s *Selector) Pinned(now time.Time) bool {
	return !s.pinnedTill.IsZero() && now.Before(s.pinnedTill)
}

func (s *Selector) switchTo(now time.Time, path Path, reason string) {
	s.path, s.lastSwitch, s.reason = path, now, reason
	if path == PathDirect {
		s.everDirect = true
	}
}

// Decide folds sample into the state and returns the action for now.
//
//   - A dead leg, or DegradeTimeouts lost pings in a row, always leaves direct at once: the streams cannot wait
//     out a dwell on a path that does not deliver.
//   - Loss above DegradeLoss, or a direct RTT sustained worse than the relay, leaves direct only after MinDwell;
//     PinDegrades such degradations within PinWindow pin the device to the relay for PinDuration.
//   - The first switch to direct happens on the first answered ping; a return after a degradation needs
//     RecoverHealthy of good pings no slower than the relay, and MinDwell since the last switch.
//   - ModeRelay always selects the relay. ModeDirect keeps direct while the leg answers: it ignores loss, RTT and
//     the pin, and returns at once.
func (s *Selector) Decide(now time.Time, sample Sample) Action {
	if s.Pinned(now) && sample.Mode != ModeDirect {
		if s.Path() == PathDirect {
			s.switchTo(now, PathRelay, ReasonUnstable)
		}
		return CloseLeg
	}
	if !s.pinnedTill.IsZero() && !s.Pinned(now) {
		s.pinnedTill = time.Time{}
	}
	if sample.Mode == ModeRelay {
		if s.Path() == PathDirect {
			s.switchTo(now, PathRelay, "")
		}
		return CloseLeg
	}
	if !sample.LegUp {
		s.Reset()
		if s.Path() == PathDirect {
			s.switchTo(now, PathRelay, ReasonFailed)
			return CloseLeg
		}
		return Stay
	}
	s.observe(now, sample)
	if s.Path() == PathDirect {
		return s.decideOnDirect(now, sample)
	}
	return s.decideOnRelay(now, sample)
}

func (s *Selector) observe(now time.Time, sample Sample) {
	if sample.Ping {
		if sample.PingOK {
			s.timeouts, s.rtt = 0, sample.PingRTT
		} else {
			s.timeouts++
		}
	}
	healthy := s.timeouts == 0 && s.rtt > 0 && (!sample.HasLoss || sample.Loss <= DegradeLoss)
	if healthy && !s.slower(sample.RelayRTT) {
		if s.healthySince.IsZero() {
			s.healthySince = now
		}
	} else {
		s.healthySince = time.Time{}
	}
	if s.rtt > 0 && s.slower(sample.RelayRTT) {
		if s.slowSince.IsZero() {
			s.slowSince = now
		}
	} else {
		s.slowSince = time.Time{}
	}
}

// slower reports the direct RTT exceeds the relay's by RTTPenalty and by RTTRatio.
func (s *Selector) slower(relay time.Duration) bool {
	if relay <= 0 || s.rtt <= 0 {
		return false
	}
	return s.rtt > relay+RTTPenalty && float64(s.rtt) > RTTRatio*float64(relay)
}

func (s *Selector) decideOnDirect(now time.Time, sample Sample) Action {
	if s.timeouts >= DegradeTimeouts {
		s.degrade(now, sample.Mode)
		return UseRelay
	}
	if sample.Mode == ModeDirect || now.Sub(s.lastSwitch) < MinDwell {
		return Stay
	}
	lossy := sample.HasLoss && sample.Loss > DegradeLoss
	slow := !s.slowSince.IsZero() && now.Sub(s.slowSince) >= RTTSustain
	if !lossy && !slow {
		return Stay
	}
	if s.degrade(now, sample.Mode) {
		return CloseLeg
	}
	return UseRelay
}

// degrade switches to the relay and records the degradation; it reports whether the device is now pinned.
func (s *Selector) degrade(now time.Time, mode Mode) bool {
	s.switchTo(now, PathRelay, ReasonUnstable)
	s.healthySince = time.Time{}
	if mode == ModeDirect {
		return false
	}
	kept := s.degrades[:0]
	for _, at := range s.degrades {
		if now.Sub(at) < PinWindow {
			kept = append(kept, at)
		}
	}
	s.degrades = append(kept, now)
	if len(s.degrades) >= PinDegrades {
		s.pinnedTill = now.Add(PinDuration)
		s.degrades = s.degrades[:0]
		return true
	}
	return false
}

func (s *Selector) decideOnRelay(now time.Time, sample Sample) Action {
	if s.timeouts > 0 || s.rtt <= 0 {
		return Stay
	}
	if sample.Mode == ModeDirect || !s.everDirect {
		if sample.Mode != ModeDirect && s.slower(sample.RelayRTT) {
			return Stay
		}
		s.switchTo(now, PathDirect, "")
		return UseDirect
	}
	if s.healthySince.IsZero() || now.Sub(s.healthySince) < RecoverHealthy || now.Sub(s.lastSwitch) < MinDwell {
		return Stay
	}
	s.switchTo(now, PathDirect, "")
	return UseDirect
}
