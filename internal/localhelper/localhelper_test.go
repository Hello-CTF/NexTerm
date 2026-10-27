package localhelper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
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
	pid     int
}

func (h *fakeHelper) Client() *supervisor.Client { return h.client }
func (h *fakeHelper) Exited() <-chan struct{}    { return h.exited }
func (h *fakeHelper) Spawned() bool              { return h.spawned }
func (h *fakeHelper) PID() int                   { return h.pid }
func (h *fakeHelper) JobBound() bool             { return false }

// fakeWorld scripts what the service finds on the machine: the helper that
// InspectHelper reports, and what ConnectHelper does. A connect while a
// helper is reported attaches to it; otherwise it "spawns" one.
type fakeWorld struct {
	mu      sync.Mutex
	running *supervisor.RunningHelper
	// inspectErr, when set, is what inspection returns instead.
	inspectErr error
	inspects   int
	connects   int
	stops      []supervisor.RunningHelper
	connect    func(attempt int) (helperHandle, error)
	connected  chan *fakeHelper
	nextPID    int
	// vanishOnAttach makes the reported helper exit before an attach.
	vanishOnAttach bool
}

func newFakeWorld() *fakeWorld {
	return &fakeWorld{connected: make(chan *fakeHelper, 64), nextPID: 100}
}

func (w *fakeWorld) setRunning(running *supervisor.RunningHelper) {
	w.mu.Lock()
	w.running = running
	w.mu.Unlock()
}

func (w *fakeWorld) config(t *testing.T) Config {
	stateDir := filepath.Join(t.TempDir(), "state")
	return Config{
		StateDir:     stateDir,
		Executable:   "/nonexistent/helper",
		Logger:       slog.New(slog.DiscardHandler),
		QuickExit:    time.Hour,
		Backoff:      []time.Duration{time.Millisecond},
		PollInterval: 10 * time.Millisecond,
		inspect: func(context.Context, string) (supervisor.RunningHelper, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.inspects++
			if w.inspectErr != nil {
				return supervisor.RunningHelper{}, w.inspectErr
			}
			if w.running == nil {
				return supervisor.RunningHelper{}, fmt.Errorf("%w: test", supervisor.ErrHelperNotRunning)
			}
			return *w.running, nil
		},
		stop: func(_ context.Context, _ string, expected supervisor.RunningHelper) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.stops = append(w.stops, expected)
			if w.running == nil || w.running.PID != expected.PID ||
				expected.ActivityKnown && w.running.Activity.Total() > expected.Activity.Total() {
				return supervisor.ErrHelperChanged
			}
			w.running = nil
			return nil
		},
		connect: func(_ context.Context, helperConfig supervisor.HelperConfig) (helperHandle, error) {
			w.mu.Lock()
			if helperConfig.NoSpawn && (w.running == nil || w.vanishOnAttach) {
				w.running = nil
				w.mu.Unlock()
				return nil, fmt.Errorf("%w: test", supervisor.ErrHelperNotRunning)
			}
			w.connects++
			attempt := w.connects
			connect := w.connect
			running := w.running
			w.nextPID++
			pid := w.nextPID
			w.mu.Unlock()
			if connect != nil {
				return connect(attempt)
			}
			helper := &fakeHelper{client: supervisor.NewClient(filepath.Join(stateDir, "none.sock"), stateDir), spawned: true, pid: pid}
			if running != nil {
				helper.spawned, helper.pid = false, running.PID
			} else {
				helper.exited = make(chan struct{})
			}
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

func (w *fakeWorld) stopCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.stops)
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
		time.Sleep(2 * time.Millisecond)
	}
}

func startService(t *testing.T, config Config) *Service {
	t.Helper()
	service := New(config)
	service.Start()
	t.Cleanup(service.Close)
	return service
}

// settled proves the lifecycle goroutine is parked in a wait that answers
// requests: a restart round trip completes only there, so anything it would
// have done before has happened.
func settled(t *testing.T, service *Service) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return service.Restart(ctx, "0:0:0:0")
}

func ready(t *testing.T, service *Service) {
	t.Helper()
	if _, err := service.WaitReady(context.Background(), 10*time.Second); err != nil {
		t.Fatalf("helper not ready: %v (%+v)", err, service.Status())
	}
}

func TestServiceStartsIdleAndSpawnsOnlyForADurableTerminal(t *testing.T) {
	world := newFakeWorld()
	service := startService(t, world.config(t))
	if _, err := service.WaitExisting(context.Background(), 10*time.Second); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("existing terminals without a helper = %v, want ErrNotRunning", err)
	}
	if !errors.Is(settled(t, service), ErrNotOutdated) || world.connectCount() != 0 {
		t.Fatalf("an idle service spawned a helper: %d connects", world.connectCount())
	}
	ready(t, service)
	if world.connectCount() != 1 {
		t.Fatalf("connects = %d, want one spawn for the durable terminal", world.connectCount())
	}
}

func TestServiceStopsRespawningAHelperThatKeepsExitingAtOnce(t *testing.T) {
	world := newFakeWorld()
	config := world.config(t)
	config.BreakerThreshold = 3
	service := startService(t, config)
	for attempt := 1; attempt <= 3; attempt++ {
		ready(t, service)
		helper := <-world.connected
		close(helper.exited)
		if attempt < 3 {
			waitState(t, service, StateIdle)
		}
	}
	status := waitState(t, service, StateFailed)
	if status.Reason == "" {
		t.Fatal("an open breaker must explain itself")
	}
	if _, err := service.WaitReady(context.Background(), time.Second); !errors.Is(err, durable.ErrUnavailable) {
		t.Fatalf("a terminal with an open breaker = %v, want unavailable", err)
	}
	if !errors.Is(settled(t, service), ErrNotOutdated) || world.connectCount() != 3 {
		t.Fatalf("connects with the breaker open = %d; respawning must stop", world.connectCount())
	}
	if err := service.Retry(context.Background()); err != nil {
		t.Fatal(err)
	}
	ready(t, service)
	if world.connectCount() != 4 {
		t.Fatalf("connects after retry = %d, want 4", world.connectCount())
	}
}

func TestServiceCountsStartFailuresTowardTheBreaker(t *testing.T) {
	world := newFakeWorld()
	world.connect = func(int) (helperHandle, error) {
		return nil, supervisor.ErrHelperExited
	}
	config := world.config(t)
	config.BreakerThreshold = 2
	service := startService(t, config)
	if _, err := service.WaitReady(context.Background(), 10*time.Millisecond); err == nil {
		t.Fatal("a helper that cannot start reported ready")
	}
	waitState(t, service, StateFailed)
	if !errors.Is(settled(t, service), ErrNotOutdated) || world.connectCount() != 2 {
		t.Fatalf("connects = %d, want 2", world.connectCount())
	}
}

func TestServiceResetsTheBreakerAfterAHelperThatRanLongEnough(t *testing.T) {
	world := newFakeWorld()
	config := world.config(t)
	config.BreakerThreshold = 2
	config.QuickExit = 30 * time.Second
	var clock struct {
		sync.Mutex
		now time.Time
	}
	clock.now = time.Unix(1_700_000_000, 0)
	config.now = func() time.Time {
		clock.Lock()
		defer clock.Unlock()
		clock.now = clock.now.Add(time.Minute)
		return clock.now
	}
	service := startService(t, config)
	for attempt := 0; attempt < 4; attempt++ {
		ready(t, service)
		close((<-world.connected).exited)
		waitState(t, service, StateIdle)
	}
}

func TestServiceDoesNotCountAHelperAPeerReplaced(t *testing.T) {
	world := newFakeWorld()
	config := world.config(t)
	config.BreakerThreshold = 2
	service := startService(t, config)
	for attempt := 0; attempt < 3; attempt++ {
		ready(t, service)
		spawned := <-world.connected
		world.setRunning(&supervisor.RunningHelper{Protocol: supervisor.ProtocolVersion, PID: 900 + attempt, ActivityKnown: true,
			Runtime: &supervisor.RuntimeMetadata{SHA256: "self"}})
		close(spawned.exited)
		if peer := <-world.connected; peer.spawned {
			t.Fatal("a running peer must be attached to, not replaced")
		}
		waitState(t, service, StateReady)
		world.setRunning(nil)
		waitState(t, service, StateIdle)
	}
}

func TestServiceAsksBeforeRestartingAnOlderProtocolHelperWithSessions(t *testing.T) {
	world := newFakeWorld()
	world.running = &supervisor.RunningHelper{Protocol: 2, PID: 7, Activity: supervisor.ActivitySnapshot{PTY: 1}, ActivityKnown: true}
	config := world.config(t)
	config.PollInterval = time.Hour
	service := startService(t, config)
	status := waitState(t, service, StateOutdated)
	if status.ActiveSessions != 1 || status.ConsentToken == "" || status.Reason == "" {
		t.Fatalf("outdated status = %+v", status)
	}
	if _, err := service.WaitReady(context.Background(), time.Second); err == nil {
		t.Fatal("new terminals must not go to a helper of another protocol")
	}
	if _, err := service.WaitExisting(context.Background(), 0); err == nil || errors.Is(err, ErrNotRunning) {
		t.Fatalf("existing terminals of a protocol 2 helper = %v, want the outdated reason", err)
	}
	if err := service.Restart(context.Background(), "2:8:1:1"); !errors.Is(err, ErrStaleConsent) {
		t.Fatalf("restart for another pid = %v, want ErrStaleConsent", err)
	}
	world.mu.Lock()
	world.running.Activity.PTY = 3
	world.mu.Unlock()
	if err := service.Restart(context.Background(), status.ConsentToken); !errors.Is(err, ErrStaleConsent) {
		t.Fatalf("restart after sessions grew = %v, want ErrStaleConsent", err)
	}
	updated := waitState(t, service, StateOutdated)
	for updated.ActiveSessions != 3 {
		time.Sleep(time.Millisecond)
		updated = service.Status()
	}
	if world.connectCount() != 0 {
		t.Fatal("a helper was started beside the outdated one")
	}
	if err := service.Restart(context.Background(), updated.ConsentToken); err != nil {
		t.Fatal(err)
	}
	waitState(t, service, StateReady)
}

func TestServiceNeverStopsAHelperWithAnUnknownSessionCount(t *testing.T) {
	world := newFakeWorld()
	world.running = &supervisor.RunningHelper{Protocol: 2, PID: 7}
	service := startService(t, world.config(t))
	if _, err := service.WaitReady(context.Background(), time.Second); err == nil {
		t.Fatal("a helper with unknown sessions was replaced for a new terminal")
	}
	status := waitState(t, service, StateOutdated)
	if status.ActivityKnown || !strings.Contains(status.Reason, "数量未知") {
		t.Fatalf("status = %+v, want an unknown session count", status)
	}
	if !errors.Is(settled(t, service), ErrStaleConsent) || world.stopCount() != 0 {
		t.Fatalf("stops = %d; an unknown session count must never be stopped as idle", world.stopCount())
	}
}

func TestServiceReplacesAnIdleOlderProtocolHelperOnlyWhenATerminalNeedsOne(t *testing.T) {
	world := newFakeWorld()
	world.running = &supervisor.RunningHelper{Protocol: 2, PID: 9, ActivityKnown: true}
	config := world.config(t)
	config.PollInterval = time.Hour
	service := startService(t, config)
	waitState(t, service, StateOutdated)
	if world.stopCount() != 0 {
		t.Fatal("an idle helper was replaced before any terminal needed one")
	}
	ready(t, service)
	world.mu.Lock()
	defer world.mu.Unlock()
	if len(world.stops) != 1 || world.stops[0].PID != 9 {
		t.Fatalf("stops = %+v, want the idle helper replaced", world.stops)
	}
}

func TestServiceKeepsUsingASameProtocolHelperFromAnotherBuild(t *testing.T) {
	world := newFakeWorld()
	world.running = &supervisor.RunningHelper{Protocol: supervisor.ProtocolVersion, PID: 11, Activity: supervisor.ActivitySnapshot{PTY: 4}, ActivityKnown: true,
		Runtime: &supervisor.RuntimeMetadata{Version: "1.2.9", SHA256: "other"}}
	service := startService(t, world.config(t))
	ready(t, service)
	status := waitState(t, service, StateReady)
	if !status.UpdateAvailable || status.ActiveSessions != 4 || status.HelperVersion != "1.2.9" || status.ConsentToken == "" {
		t.Fatalf("status = %+v, want a ready helper that can be updated", status)
	}
	if world.stopCount() != 0 {
		t.Fatal("a helper of another build was stopped without consent")
	}
	if err := service.Restart(context.Background(), status.ConsentToken); err != nil {
		t.Fatal(err)
	}
	waitState(t, service, StateReady)
	for service.Status().UpdateAvailable {
		time.Sleep(time.Millisecond)
	}
	if world.stopCount() != 1 {
		t.Fatalf("stops = %d, want the consented restart only", world.stopCount())
	}
}

func TestServiceUsesACurrentHelperOfThisBuildWithoutAskingToUpdate(t *testing.T) {
	world := newFakeWorld()
	world.running = &supervisor.RunningHelper{Protocol: supervisor.ProtocolVersion, PID: 11, Activity: supervisor.ActivitySnapshot{PTY: 4}, ActivityKnown: true,
		Runtime: &supervisor.RuntimeMetadata{Version: "dev", SHA256: "self"}}
	service := startService(t, world.config(t))
	if _, err := service.WaitExisting(context.Background(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if status := service.Status(); status.UpdateAvailable {
		t.Fatalf("a helper of this build offered an update: %+v", status)
	}
	if !errors.Is(settled(t, service), ErrNotOutdated) || world.stopCount() != 0 {
		t.Fatalf("a helper of this build was stopped: %d", world.stopCount())
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
	if _, err := service.WaitReady(context.Background(), time.Second); err == nil {
		t.Fatal("a disabled service reported ready")
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
	close(release)
	ready(t, service)
}

func (w *fakeWorld) inspectCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inspects
}

func TestServiceReportsAHelperThatCannotBeInspectedInsteadOfNoHelper(t *testing.T) {
	for name, inspectErr := range map[string]error{
		"hello timeout": fmt.Errorf("%w: cannot be inspected: %w", supervisor.ErrUnavailable, context.DeadlineExceeded),
		"pid mismatch":  fmt.Errorf("%w: endpoint served by pid 2, helper.pid names 3", supervisor.ErrStateMismatch),
	} {
		t.Run(name, func(t *testing.T) {
			world := newFakeWorld()
			world.inspectErr = inspectErr
			service := startService(t, world.config(t))
			_, err := service.WaitExisting(context.Background(), 10*time.Second)
			var unavailable *UnavailableError
			if errors.Is(err, ErrNotRunning) || !errors.As(err, &unavailable) || unavailable.Status.Reason == "" {
				t.Fatalf("existing terminals of an uninspectable helper = %v, want an unavailable reason", err)
			}
			if _, err := service.WaitReady(context.Background(), time.Second); err == nil {
				t.Fatal("a new terminal went to a helper that cannot be inspected")
			}
			if status := service.Status(); status.State != StateFailed {
				t.Fatalf("status = %+v, want failed with a reason", status)
			}
			if !errors.Is(settled(t, service), ErrNotOutdated) || world.connectCount() != 0 {
				t.Fatalf("connects = %d; nothing may be spawned beside a helper that answers", world.connectCount())
			}
			world.mu.Lock()
			world.inspectErr = nil
			world.mu.Unlock()
			// Once it can be checked, the earlier durable request is served.
			waitState(t, service, StateReady)
		})
	}
}

func TestServiceKeepsServingAHelperThatMissesAnInspection(t *testing.T) {
	world := newFakeWorld()
	world.running = &supervisor.RunningHelper{Protocol: supervisor.ProtocolVersion, PID: 11, ActivityKnown: true,
		Runtime: &supervisor.RuntimeMetadata{SHA256: "self"}}
	service := startService(t, world.config(t))
	ready(t, service)
	world.mu.Lock()
	world.inspectErr = fmt.Errorf("%w: hello timed out", supervisor.ErrUnavailable)
	world.mu.Unlock()
	seen := world.inspectCount()
	for world.inspectCount() < seen+3 {
		time.Sleep(time.Millisecond)
	}
	if !errors.Is(settled(t, service), ErrNotOutdated) {
		t.Fatal("the service left the ready helper")
	}
	if state := service.Status().State; state != StateReady {
		t.Fatalf("a helper that missed inspections was declared gone: %s", state)
	}
}

func TestStartNeverSpawnsWhenTheHelperVanishesBeforeTheAttach(t *testing.T) {
	world := newFakeWorld()
	world.running = &supervisor.RunningHelper{Protocol: supervisor.ProtocolVersion, PID: 11, ActivityKnown: true,
		Runtime: &supervisor.RuntimeMetadata{SHA256: "self"}}
	world.vanishOnAttach = true
	service := startService(t, world.config(t))
	waitState(t, service, StateIdle)
	if !errors.Is(settled(t, service), ErrNotOutdated) || world.connectCount() != 0 {
		t.Fatalf("connects = %d; starting must never spawn a helper", world.connectCount())
	}
}

