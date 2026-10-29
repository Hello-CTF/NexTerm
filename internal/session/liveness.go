package session

import (
	"context"
	"errors"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// Liveness is the connection health of a connected session as judged by its
// heartbeat. It refines StatusConnected only: a session that is reconnecting,
// failed or disconnected carries no liveness.
type Liveness string

const (
	// LivenessAlive means the peer answered or sent data recently.
	LivenessAlive Liveness = "alive"
	// LivenessSuspect means a heartbeat went unanswered with no other
	// traffic, but the connection has not been silent long enough to be
	// declared dead.
	LivenessSuspect Liveness = "suspect"
	// LivenessDead means the connection is gone; the session reconnects.
	LivenessDead Liveness = "dead"
)

// LivenessPolicy tunes the per-session heartbeat. Zero fields take defaults; a
// negative Interval disables the background heartbeat (explicit probes still
// work).
type LivenessPolicy struct {
	// Interval is the heartbeat period while the connection is alive.
	Interval time.Duration
	// SuspectInterval is the shorter period used while suspect.
	SuspectInterval time.Duration
	// Timeout bounds one background heartbeat.
	Timeout time.Duration
	// ProbeTimeout bounds one explicit probe (foreground resume).
	ProbeTimeout time.Duration
	// SilenceBudget is how long the connection may stay completely silent
	// (no reply and no inbound bytes) before it is declared dead. It matches
	// the SSH transport keepalive budget (30s x 3).
	SilenceBudget time.Duration
	// SuspendGrace is how long after a detected system suspend or clock jump
	// silence never declares the connection dead. It only matters when
	// SilenceBudget is shorter than the grace; otherwise the budget restarted
	// at the jump already outlasts it.
	SuspendGrace time.Duration
}

func (p LivenessPolicy) withDefaults() LivenessPolicy {
	if p.Interval == 0 {
		p.Interval = 15 * time.Second
	}
	if p.SuspectInterval <= 0 {
		p.SuspectInterval = 5 * time.Second
	}
	if p.Timeout <= 0 {
		p.Timeout = 10 * time.Second
	}
	if p.ProbeTimeout <= 0 {
		p.ProbeTimeout = 5 * time.Second
	}
	if p.SilenceBudget <= 0 {
		p.SilenceBudget = 90 * time.Second
	}
	if p.SuspendGrace <= 0 {
		p.SuspendGrace = 30 * time.Second
	}
	return p
}

// livenessReading is one clock sample. mono is a monotonic offset and wall the
// wall clock without its monotonic reading: the two diverge across a system
// suspend on platforms whose monotonic clock stops while asleep, while on the
// others the monotonic gap itself grows past any heartbeat period.
type livenessReading struct {
	mono time.Duration
	wall time.Time
}

type livenessClock interface {
	read() livenessReading
	timer(time.Duration) (<-chan time.Time, func() bool)
}

var livenessEpoch = time.Now()

type systemLivenessClock struct{}

func (systemLivenessClock) read() livenessReading {
	return livenessReading{mono: time.Since(livenessEpoch), wall: time.Now().Round(0)}
}

func (systemLivenessClock) timer(d time.Duration) (<-chan time.Time, func() bool) {
	timer := time.NewTimer(d)
	return timer.C, timer.Stop
}

type heartbeatResult int

const (
	heartbeatOK heartbeatResult = iota
	// heartbeatMissed is an unanswered heartbeat: the connection may be
	// slow, the network may be changing, or the peer may be gone.
	heartbeatMissed
	// heartbeatLost is a definitive transport failure.
	heartbeatLost
)

func classifyHeartbeat(err error) heartbeatResult {
	switch {
	case err == nil:
		return heartbeatOK
	case errors.Is(err, context.DeadlineExceeded):
		return heartbeatMissed
	default:
		return heartbeatLost
	}
}

// livenessObservation is one heartbeat outcome. inbound is the transport's
// inbound byte count when the heartbeat returned; probe marks an explicit
// (foreground) probe, which never escalates a miss past suspect.
type livenessObservation struct {
	result  heartbeatResult
	inbound uint64
	probe   bool
}

// livenessTracker is the per-connection state machine:
//
//	alive --missed, no traffic--> suspect --silent >= SilenceBudget--> dead
//	  ^                              |
//	  +---- reply or any traffic ----+          any --lost--> dead
//
// Any inbound bytes since the previous observation prove the peer alive even
// when the heartbeat reply is queued behind bulk output. Misses are not
// counted at all, so one unanswered request (which the SSH transport shares
// between every waiter) can never add up to a verdict: only a definitive
// transport failure, or total silence for the whole budget measured on the
// monotonic clock, declares the connection dead. A detected suspend or clock
// jump restarts the silence budget and opens a grace window in which silence
// never declares death, because the network usually takes a few seconds to
// come back after a wake.
type livenessTracker struct {
	policy     LivenessPolicy
	state      Liveness
	last       livenessReading
	lastLife   livenessReading
	graceUntil time.Duration
	inbound    uint64
}

func newLivenessTracker(policy LivenessPolicy, now livenessReading, inbound uint64) livenessTracker {
	return livenessTracker{policy: policy, state: LivenessAlive, last: now, lastLife: now, inbound: inbound}
}

func (t *livenessTracker) suspended(now livenessReading) bool {
	expected := max(t.policy.Interval, t.policy.SuspectInterval) + t.policy.Timeout
	monoGap := now.mono - t.last.mono
	wallGap := now.wall.Sub(t.last.wall)
	drift := wallGap - monoGap
	if drift < 0 {
		drift = -drift
	}
	return monoGap > 2*expected || drift > expected
}

func (t *livenessTracker) observe(now livenessReading, observation livenessObservation) Liveness {
	if t.suspended(now) {
		t.graceUntil = now.mono + t.policy.SuspendGrace
		t.lastLife = now
	}
	t.last = now
	// A smaller count is an older snapshot delivered late, not new traffic.
	traffic := observation.inbound > t.inbound
	t.inbound = max(t.inbound, observation.inbound)
	if t.state == LivenessDead {
		return t.state
	}
	switch {
	case observation.result == heartbeatLost:
		t.state = LivenessDead
	case observation.result == heartbeatOK || traffic:
		t.lastLife = now
		t.state = LivenessAlive
	default:
		t.state = LivenessSuspect
		silent := now.mono - t.lastLife.mono
		if !observation.probe && now.mono >= t.graceUntil && silent >= t.policy.SilenceBudget {
			t.state = LivenessDead
		}
	}
	return t.state
}

func (t *livenessTracker) nextInterval() time.Duration {
	if t.state != LivenessAlive {
		return t.policy.SuspectInterval
	}
	return t.policy.Interval
}

// livenessLabelLocked is the status-event liveness: only a connected session
// that is suspect reports it. A connection judged dead whose reconnect could
// not start also reads as suspect, so the indicator never claims a healthy
// link. Callers hold s.mu.
func (s *Session) livenessLabelLocked(status Status) Liveness {
	if status == StatusConnected && s.livenessGen == s.generation && s.liveness.state != "" && s.liveness.state != LivenessAlive {
		return LivenessSuspect
	}
	return ""
}

// armLivenessLocked resets the tracker for a freshly connected transport and
// captures what the monitor needs while the connection is current. Callers
// hold s.mu.
func (m *Manager) armLivenessLocked(session *Session) livenessStart {
	start := livenessStart{ctx: session.ctx, generation: session.generation}
	var inbound uint64
	if heartbeater, ok := transportHeartbeater(session.transport); ok {
		inbound = heartbeater.InboundBytes()
		if m.livenessPolicy.Interval >= 0 {
			start.heartbeater = heartbeater
		}
	}
	session.liveness = newLivenessTracker(m.livenessPolicy, m.livenessClock.read(), inbound)
	session.livenessGen = session.generation
	return start
}

type livenessStart struct {
	ctx         context.Context
	generation  uint64
	heartbeater base.Heartbeater
}

func transportHeartbeater(transport *transportHandle) (base.Heartbeater, bool) {
	if transport == nil {
		return nil, false
	}
	heartbeater, ok := transport.Transport.(base.Heartbeater)
	return heartbeater, ok
}

// startLiveness runs the background heartbeat for one connection generation.
// It stops when the session context ends (reconnect, close, manager close) or
// when its connection is no longer the session's current one. The SSH
// transport keeps its own keepalive for connections no session monitors; both
// wait on the same in-flight request, so monitoring adds no extra requests.
func (m *Manager) startLiveness(session *Session, start livenessStart) {
	heartbeater, ctx, generation := start.heartbeater, start.ctx, start.generation
	if heartbeater == nil {
		return
	}
	m.startGoroutine(func() {
		for {
			session.mu.Lock()
			interval := session.liveness.nextInterval()
			session.mu.Unlock()
			fire, stop := m.livenessClock.timer(interval)
			select {
			case <-ctx.Done():
				stop()
				return
			case <-fire:
			}
			probeCtx, cancel := context.WithTimeout(ctx, m.livenessPolicy.Timeout)
			err := heartbeater.Heartbeat(probeCtx)
			cancel()
			if ctx.Err() != nil {
				m.livenessStale.Add(1)
				return
			}
			observation := livenessObservation{result: classifyHeartbeat(err), inbound: heartbeater.InboundBytes()}
			if _, current := m.observeLiveness(session, generation, observation, err); !current {
				return
			}
		}
	})
}

// observeLiveness feeds one heartbeat outcome into the session's tracker and
// reports whether the connection's monitor should keep running. A result from
// a superseded generation is dropped and counted. A dead
// connection starts an immediate reconnect; when that cannot start, the
// transport is closed so the ordinary disconnect path takes over, and the
// session reports itself suspect meanwhile. Other transitions are published
// as status events.
func (m *Manager) observeLiveness(session *Session, generation uint64, observation livenessObservation, cause error) (Liveness, bool) {
	now := m.livenessClock.read()
	session.mu.Lock()
	if session.closed || session.status != StatusConnected || session.generation != generation || session.livenessGen != generation {
		session.mu.Unlock()
		m.livenessStale.Add(1)
		return "", false
	}
	previous := session.liveness.state
	state := session.liveness.observe(now, observation)
	transport := session.transport
	session.mu.Unlock()
	if state == LivenessDead {
		err := m.startReconnectImmediate(session.ID, generation)
		if err == nil || errors.Is(err, ErrStaleGeneration) || errors.Is(err, ErrSessionClosed) {
			if previous != LivenessDead {
				m.logger.Warn("session connection lost; reconnecting", "session", session.ID, "error", cause)
			}
			// The reconnect owns the session now; this monitor is done.
			return state, false
		}
		if previous != LivenessDead {
			m.logger.Warn("session connection lost and its reconnect could not start; closing the transport",
				"session", session.ID, "error", cause, "reconnect_error", err)
		}
		if transport != nil {
			_ = transport.Close()
		}
	}
	if state != previous {
		session.mu.Lock()
		current := !session.closed && session.status == StatusConnected && session.generation == generation && session.livenessGen == generation
		var event StatusEvent
		if current {
			event = session.statusEventLocked(StatusConnected, nil)
		}
		session.mu.Unlock()
		if current {
			m.emit(context.Background(), TopicSessionStatus, event)
		}
	}
	return state, true
}

// ProbeLivenessFor checks the session's connection now, typically because the
// client returned to the foreground. A connected session sends one heartbeat:
// a definitive failure reconnects at once, while a missed reply only marks it
// suspect (the background heartbeat decides death). A session waiting out a
// reconnect backoff retries immediately. Other states are reported unchanged.
func (m *Manager) ProbeLivenessFor(ctx context.Context, id string) (SessionInfo, error) {
	session, err := m.SessionFor(ctx, id)
	if err != nil {
		return SessionInfo{}, err
	}
	session.mu.Lock()
	if session.reconnecting {
		if session.reconnectWake != nil {
			select {
			case session.reconnectWake <- struct{}{}:
			default:
			}
		}
		session.mu.Unlock()
		return session.Info(), nil
	}
	status := session.status
	generation := session.generation
	transport := session.transport
	session.mu.Unlock()
	if status == StatusFailed {
		// The network may be back: a Failed session holding durable
		// terminals retries now instead of waiting for its background retry.
		if sessionHoldsDurable(session) {
			_ = m.startReconnectImmediate(session.ID, 0)
		}
		return session.Info(), nil
	}
	if status != StatusConnected {
		return session.Info(), nil
	}
	heartbeater, _ := transportHeartbeater(transport)
	if transport == nil || !transport.IsAlive() {
		observation := livenessObservation{result: heartbeatLost, probe: true}
		if heartbeater != nil {
			observation.inbound = heartbeater.InboundBytes()
		}
		m.observeLiveness(session, generation, observation, base.ErrDisconnected)
		return session.Info(), nil
	}
	if heartbeater == nil {
		return session.Info(), nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, m.livenessPolicy.ProbeTimeout)
	err = heartbeater.Heartbeat(probeCtx)
	cancel()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return SessionInfo{}, ctxErr
	}
	observation := livenessObservation{result: classifyHeartbeat(err), inbound: heartbeater.InboundBytes(), probe: true}
	m.observeLiveness(session, generation, observation, err)
	return session.Info(), nil
}
