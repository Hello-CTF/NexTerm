package localhelper

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
)

type fakeHelper struct {
	client  *supervisor.Client
	exited  chan struct{}
	spawned bool
}

func (h *fakeHelper) Client() *supervisor.Client { return h.client }
func (h *fakeHelper) Exited() <-chan struct{}    { return h.exited }
func (h *fakeHelper) Spawned() bool              { return h.spawned }
func (h *fakeHelper) JobBound() bool             { return false }

// fakeWorld scripts what the service finds on the machine: the helper that
// InspectHelper reports, and what ConnectHelper does.
type fakeWorld struct {
	mu        sync.Mutex
	running   *supervisor.RunningHelper
	connects  int
	stops     []supervisor.RunningHelper
	connect   func(attempt int) (helperHandle, error)
	connected chan *fakeHelper
}

func newFakeWorld() *fakeWorld {
	return &fakeWorld{connected: make(chan *fakeHelper, 16)}
}

func (w *fakeWorld) config(t *testing.T) Config {
	stateDir := filepath.Join(t.TempDir(), "state")
	return Config{
		StateDir:     stateDir,
		Logger:       slog.New(slog.DiscardHandler),
		Executable:   "/nonexistent/helper",
		QuickExit:    time.Hour,
		Backoff:      []time.Duration{time.Millisecond},
		PollInterval: 10 * time.Millisecond,
		inspect: func(context.Context, string) (supervisor.RunningHelper, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			if w.running == nil {
				return supervisor.RunningHelper{}, supervisor.ErrUnavailable
			}
			return *w.running, nil
		},
		stop: func(_ context.Context, _ string, expected supervisor.RunningHelper) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.stops = append(w.stops, expected)
			if w.running == nil || w.running.PID != expected.PID || w.running.Activity.Total() > expected.Activity.Total() {
				return supervisor.ErrHelperChanged
			}
			w.running = nil
			return nil
		},
		connect: func(context.Context, supervisor.HelperConfig) (helperHandle, error) {
			w.mu.Lock()
			w.connects++
			attempt := w.connects
			connect := w.connect
			w.mu.Unlock()
			if connect != nil {
				return connect(attempt)
			}
			helper := &fakeHelper{client: supervisor.NewClient(filepath.Join(stateDir, "none.sock"), stateDir), exited: make(chan struct{}), spawned: true}
			w.connected <- helper
			return helper, nil
		},
		selfDigest: func() (string, error) { return "self", nil },
	}
}

func (w *fakeWorld) connectCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.connects
}

func waitState(t *testing.T, service *Service, want State) Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		status := service.Status()
		if status.State == want {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %+v, want state %s", status, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startService(t *testing.T, config Config) *Service {
	t.Helper()
	service := New(config)
	service.Start()
	t.Cleanup(service.Close)
	return service
}

func TestServiceStopsRespawningAHelperThatKeepsExitingAtOnce(t *testing.T) {
	world := newFakeWorld()
	config := world.config(t)
	config.BreakerThreshold = 3
	go func() {
		for helper := range world.connected {
			close(helper.exited)
		}
	}()
	t.Cleanup(func() { close(world.connected) })
	service := startService(t, config)
	status := waitState(t, service, StateFailed)
	if status.Reason == "" {
		t.Fatal("an open breaker must explain itself")
	}
	if count := world.connectCount(); count != 3 {
		t.Fatalf("connects = %d, want exactly the breaker threshold 3", count)
	}
	time.Sleep(100 * time.Millisecond)
	if count := world.connectCount(); count != 3 {
		t.Fatalf("connects after the breaker opened = %d; respawning must stop", count)
	}
	if _, err := service.Provider(); !errors.Is(err, durable.ErrUnavailable) {
		t.Fatalf("provider of a failed helper = %v, want unavailable", err)
	}
	if err := service.Retry(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return world.connectCount() > 3 })
}

func TestServiceCountsStartFailuresTowardTheBreaker(t *testing.T) {
	world := newFakeWorld()
	world.connect = func(int) (helperHandle, error) {
		return nil, supervisor.ErrHelperExited
	}
	config := world.config(t)
	config.BreakerThreshold = 2
	service := startService(t, config)
	waitState(t, service, StateFailed)
	if count := world.connectCount(); count != 2 {
		t.Fatalf("connects = %d, want 2", count)
	}
}

func TestServiceResetsTheBreakerAfterAHelperThatRanLongEnough(t *testing.T) {
	world := newFakeWorld()
	config := world.config(t)
	config.QuickExit = 0
	config.BreakerThreshold = 2
	clock := struct {
		sync.Mutex
		now time.Time
	}{now: time.Unix(1_700_000_000, 0)}
	config.now = func() time.Time {
		clock.Lock()
		defer clock.Unlock()
		clock.now = clock.now.Add(time.Minute)
		return clock.now
	}
	config.QuickExit = 30 * time.Second
	go func() {
		for helper := range world.connected {
			close(helper.exited)
		}
	}()
	t.Cleanup(func() { close(world.connected) })
	service := startService(t, config)
	waitFor(t, func() bool { return world.connectCount() >= 5 })
	if state := service.Status().State; state == StateFailed {
		t.Fatalf("helpers that ran past QuickExit opened the breaker: %+v", service.Status())
	}
}

func TestServiceAsksBeforeReplacingAnOutdatedHelperWithSessions(t *testing.T) {
	world := newFakeWorld()
	world.running = &supervisor.RunningHelper{Protocol: supervisor.ProtocolVersion, PID: 4242, Activity: supervisor.ActivitySnapshot{PTY: 2},
		Runtime: &supervisor.RuntimeMetadata{Version: "1.2.9", SHA256: "other"}}
	service := startService(t, world.config(t))
	status := waitState(t, service, StateOutdated)
	if status.ActiveSessions != 2 || status.ConsentToken == "" || status.HelperVersion != "1.2.9" || status.Reason == "" {
		t.Fatalf("outdated status = %+v", status)
	}
	time.Sleep(50 * time.Millisecond)
	if world.connectCount() != 0 || len(world.stops) != 0 {
		t.Fatalf("an outdated helper with sessions was replaced without consent: connects %d stops %d", world.connectCount(), len(world.stops))
	}
	if _, err := service.Provider(); err == nil {
		t.Fatal("new terminals must not go to an outdated helper before consent")
	}
	if _, err := service.WaitExisting(context.Background(), 0); err != nil {
		t.Fatalf("existing sessions of a same-protocol helper must stay reachable: %v", err)
	}
	if err := service.Restart(context.Background(), "3:4243:2"); !errors.Is(err, ErrStaleConsent) {
		t.Fatalf("restart with a token for another helper = %v, want ErrStaleConsent", err)
	}
	if err := service.Restart(context.Background(), status.ConsentToken); err != nil {
		t.Fatal(err)
	}
	waitState(t, service, StateReady)
	world.mu.Lock()
	defer world.mu.Unlock()
	if len(world.stops) != 1 || world.stops[0].PID != 4242 {
		t.Fatalf("stops = %+v, want one stop of pid 4242", world.stops)
	}
}

func TestServiceRefusesConsentWhenTheOutdatedHelperGainedSessions(t *testing.T) {
	world := newFakeWorld()
	world.running = &supervisor.RunningHelper{Protocol: 2, PID: 7, Activity: supervisor.ActivitySnapshot{PTY: 1}}
	config := world.config(t)
	config.PollInterval = time.Hour
	service := startService(t, config)
	status := waitState(t, service, StateOutdated)
	if _, err := service.WaitExisting(context.Background(), 0); err == nil {
		t.Fatal("a protocol 2 helper cannot serve this client's sessions")
	}
	world.mu.Lock()
	world.running.Activity.PTY = 3
	world.mu.Unlock()
	if err := service.Restart(context.Background(), status.ConsentToken); !errors.Is(err, ErrStaleConsent) {
		t.Fatalf("restart after sessions grew = %v, want ErrStaleConsent", err)
	}
	world.mu.Lock()
	running := world.running
	world.mu.Unlock()
	if running == nil {
		t.Fatal("the helper was stopped although the user approved fewer sessions")
	}
	updated := waitState(t, service, StateOutdated)
	if updated.ActiveSessions != 3 {
		t.Fatalf("status after refused consent = %+v, want the new count", updated)
	}
}

func TestServiceReplacesAnIdleOutdatedHelperWithoutAsking(t *testing.T) {
	world := newFakeWorld()
	world.running = &supervisor.RunningHelper{Protocol: 2, PID: 9}
	service := startService(t, world.config(t))
	waitState(t, service, StateReady)
	world.mu.Lock()
	defer world.mu.Unlock()
	if len(world.stops) != 1 || world.stops[0].PID != 9 {
		t.Fatalf("stops = %+v, want the idle helper replaced", world.stops)
	}
}

func TestServiceUsesACurrentHelperOfThisBuild(t *testing.T) {
	world := newFakeWorld()
	world.running = &supervisor.RunningHelper{Protocol: supervisor.ProtocolVersion, PID: 11, Activity: supervisor.ActivitySnapshot{PTY: 4},
		Runtime: &supervisor.RuntimeMetadata{Version: "dev", SHA256: "self"}}
	service := startService(t, world.config(t))
	waitState(t, service, StateReady)
	if len(world.stops) != 0 {
		t.Fatalf("a helper of this build was stopped: %+v", world.stops)
	}
	if _, err := service.Provider(); err != nil {
		t.Fatal(err)
	}
}

func TestDisabledServiceNeverStarts(t *testing.T) {
	world := newFakeWorld()
	config := world.config(t)
	config.StateDir = ""
	service := New(config)
	service.Start()
	defer service.Close()
	if status := service.Status(); status.State != StateDisabled || status.Reason == "" {
		t.Fatalf("status = %+v, want disabled with a reason", status)
	}
	if err := service.Retry(context.Background()); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("retry = %v", err)
	}
	if world.connectCount() != 0 {
		t.Fatal("a disabled service spawned a helper")
	}
}

func TestWaitReadyWaitsOnlyWhileStarting(t *testing.T) {
	world := newFakeWorld()
	release := make(chan struct{})
	world.connect = func(int) (helperHandle, error) {
		<-release
		return &fakeHelper{client: supervisor.NewClient("/nonexistent.sock", "/nonexistent"), exited: make(chan struct{}), spawned: true}, nil
	}
	service := startService(t, world.config(t))
	if _, err := service.WaitReady(context.Background(), 20*time.Millisecond); err == nil {
		t.Fatal("a starting helper reported ready")
	} else {
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) || !unavailable.Starting() {
			t.Fatalf("starting error = %v, want a starting UnavailableError", err)
		}
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(release)
	}()
	if _, err := service.WaitReady(context.Background(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
