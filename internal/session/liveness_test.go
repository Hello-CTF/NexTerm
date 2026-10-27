package session

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

var livenessTestPolicy = LivenessPolicy{
	Interval: 15 * time.Second, SuspectInterval: 5 * time.Second,
	Timeout: 10 * time.Second, ProbeTimeout: 5 * time.Second, DeadAfter: 3,
}

func livenessAt(mono time.Duration) livenessReading {
	return livenessReading{mono: mono, wall: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC).Add(mono)}
}

func TestLivenessTrackerNeedsConsecutiveMissesToDeclareDead(t *testing.T) {
	tracker := newLivenessTracker(livenessTestPolicy, livenessAt(0))
	steps := []struct {
		at     time.Duration
		result heartbeatResult
		want   Liveness
	}{
		{25 * time.Second, heartbeatMissed, LivenessSuspect},
		{40 * time.Second, heartbeatMissed, LivenessSuspect},
		{55 * time.Second, heartbeatOK, LivenessAlive},
		{80 * time.Second, heartbeatMissed, LivenessSuspect},
		{95 * time.Second, heartbeatMissed, LivenessSuspect},
		{110 * time.Second, heartbeatMissed, LivenessDead},
		{125 * time.Second, heartbeatOK, LivenessDead},
	}
	for index, step := range steps {
		if got := tracker.observe(livenessAt(step.at), step.result); got != step.want {
			t.Fatalf("step %d: state = %s, want %s", index, got, step.want)
		}
	}
}

func TestLivenessTrackerDeclaresDeadOnLostTransport(t *testing.T) {
	tracker := newLivenessTracker(livenessTestPolicy, livenessAt(0))
	if got := tracker.observe(livenessAt(15*time.Second), heartbeatLost); got != LivenessDead {
		t.Fatalf("state after lost transport = %s, want dead", got)
	}
}

func TestLivenessTrackerSuspectIntervalIsShorter(t *testing.T) {
	tracker := newLivenessTracker(livenessTestPolicy, livenessAt(0))
	if got := tracker.nextInterval(); got != livenessTestPolicy.Interval {
		t.Fatalf("alive interval = %v, want %v", got, livenessTestPolicy.Interval)
	}
	tracker.observe(livenessAt(25*time.Second), heartbeatMissed)
	if got := tracker.nextInterval(); got != livenessTestPolicy.SuspectInterval {
		t.Fatalf("suspect interval = %v, want %v", got, livenessTestPolicy.SuspectInterval)
	}
}

// A suspend on a platform whose monotonic clock stops while asleep shows up as
// wall time running ahead of monotonic time.
func TestLivenessTrackerIgnoresMissesAcrossFrozenMonotonicSleep(t *testing.T) {
	tracker := newLivenessTracker(livenessTestPolicy, livenessAt(0))
	tracker.observe(livenessAt(25*time.Second), heartbeatMissed)
	tracker.observe(livenessAt(40*time.Second), heartbeatMissed)
	// Ten minutes asleep: monotonic advances by one period, wall by 10m.
	asleep := livenessReading{mono: 55 * time.Second, wall: livenessAt(40 * time.Second).wall.Add(10*time.Minute + 15*time.Second)}
	if got := tracker.observe(asleep, heartbeatMissed); got != LivenessSuspect {
		t.Fatalf("state after a miss across sleep = %s, want suspect", got)
	}
	next := livenessReading{mono: asleep.mono + 15*time.Second, wall: asleep.wall.Add(15 * time.Second)}
	if got := tracker.observe(next, heartbeatMissed); got != LivenessSuspect {
		t.Fatalf("state after the first post-wake miss = %s, want suspect (count restarted)", got)
	}
}

// On platforms whose monotonic clock keeps running while asleep, the gap
// between observations itself exceeds any heartbeat period.
func TestLivenessTrackerIgnoresMissesAcrossLongMonotonicGap(t *testing.T) {
	tracker := newLivenessTracker(livenessTestPolicy, livenessAt(0))
	tracker.observe(livenessAt(25*time.Second), heartbeatMissed)
	tracker.observe(livenessAt(40*time.Second), heartbeatMissed)
	if got := tracker.observe(livenessAt(40*time.Second+10*time.Minute), heartbeatMissed); got != LivenessSuspect {
		t.Fatalf("state after a miss across a 10 minute gap = %s, want suspect", got)
	}
	if got := tracker.observe(livenessAt(55*time.Second+10*time.Minute), heartbeatOK); got != LivenessAlive {
		t.Fatalf("state after an answered heartbeat = %s, want alive", got)
	}
}

func TestReconnectDelayIsJitteredWithinHalfStep(t *testing.T) {
	manager := NewManager(Config{
		Connector: newFakeConnector(), ReconnectBackoff: []time.Duration{2 * time.Second, 8 * time.Second},
	})
	t.Cleanup(func() { _ = manager.Close() })
	tests := []struct {
		attempt int
		sample  float64
		want    time.Duration
	}{
		{0, 0, time.Second},
		{0, 0.5, 1500 * time.Millisecond},
		{1, 0, 4 * time.Second},
		{1, 0.75, 7 * time.Second},
		{7, 0, 4 * time.Second},
	}
	for _, test := range tests {
		manager.jitter = func() float64 { return test.sample }
		if got := manager.reconnectDelay(test.attempt); got != test.want {
			t.Fatalf("reconnectDelay(%d) with sample %v = %v, want %v", test.attempt, test.sample, got, test.want)
		}
	}
	manager.jitter = func() float64 { return 0.999999 }
	if got := manager.reconnectDelay(1); got >= 8*time.Second || got < 4*time.Second {
		t.Fatalf("reconnectDelay(1) at the top of the range = %v, want within [4s, 8s)", got)
	}
}

type fakeLivenessTimer struct {
	interval time.Duration
	fire     chan time.Time
}

type fakeLivenessClock struct {
	mu     sync.Mutex
	now    livenessReading
	timers chan fakeLivenessTimer
}

func newFakeLivenessClock() *fakeLivenessClock {
	return &fakeLivenessClock{now: livenessAt(0), timers: make(chan fakeLivenessTimer, 64)}
}

func (c *fakeLivenessClock) read() livenessReading {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeLivenessClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = livenessReading{mono: c.now.mono + d, wall: c.now.wall.Add(d)}
	c.mu.Unlock()
}

func (c *fakeLivenessClock) timer(d time.Duration) (<-chan time.Time, func() bool) {
	fire := make(chan time.Time, 1)
	c.timers <- fakeLivenessTimer{interval: d, fire: fire}
	return fire, func() bool { return true }
}

func (c *fakeLivenessClock) nextTimer(t *testing.T) fakeLivenessTimer {
	t.Helper()
	select {
	case timer := <-c.timers:
		return timer
	case <-time.After(5 * time.Second):
		t.Fatal("liveness monitor never armed its timer")
		return fakeLivenessTimer{}
	}
}

type heartbeatTransport struct {
	*fakeTransport
	entered chan struct{}
	results chan error
}

func (t *heartbeatTransport) Heartbeat(ctx context.Context) error {
	t.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-t.results:
		return err
	}
}

type heartbeatConnector struct {
	mu         sync.Mutex
	transports []*heartbeatTransport
	calls      atomic.Int32
}

func (c *heartbeatConnector) Connect(context.Context, Asset, uint64) (base.Transport, error) {
	call := c.calls.Add(1)
	transport := &heartbeatTransport{
		fakeTransport: newFakeTransport(KindSSH, uint64(call)+2000),
		entered:       make(chan struct{}, 16),
		results:       make(chan error, 16),
	}
	c.mu.Lock()
	c.transports = append(c.transports, transport)
	c.mu.Unlock()
	return transport, nil
}

func (c *heartbeatConnector) transport(index int) *heartbeatTransport {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transports[index]
}

type livenessFixture struct {
	manager   *Manager
	connector *heartbeatConnector
	clock     *fakeLivenessClock
	statuses  chan StatusEvent
	session   *Session
}

func newLivenessFixture(t *testing.T, backoff time.Duration) *livenessFixture {
	t.Helper()
	fixture := &livenessFixture{
		connector: &heartbeatConnector{},
		clock:     newFakeLivenessClock(),
		statuses:  make(chan StatusEvent, 64),
	}
	fixture.manager = NewManager(Config{
		Connector: fixture.connector, Terminals: newFakeTerminalFactory(),
		ReconnectBackoff: []time.Duration{backoff}, Liveness: livenessTestPolicy, OpenAccess: true,
		Emitter: EmitterFunc(func(_ context.Context, event Event) error {
			if status, ok := event.Payload.(StatusEvent); ok {
				fixture.statuses <- status
			}
			return nil
		}),
	})
	fixture.manager.livenessClock = fixture.clock
	t.Cleanup(func() { _ = fixture.manager.Close() })
	session, err := fixture.manager.Connect(context.Background(), Asset{ID: "liveness", Kind: KindSSH})
	if err != nil {
		t.Fatal(err)
	}
	fixture.session = session
	if event := fixture.nextStatus(t); event.Status != StatusConnecting {
		t.Fatalf("first status = %+v, want connecting", event)
	}
	if event := fixture.nextStatus(t); event.Status != StatusConnected {
		t.Fatalf("second status = %+v, want connected", event)
	}
	return fixture
}

func (f *livenessFixture) nextStatus(t *testing.T) StatusEvent {
	t.Helper()
	select {
	case event := <-f.statuses:
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("no session status event")
		return StatusEvent{}
	}
}

// heartbeat fires the monitor's pending timer after advancing the clock by
// its interval and answers the heartbeat it sends with result.
func (f *livenessFixture) heartbeat(t *testing.T, transport *heartbeatTransport, result error) time.Duration {
	t.Helper()
	timer := f.clock.nextTimer(t)
	f.clock.advance(timer.interval)
	timer.fire <- time.Time{}
	<-transport.entered
	transport.results <- result
	return timer.interval
}

func TestLivenessMonitorSurfacesSuspectAndReconnectsWhenDead(t *testing.T) {
	fixture := newLivenessFixture(t, time.Hour)
	first := fixture.connector.transport(0)

	if interval := fixture.heartbeat(t, first, context.DeadlineExceeded); interval != livenessTestPolicy.Interval {
		t.Fatalf("alive heartbeat interval = %v, want %v", interval, livenessTestPolicy.Interval)
	}
	suspect := fixture.nextStatus(t)
	if suspect.Status != StatusConnected || suspect.Liveness != LivenessSuspect {
		t.Fatalf("status after a missed heartbeat = %+v, want connected/suspect", suspect)
	}
	if info := fixture.session.Info(); info.Liveness != LivenessSuspect {
		t.Fatalf("session info liveness = %q, want suspect", info.Liveness)
	}
	if interval := fixture.heartbeat(t, first, context.DeadlineExceeded); interval != livenessTestPolicy.SuspectInterval {
		t.Fatalf("suspect heartbeat interval = %v, want %v", interval, livenessTestPolicy.SuspectInterval)
	}
	fixture.heartbeat(t, first, context.DeadlineExceeded)

	// The third consecutive miss declares the connection dead; the reconnect
	// starts without the (one hour) backoff.
	reconnecting := fixture.nextStatus(t)
	if reconnecting.Status != StatusReconnecting || reconnecting.Liveness != "" {
		t.Fatalf("status after the dead verdict = %+v, want reconnecting", reconnecting)
	}
	connected := fixture.nextStatus(t)
	if connected.Status != StatusConnected || connected.Liveness != "" {
		t.Fatalf("status after reconnect = %+v, want connected and not suspect", connected)
	}
	if got := fixture.connector.calls.Load(); got != 2 {
		t.Fatalf("connector calls = %d, want 2", got)
	}
	if first.IsAlive() {
		t.Fatal("dead transport was not closed by the reconnect")
	}
	// The replacement connection is monitored from a fresh alive state.
	second := fixture.connector.transport(1)
	if interval := fixture.heartbeat(t, second, nil); interval != livenessTestPolicy.Interval {
		t.Fatalf("replacement heartbeat interval = %v, want %v", interval, livenessTestPolicy.Interval)
	}
}

func TestLivenessMonitorRecoversFromSuspect(t *testing.T) {
	fixture := newLivenessFixture(t, time.Hour)
	transport := fixture.connector.transport(0)
	fixture.heartbeat(t, transport, context.DeadlineExceeded)
	if event := fixture.nextStatus(t); event.Liveness != LivenessSuspect {
		t.Fatalf("status = %+v, want suspect", event)
	}
	fixture.heartbeat(t, transport, nil)
	recovered := fixture.nextStatus(t)
	if recovered.Status != StatusConnected || recovered.Liveness != "" {
		t.Fatalf("status after an answered heartbeat = %+v, want connected and not suspect", recovered)
	}
	if got := fixture.connector.calls.Load(); got != 1 {
		t.Fatalf("connector calls = %d, want 1 (no reconnect)", got)
	}
}

func TestLivenessMonitorDoesNotDeclareDeadAcrossSleep(t *testing.T) {
	fixture := newLivenessFixture(t, time.Hour)
	transport := fixture.connector.transport(0)
	fixture.heartbeat(t, transport, context.DeadlineExceeded)
	fixture.nextStatus(t)
	fixture.heartbeat(t, transport, context.DeadlineExceeded)

	// The lid closes for ten minutes while the next heartbeat is pending.
	timer := fixture.clock.nextTimer(t)
	fixture.clock.mu.Lock()
	fixture.clock.now.wall = fixture.clock.now.wall.Add(10 * time.Minute)
	fixture.clock.mu.Unlock()
	fixture.clock.advance(timer.interval)
	timer.fire <- time.Time{}
	<-transport.entered
	transport.results <- context.DeadlineExceeded

	// The monitor keeps probing instead of reconnecting.
	if next := fixture.clock.nextTimer(t); next.interval != livenessTestPolicy.SuspectInterval {
		t.Fatalf("interval after the sleep = %v, want suspect interval", next.interval)
	}
	if got := fixture.connector.calls.Load(); got != 1 {
		t.Fatalf("connector calls = %d, want 1 (no false disconnect)", got)
	}
	if info := fixture.session.Info(); info.Status != StatusConnected || info.Liveness != LivenessSuspect {
		t.Fatalf("session after the sleep = %+v, want connected/suspect", info)
	}
}

func TestLivenessDropsHeartbeatOfSupersededConnection(t *testing.T) {
	fixture := newLivenessFixture(t, 0)
	first := fixture.connector.transport(0)
	timer := fixture.clock.nextTimer(t)
	timer.fire <- time.Time{}
	<-first.entered
	// A manual reconnect replaces the connection while its heartbeat is in
	// flight; the old monitor's outcome must not touch the new connection.
	if err := fixture.manager.Reconnect(context.Background(), fixture.session.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.manager.Close(); err != nil {
		t.Fatal(err)
	}
	if got := fixture.manager.livenessStale.Load(); got != 1 {
		t.Fatalf("dropped stale heartbeat results = %d, want 1", got)
	}
}

func TestProbeLivenessReconnectsDeadConnectionImmediately(t *testing.T) {
	fixture := newLivenessFixture(t, time.Hour)
	fixture.connector.transport(0).alive.Store(false)
	if _, err := fixture.manager.ProbeLivenessFor(context.Background(), fixture.session.ID); err != nil {
		t.Fatal(err)
	}
	reconnecting := fixture.nextStatus(t)
	if reconnecting.Status != StatusReconnecting {
		t.Fatalf("status after probing a dead connection = %+v, want reconnecting", reconnecting)
	}
	if connected := fixture.nextStatus(t); connected.Status != StatusConnected {
		t.Fatalf("status after the immediate reconnect = %+v, want connected", connected)
	}
}

func TestProbeLivenessReportsMissedHeartbeatAsSuspect(t *testing.T) {
	fixture := newLivenessFixture(t, time.Hour)
	transport := fixture.connector.transport(0)
	transport.results <- context.DeadlineExceeded
	info, err := fixture.manager.ProbeLivenessFor(context.Background(), fixture.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != StatusConnected || info.Liveness != LivenessSuspect {
		t.Fatalf("probe result = %+v, want connected/suspect", info)
	}
	transport.results <- nil
	info, err = fixture.manager.ProbeLivenessFor(context.Background(), fixture.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != StatusConnected || info.Liveness != "" {
		t.Fatalf("probe result after an answered heartbeat = %+v, want connected and not suspect", info)
	}
}

func TestProbeLivenessCutsReconnectBackoffShort(t *testing.T) {
	fixture := newLivenessFixture(t, time.Hour)
	fixture.manager.jitter = func() float64 { return 0 }
	if err := fixture.manager.StartReconnect(fixture.session.ID); err != nil {
		t.Fatal(err)
	}
	reconnecting := fixture.nextStatus(t)
	if reconnecting.Status != StatusReconnecting || reconnecting.NextRetryAt == nil {
		t.Fatalf("status = %+v, want reconnecting with a scheduled retry", reconnecting)
	}
	// Without the probe the loop would wait half an hour.
	if _, err := fixture.manager.ProbeLivenessFor(context.Background(), fixture.session.ID); err != nil {
		t.Fatal(err)
	}
	if connected := fixture.nextStatus(t); connected.Status != StatusConnected {
		t.Fatalf("status after the foreground probe = %+v, want connected", connected)
	}
}
