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
	// LivenessAlive means the last heartbeat was answered.
	LivenessAlive Liveness = "alive"
	// LivenessSuspect means heartbeats are going unanswered but not yet long
	// enough to declare the connection dead, or the outcome straddled a
	// system sleep and cannot be trusted.
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
	// SuspectInterval is the shorter period used while suspect, so a dead
	// connection is confirmed quickly once heartbeats start failing.
	SuspectInterval time.Duration
	// Timeout bounds one background heartbeat.
	Timeout time.Duration
	// ProbeTimeout bounds one explicit probe (foreground resume).
	ProbeTimeout time.Duration
	// DeadAfter is the number of consecutive unanswered heartbeats that
	// declares the connection dead.
	DeadAfter int
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
	if p.DeadAfter <= 0 {
		p.DeadAfter = 3
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

// livenessTracker is the per-connection state machine:
//
//	alive --missed--> suspect --missed x DeadAfter--> dead
//	  ^                  |
//	  +------- ok -------+          any --lost--> dead
//
// A missed heartbeat whose observation gap shows the system was suspended (or
// the wall clock jumped) does not count towards DeadAfter: the reply may have
// been lost to the sleep itself, so the tracker only becomes suspect and
// restarts the count.
type livenessTracker struct {
	policy   LivenessPolicy
	state    Liveness
	failures int
	last     livenessReading
}

func newLivenessTracker(policy LivenessPolicy, now livenessReading) livenessTracker {
	return livenessTracker{policy: policy, state: LivenessAlive, last: now}
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

func (t *livenessTracker) observe(now livenessReading, result heartbeatResult) Liveness {
	suspended := t.suspended(now)
	t.last = now
	if t.state == LivenessDead {
		return t.state
	}
	switch result {
	case heartbeatOK:
		t.failures = 0
		t.state = LivenessAlive
	case heartbeatLost:
		t.state = LivenessDead
	case heartbeatMissed:
		if suspended {
			t.failures = 0
			t.state = LivenessSuspect
			break
		}
		t.failures++
		if t.failures >= t.policy.DeadAfter {
			t.state = LivenessDead
		} else {
			t.state = LivenessSuspect
		}
	}
	return t.state
}

func (t *livenessTracker) nextInterval() time.Duration {
	if t.state == LivenessSuspect {
		return t.policy.SuspectInterval
	}
	return t.policy.Interval
}

// livenessLabelLocked is the status-event liveness: only a connected session
// that is suspect reports it. Callers hold s.mu.
func (s *Session) livenessLabelLocked(status Status) Liveness {
	if status == StatusConnected && s.livenessGen == s.generation && s.liveness.state == LivenessSuspect {
		return LivenessSuspect
	}
	return ""
}

// armLivenessLocked resets the tracker for a freshly connected transport and
// captures what the monitor needs while the connection is current. Callers
// hold s.mu.
func (m *Manager) armLivenessLocked(session *Session) livenessStart {
	session.liveness = newLivenessTracker(m.livenessPolicy, m.livenessClock.read())
	session.livenessGen = session.generation
	start := livenessStart{ctx: session.ctx, generation: session.generation}
	if m.livenessPolicy.Interval >= 0 && session.transport != nil {
		start.heartbeater, _ = session.transport.Transport.(base.Heartbeater)
	}
	return start
}

type livenessStart struct {
	ctx         context.Context
	generation  uint64
	heartbeater base.Heartbeater
}

// startLiveness runs the background heartbeat for one connection generation.
// It stops when the session context ends (reconnect, close, manager close) or
// once the connection is declared dead.
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
			if state, current := m.observeLiveness(session, generation, classifyHeartbeat(err), err); !current || state == LivenessDead {
				return
			}
		}
	})
}

// observeLiveness feeds one heartbeat outcome into the session's tracker. A
// result from a superseded generation is dropped and counted; a transition is
// published as a status event, and a dead connection starts an immediate
// reconnect.
func (m *Manager) observeLiveness(session *Session, generation uint64, result heartbeatResult, cause error) (Liveness, bool) {
	now := m.livenessClock.read()
	session.mu.Lock()
	if session.closed || session.status != StatusConnected || session.generation != generation || session.livenessGen != generation {
		session.mu.Unlock()
		m.livenessStale.Add(1)
		return "", false
	}
	previous := session.liveness.state
	state := session.liveness.observe(now, result)
	var event StatusEvent
	publish := state != previous && state != LivenessDead
	if publish {
		event = session.statusEventLocked(StatusConnected, nil)
	}
	session.mu.Unlock()
	switch {
	case state == LivenessDead:
		m.logger.Warn("session connection lost; reconnecting", "session", session.ID, "error", cause)
		_ = m.startReconnectImmediate(session.ID, generation)
	case publish:
		m.emit(context.Background(), TopicSessionStatus, event)
	}
	return state, true
}

// ProbeLivenessFor checks the session's connection now, typically because the
// client returned to the foreground. A connected session sends one heartbeat
// (a definitive failure reconnects at once, a missed reply counts towards the
// dead threshold); a session waiting out a reconnect backoff retries
// immediately. Other states are reported unchanged.
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
	if status != StatusConnected {
		return session.Info(), nil
	}
	if transport == nil || !transport.IsAlive() {
		m.observeLiveness(session, generation, heartbeatLost, base.ErrDisconnected)
		return session.Info(), nil
	}
	heartbeater, ok := transport.Transport.(base.Heartbeater)
	if !ok {
		return session.Info(), nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, m.livenessPolicy.ProbeTimeout)
	err = heartbeater.Heartbeat(probeCtx)
	cancel()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return SessionInfo{}, ctxErr
	}
	m.observeLiveness(session, generation, classifyHeartbeat(err), err)
	return session.Info(), nil
}
