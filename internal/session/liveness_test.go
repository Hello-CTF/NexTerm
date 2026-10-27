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
	Timeout: 10 * time.Second, ProbeTimeout: 5 * time.Second,
	SilenceBudget: 90 * time.Second, SuspendGrace: 30 * time.Second,
}

var livenessTestBase = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

func livenessAt(mono time.Duration) livenessReading {
	return livenessReading{mono: mono, wall: livenessTestBase.Add(mono)}
}

func missed(inbound uint64) livenessObservation {
	return livenessObservation{result: heartbeatMissed, inbound: inbound}
}

func TestLivenessTrackerKeepsSlowLinkUnderBulkOutputAlive(t *testing.T) {
	tracker := newLivenessTracker(livenessTestPolicy, livenessAt(0), 0)
	// Every reply is late, but output keeps arriving for five minutes.
	for step := 1; step <= 20; step++ {
		got := tracker.observe(livenessAt(time.Duration(step)*15*time.Second), missed(uint64(step)*4096))
		if got != LivenessAlive {
			t.Fatalf("step %d: state = %s, want alive while inbound bytes keep arriving", step, got)
		}
	}
}

func TestLivenessTrackerNeedsTotalSilenceForTheWholeBudget(t *testing.T) {
	tracker := newLivenessTracker(livenessTestPolicy, livenessAt(0), 0)
	for at := 15 * time.Second; at < 90*time.Second; at += 5 * time.Second {
		if got := tracker.observe(livenessAt(at), missed(0)); got != LivenessSuspect {
			t.Fatalf("at %v: state = %s, want suspect before the silence budget is spent", at, got)
		}
	}
	if got := tracker.observe(livenessAt(90*time.Second), missed(0)); got != LivenessDead {
		t.Fatalf("after 90s of total silence: state = %s, want dead", got)
	}
}

func TestLivenessTrackerReplyOrTrafficRestartsTheSilenceBudget(t *testing.T) {
	tracker := newLivenessTracker(livenessTestPolicy, livenessAt(0), 0)
	tracker.observe(livenessAt(60*time.Second), missed(0))
	if got := tracker.observe(livenessAt(75*time.Second), missed(10)); got != LivenessAlive {
		t.Fatalf("state after inbound traffic = %s, want alive", got)
	}
	if got := tracker.observe(livenessAt(160*time.Second), missed(10)); got != LivenessSuspect {
		t.Fatalf("85s after the last traffic: state = %s, want suspect", got)
	}
	if got := tracker.observe(livenessAt(165*time.Second), livenessObservation{result: heartbeatOK, inbound: 10}); got != LivenessAlive {
		t.Fatalf("state after a reply = %s, want alive", got)
	}
}

func TestLivenessTrackerIgnoresOlderSmallerInboundSnapshot(t *testing.T) {
	tracker := newLivenessTracker(livenessTestPolicy, livenessAt(0), 0)
	if got := tracker.observe(livenessAt(60*time.Second), missed(100)); got != LivenessAlive {
		t.Fatalf("state after inbound traffic = %s, want alive", got)
	}
	// A late snapshot taken before the 100-byte one is not traffic.
	if got := tracker.observe(livenessAt(70*time.Second), missed(50)); got != LivenessSuspect {
		t.Fatalf("state after a stale smaller snapshot = %s, want suspect", got)
	}
	// The maximum was kept, so the same count again is still not traffic.
	if got := tracker.observe(livenessAt(75*time.Second), missed(100)); got != LivenessSuspect {
		t.Fatalf("state after re-observing the known maximum = %s, want suspect", got)
	}
	if got := tracker.observe(livenessAt(80*time.Second), missed(101)); got != LivenessAlive {
		t.Fatalf("state after genuinely new bytes = %s, want alive", got)
	}
}

func TestLivenessTrackerDeclaresDeadOnLostTransportAtOnce(t *testing.T) {
	tracker := newLivenessTracker(livenessTestPolicy, livenessAt(0), 0)
	got := tracker.observe(livenessAt(time.Second), livenessObservation{result: heartbeatLost, probe: true})
	if got != LivenessDead {
		t.Fatalf("state after a lost transport = %s, want dead", got)
	}
}

func TestLivenessTrackerProbeMissesNeverDeclareDead(t *testing.T) {
	tracker := newLivenessTracker(livenessTestPolicy, livenessAt(0), 0)
	for at := 5 * time.Second; at <= 200*time.Second; at += 5 * time.Second {
		got := tracker.observe(livenessAt(at), livenessObservation{result: heartbeatMissed, probe: true})
		if got != LivenessSuspect {
			t.Fatalf("probe miss at %v: state = %s, want suspect", at, got)
		}
	}
}

func TestLivenessTrackerSuspectIntervalIsShorter(t *testing.T) {
	tracker := newLivenessTracker(livenessTestPolicy, livenessAt(0), 0)
	if got := tracker.nextInterval(); got != livenessTestPolicy.Interval {
		t.Fatalf("alive interval = %v, want %v", got, livenessTestPolicy.Interval)
	}
	tracker.observe(livenessAt(25*time.Second), missed(0))
	if got := tracker.nextInterval(); got != livenessTestPolicy.SuspectInterval {
		t.Fatalf("suspect interval = %v, want %v", got, livenessTestPolicy.SuspectInterval)
	}
}

// A suspend on a platform whose monotonic clock keeps running shows up as a
// monotonic gap far beyond any heartbeat period; without the restart the
// accumulated silence would exceed the budget at once.
func TestLivenessTrackerRestartsSilenceAfterLongMonotonicGap(t *testing.T) {
	tracker := newLivenessTracker(livenessTestPolicy, livenessAt(0), 0)
	tracker.observe(livenessAt(25*time.Second), missed(0))
	wake := 25*time.Second + 10*time.Minute
	if got := tracker.observe(livenessAt(wake), missed(0)); got != LivenessSuspect {
		t.Fatalf("state after a miss across a 10 minute gap = %s, want suspect", got)
	}
	// Silence after the wake still counts once the grace window is over.
	for at := wake + 5*time.Second; at < wake+90*time.Second; at += 5 * time.Second {
		if got := tracker.observe(livenessAt(at), missed(0)); got != LivenessSuspect {
			t.Fatalf("%v after wake: state = %s, want suspect", at-wake, got)
		}
	}
	if got := tracker.observe(livenessAt(wake+90*time.Second), missed(0)); got != LivenessDead {
		t.Fatalf("90s of silence after wake: state = %s, want dead", got)
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

func TestReconnectBackoffWakeIsHonoredOnlyWhenWakeable(t *testing.T) {
	// A closed channel is always ready, standing in for a probe that keeps
	// arriving.
	wake := make(chan struct{})
	close(wake)
	woken, err := waitReconnectBackoff(context.Background(), time.Hour, wake, true)
	if err != nil || !woken {
		t.Fatalf("wakeable wait = (%v, %v), want woken", woken, err)
	}
	woken, err = waitReconnectBackoff(context.Background(), time.Millisecond, wake, false)
	if err != nil || woken {
		t.Fatalf("non-wakeable wait = (%v, %v), want the full delay", woken, err)
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

// suspend models a sleep on a platform whose monotonic clock stops.
func (c *fakeLivenessClock) suspend(d time.Duration) {
	c.mu.Lock()
	c.now.wall = c.now.wall.Add(d)
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
	inbound atomic.Uint64
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

func (t *heartbeatTransport) InboundBytes() uint64 { return t.inbound.Load() }

type heartbeatConnector struct {
	mu         sync.Mutex
	transports []*heartbeatTransport
	calls      atomic.Int32
}

func (c *heartbeatConnector) Connect(_ context.Context, asset Asset, _ uint64) (base.Transport, error) {
	call := c.calls.Add(1)
	transport := &heartbeatTransport{
		fakeTransport: newFakeTransport(asset.Kind, uint64(call)+2000),
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

func newLivenessFixture(t *testing.T, backoff time.Duration, kind ...string) *livenessFixture {
	t.Helper()
	assetKind := KindSSH
	if len(kind) > 0 {
		assetKind = kind[0]
	}
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
	session, err := fixture.manager.Connect(context.Background(), Asset{ID: "liveness", Kind: assetKind})
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

func (f *livenessFixture) noStatus(t *testing.T) {
	t.Helper()
	select {
	case event := <-f.statuses:
		t.Fatalf("unexpected status event %+v", event)
	default:
	}
}

// heartbeat fires the monitor's pending timer after advancing the clock by
// its interval and answers the heartbeat it sends with result. It returns
// once the outcome was observed (the monitor armed its next timer) unless
// the outcome ends the monitor.
func (f *livenessFixture) heartbeat(t *testing.T, transport *heartbeatTransport, result error, before ...func()) time.Duration {
	t.Helper()
	timer := f.clock.nextTimer(t)
	// The armed timer proves the previous outcome was observed.
	for _, run := range before {
		run()
	}
	f.clock.advance(timer.interval)
	timer.fire <- time.Time{}
	<-transport.entered
	transport.results <- result
	return timer.interval
}

// probe runs one foreground probe whose heartbeat gets result.
func (f *livenessFixture) probe(t *testing.T, transport *heartbeatTransport, result error) SessionInfo {
	t.Helper()
	transport.results <- result
	info, err := f.manager.ProbeLivenessFor(context.Background(), f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	<-transport.entered
	return info
}

func TestLivenessMonitorSurfacesSuspectAndRecovers(t *testing.T) {
	fixture := newLivenessFixture(t, time.Hour)
	transport := fixture.connector.transport(0)
	if interval := fixture.heartbeat(t, transport, context.DeadlineExceeded); interval != livenessTestPolicy.Interval {
		t.Fatalf("alive heartbeat interval = %v, want %v", interval, livenessTestPolicy.Interval)
	}
	suspect := fixture.nextStatus(t)
	if suspect.Status != StatusConnected || suspect.Liveness != LivenessSuspect {
		t.Fatalf("status after a missed heartbeat = %+v, want connected/suspect", suspect)
	}
	if info := fixture.session.Info(); info.Liveness != LivenessSuspect {
		t.Fatalf("session info liveness = %q, want suspect", info.Liveness)
	}
	if interval := fixture.heartbeat(t, transport, nil); interval != livenessTestPolicy.SuspectInterval {
		t.Fatalf("suspect heartbeat interval = %v, want %v", interval, livenessTestPolicy.SuspectInterval)
	}
	recovered := fixture.nextStatus(t)
	if recovered.Status != StatusConnected || recovered.Liveness != "" {
		t.Fatalf("status after an answered heartbeat = %+v, want connected and not suspect", recovered)
	}
	if got := fixture.connector.calls.Load(); got != 1 {
		t.Fatalf("connector calls = %d, want 1 (no reconnect)", got)
	}
}

func TestLivenessMonitorKeepsSlowLinkUnderBulkOutput(t *testing.T) {
	fixture := newLivenessFixture(t, time.Hour)
	transport := fixture.connector.transport(0)
	// Five minutes of late replies while output keeps streaming in.
	for step := 0; step < 20; step++ {
		fixture.heartbeat(t, transport, context.DeadlineExceeded, func() { transport.inbound.Add(64 << 10) })
	}
	fixture.clock.nextTimer(t)
	fixture.noStatus(t)
	if info := fixture.session.Info(); info.Status != StatusConnected || info.Liveness != "" {
		t.Fatalf("session = %+v, want connected and not suspect", info)
	}
	if got := fixture.connector.calls.Load(); got != 1 {
		t.Fatalf("connector calls = %d, want 1 (no reconnect)", got)
	}
}

func TestLivenessMonitorReconnectsAfterTotalSilence(t *testing.T) {
	fixture := newLivenessFixture(t, time.Hour)
	first := fixture.connector.transport(0)
	fixture.heartbeat(t, first, context.DeadlineExceeded)
	if event := fixture.nextStatus(t); event.Liveness != LivenessSuspect {
		t.Fatalf("status = %+v, want suspect", event)
	}
	for fixture.clock.read().mono < livenessTestPolicy.SilenceBudget {
		fixture.heartbeat(t, first, context.DeadlineExceeded)
	}
	// Total silence for the budget declares the link dead; the reconnect
	// starts without the (one hour) backoff and with no scheduled retry.
	reconnecting := fixture.nextStatus(t)
	if reconnecting.Status != StatusReconnecting || reconnecting.NextRetryAt != nil {
		t.Fatalf("status after the dead verdict = %+v, want reconnecting with no scheduled retry", reconnecting)
	}
	if connected := fixture.nextStatus(t); connected.Status != StatusConnected || connected.Liveness != "" {
		t.Fatalf("status after reconnect = %+v, want connected and not suspect", connected)
	}
	if first.IsAlive() {
		t.Fatal("dead transport was not closed by the reconnect")
	}
	second := fixture.connector.transport(1)
	if interval := fixture.heartbeat(t, second, nil); interval != livenessTestPolicy.Interval {
		t.Fatalf("replacement heartbeat interval = %v, want %v", interval, livenessTestPolicy.Interval)
	}
}

func TestLivenessMonitorReconnectsOnHardErrorAtOnce(t *testing.T) {
	fixture := newLivenessFixture(t, time.Hour)
	fixture.heartbeat(t, fixture.connector.transport(0), base.ErrDisconnected)
	if event := fixture.nextStatus(t); event.Status != StatusReconnecting {
		t.Fatalf("status after a hard heartbeat error = %+v, want reconnecting", event)
	}
	if event := fixture.nextStatus(t); event.Status != StatusConnected {
		t.Fatalf("status = %+v, want connected", event)
	}
}

func TestLivenessClosesTransportWhenReconnectCannotStart(t *testing.T) {
	// Local sessions cannot reconnect, so the dead verdict falls back to the
	// ordinary disconnect path.
	fixture := newLivenessFixture(t, time.Hour, KindLocal)
	transport := fixture.connector.transport(0)
	fixture.heartbeat(t, transport, base.ErrDisconnected)
	event := fixture.nextStatus(t)
	if event.Status != StatusConnected || event.Liveness != LivenessSuspect {
		t.Fatalf("status = %+v, want connected/suspect rather than a healthy connection", event)
	}
	if transport.IsAlive() {
		t.Fatal("transport of a dead, unreconnectable connection was left open")
	}
}

// After a 10 minute suspend the foreground fires visible, online and wake
// probes a few seconds apart while Wi-Fi needs 15s to come back.
func TestLivenessWakeWithNetworkStillDownIsNotDead(t *testing.T) {
	fixture := newLivenessFixture(t, time.Hour)
	transport := fixture.connector.transport(0)
	timer := fixture.clock.nextTimer(t)
	fixture.clock.advance(10 * time.Second)
	fixture.clock.suspend(10 * time.Minute)

	if info := fixture.probe(t, transport, context.DeadlineExceeded); info.Status != StatusConnected {
		t.Fatalf("visible probe = %+v, want connected", info)
	}
	if event := fixture.nextStatus(t); event.Liveness != LivenessSuspect {
		t.Fatalf("status = %+v, want suspect", event)
	}
	fixture.clock.advance(2 * time.Second)
	fixture.probe(t, transport, context.DeadlineExceeded)
	fixture.clock.advance(3 * time.Second)
	fixture.probe(t, transport, context.DeadlineExceeded)
	// The background heartbeat keeps missing until the network is back.
	fixture.clock.advance(5 * time.Second)
	timer.fire <- time.Time{}
	<-transport.entered
	transport.results <- context.DeadlineExceeded
	fixture.heartbeat(t, transport, context.DeadlineExceeded)
	fixture.heartbeat(t, transport, nil)
	if event := fixture.nextStatus(t); event.Status != StatusConnected || event.Liveness != "" {
		t.Fatalf("status once the network is back = %+v, want connected and not suspect", event)
	}
	fixture.noStatus(t)
	if got := fixture.connector.calls.Load(); got != 1 {
		t.Fatalf("connector calls = %d, want 1 (no false disconnect)", got)
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
	if reconnecting.Status != StatusReconnecting || reconnecting.NextRetryAt != nil {
		t.Fatalf("status after probing a dead connection = %+v, want reconnecting with no scheduled retry", reconnecting)
	}
	if connected := fixture.nextStatus(t); connected.Status != StatusConnected {
		t.Fatalf("status after the immediate reconnect = %+v, want connected", connected)
	}
}

func TestProbeLivenessMissStaysSuspect(t *testing.T) {
	fixture := newLivenessFixture(t, time.Hour)
	transport := fixture.connector.transport(0)
	for range 5 {
		fixture.clock.advance(30 * time.Second)
		if info := fixture.probe(t, transport, context.DeadlineExceeded); info.Status != StatusConnected || info.Liveness != LivenessSuspect {
			t.Fatalf("probe result = %+v, want connected/suspect", info)
		}
	}
	if info := fixture.probe(t, transport, nil); info.Status != StatusConnected || info.Liveness != "" {
		t.Fatalf("probe result after an answered heartbeat = %+v, want connected and not suspect", info)
	}
	if got := fixture.connector.calls.Load(); got != 1 {
		t.Fatalf("connector calls = %d, want 1", got)
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
