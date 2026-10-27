package exposure

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/fleet/agent"
)

type fakeHost struct {
	mu         sync.Mutex
	enabled    bool
	blocked    string
	account    Account
	accountErr error
	enrollment *Enrollment
	enrolls    []agent.EnrollRequest
	revoked    []string
	echoKind   bool
	enrollErr  error
	nextID     int
}

func newFakeHost() *fakeHost {
	return &fakeHost{account: Account{URL: "https://nexterm.example.com", Username: "alice"}, echoKind: true}
}

func (h *fakeHost) Enabled(context.Context) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.enabled, nil
}

func (h *fakeHost) SetEnabled(_ context.Context, enabled bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enabled = enabled
	return nil
}

func (h *fakeHost) Blocked(context.Context) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.blocked
}

func (h *fakeHost) Account(context.Context) (Account, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.account, h.accountErr
}

func (h *fakeHost) LoadEnrollment(context.Context) (*Enrollment, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.accountErr != nil && errors.Is(h.accountErr, ErrLocked) {
		return nil, ErrLocked
	}
	if h.enrollment == nil {
		return nil, nil
	}
	copied := *h.enrollment
	return &copied, nil
}

func (h *fakeHost) SaveEnrollment(_ context.Context, enrollment *Enrollment) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	copied := *enrollment
	h.enrollment = &copied
	return nil
}

func (h *fakeHost) RemoveEnrollment(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enrollment = nil
	return nil
}

func (h *fakeHost) Enroll(_ context.Context, account Account, request agent.EnrollRequest) (*agent.EnrollResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enrolls = append(h.enrolls, request)
	if h.enrollErr != nil {
		return nil, h.enrollErr
	}
	h.nextID++
	response := &agent.EnrollResponse{
		DeviceID: "device-" + string(rune('0'+h.nextID)), Secret: "secret", TerminalEnabled: true, MetricsIntervalMS: 60000,
	}
	if h.echoKind {
		response.Kind = request.Kind
	}
	return response, nil
}

func (h *fakeHost) Revoke(_ context.Context, deviceID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.revoked = append(h.revoked, deviceID)
	return nil
}

func (h *fakeHost) set(update func(h *fakeHost)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	update(h)
}

// hostSnapshot is what the controller left in the host.
type hostSnapshot struct {
	enabled    bool
	enrollment *Enrollment
	enrolls    []agent.EnrollRequest
	revoked    []string
}

func (h *fakeHost) snapshot() hostSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	copied := hostSnapshot{
		enabled: h.enabled, enrolls: append([]agent.EnrollRequest(nil), h.enrolls...),
		revoked: append([]string(nil), h.revoked...),
	}
	if h.enrollment != nil {
		enrollment := *h.enrollment
		copied.enrollment = &enrollment
	}
	return copied
}

// fakeRunner stands in for the agent runtime: it runs until its context ends
// or the test makes it exit.
type fakeRunner struct {
	config    agent.Config
	store     agent.ConfigStore
	onChannel func(bool)
	exit      chan error
	stopped   chan struct{}
}

func (r *fakeRunner) Run(ctx context.Context) error {
	defer close(r.stopped)
	select {
	case <-ctx.Done():
		return nil
	case err := <-r.exit:
		return err
	}
}

type runnerLog struct {
	mu      sync.Mutex
	runners []*fakeRunner
}

func (l *runnerLog) factory(config *agent.Config, store agent.ConfigStore, onChannel func(bool)) (Runner, error) {
	runner := &fakeRunner{config: *config, store: store, onChannel: onChannel, exit: make(chan error, 1), stopped: make(chan struct{})}
	l.mu.Lock()
	l.runners = append(l.runners, runner)
	l.mu.Unlock()
	return runner, nil
}

func (l *runnerLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.runners)
}

func (l *runnerLog) last(t *testing.T) *fakeRunner {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.runners) == 0 {
		t.Fatal("no agent was started")
	}
	return l.runners[len(l.runners)-1]
}

func newTestController(t *testing.T, host *fakeHost, runners *runnerLog) *Controller {
	t.Helper()
	controller := New(Config{
		Host: host, NewRunner: runners.factory, DeviceName: "desk", AppVersion: "1.3.0",
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		RecheckInterval: time.Hour,
	})
	controller.Start()
	t.Cleanup(controller.Close)
	return controller
}

// reconcileNow runs one reconcile in the controller goroutine and waits for it.
func reconcileNow(t *testing.T, controller *Controller) Status {
	t.Helper()
	if err := controller.do(context.Background(), func() error { controller.reconcile(); return nil }); err != nil {
		t.Fatal(err)
	}
	return controller.Status()
}

func waitStatus(t *testing.T, controller *Controller, what string, condition func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if status := controller.Status(); condition(status) {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; status %+v", what, controller.Status())
	return Status{}
}

func stateIs(state State) func(Status) bool {
	return func(status Status) bool { return status.State == state }
}

func enable(t *testing.T, controller *Controller) Status {
	t.Helper()
	status, err := controller.SetEnabled(context.Background(), true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	return status
}

func TestExposureIsOffByDefault(t *testing.T) {
	host := newFakeHost()
	runners := &runnerLog{}
	controller := newTestController(t, host, runners)
	if status := reconcileNow(t, controller); status.State != StateOff || status.Enabled {
		t.Fatalf("status = %+v, want off", status)
	}
	if runners.count() != 0 || len(host.snapshot().enrolls) != 0 {
		t.Fatal("exposure enrolled or started an agent without the user turning it on")
	}
}

func TestEnableEnrollsAsLocalHelperAndRunsAgent(t *testing.T) {
	host := newFakeHost()
	runners := &runnerLog{}
	controller := newTestController(t, host, runners)

	status := enable(t, controller)
	if status.State != StateConnecting || status.DeviceID == "" || !status.Enabled {
		t.Fatalf("status after enable = %+v, want a connecting agent", status)
	}
	snapshot := host.snapshot()
	if len(snapshot.enrolls) != 1 || snapshot.enrolls[0].Kind != agent.DeviceKindLocalHelper || snapshot.enrolls[0].Name != "desk" {
		t.Fatalf("enroll requests = %+v, want one local helper enroll", snapshot.enrolls)
	}
	if !snapshot.enabled || snapshot.enrollment == nil || !snapshot.enrollment.Account.Same(host.account) {
		t.Fatalf("host after enable = %+v, want the setting on and an enrollment bound to the account", snapshot)
	}
	runner := runners.last(t)
	if runner.config.DeviceID != status.DeviceID || len(runner.config.BaseURLs) != 1 || runner.config.BaseURLs[0].URL != host.account.URL {
		t.Fatalf("agent config = %+v, want the enrolled device and the account URL as base URL", runner.config)
	}

	runner.onChannel(true)
	waitStatus(t, controller, "online", stateIs(StateOnline))
	runner.onChannel(false)
	waitStatus(t, controller, "connecting after the channel dropped", stateIs(StateConnecting))

	// The agent's config saves reach the host.
	config, err := runner.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	config.CurrentURL = host.account.URL
	if err := runner.store.Save(config); err != nil {
		t.Fatal(err)
	}
	if saved := host.snapshot().enrollment; saved == nil || saved.Config.CurrentURL != host.account.URL {
		t.Fatalf("saved enrollment = %+v, want the agent's current URL", saved)
	}
}

func TestEnableNeedsAccountAndHelper(t *testing.T) {
	host := newFakeHost()
	host.accountErr = ErrNoAccount
	runners := &runnerLog{}
	controller := newTestController(t, host, runners)
	if _, err := controller.SetEnabled(context.Background(), true); err == nil {
		t.Fatal("exposure turned on without a linked account")
	}
	host.set(func(h *fakeHost) { h.accountErr, h.blocked = nil, "本机终端为普通模式" })
	if _, err := controller.SetEnabled(context.Background(), true); err == nil {
		t.Fatal("exposure turned on while the local helper cannot be exposed")
	}
	if snapshot := host.snapshot(); snapshot.enabled || len(snapshot.enrolls) != 0 || runners.count() != 0 {
		t.Fatalf("refused enable changed state: %+v", snapshot)
	}
}

func TestServerWithoutLocalHelperKindIsRefused(t *testing.T) {
	host := newFakeHost()
	host.echoKind = false
	runners := &runnerLog{}
	controller := newTestController(t, host, runners)
	if _, err := controller.SetEnabled(context.Background(), true); err == nil {
		t.Fatal("enable succeeded against a server that enrolled an ordinary device")
	}
	snapshot := host.snapshot()
	if len(snapshot.revoked) != 1 || snapshot.enrollment != nil || runners.count() != 0 {
		t.Fatalf("host = %+v runners=%d, want the device revoked, nothing saved and no agent", snapshot, runners.count())
	}
	if status := controller.Status(); status.State != StateFailed {
		t.Fatalf("status = %+v, want failed", status)
	}
	// The failure holds until the user turns exposure on again.
	reconcileNow(t, controller)
	reconcileNow(t, controller)
	if snapshot := host.snapshot(); len(snapshot.enrolls) != 1 {
		t.Fatalf("enroll retried without the user: %d enrolls", len(snapshot.enrolls))
	}
}

func TestRevocationTurnsExposureOff(t *testing.T) {
	host := newFakeHost()
	runners := &runnerLog{}
	controller := newTestController(t, host, runners)
	first := enable(t, controller)
	runners.last(t).exit <- agent.ErrRevoked

	status := waitStatus(t, controller, "revoked", stateIs(StateRevoked))
	if status.Enabled {
		t.Fatalf("status = %+v, want exposure off", status)
	}
	if snapshot := host.snapshot(); snapshot.enabled || snapshot.enrollment != nil {
		t.Fatalf("host = %+v, want the setting off and the revoked credentials removed", snapshot)
	}
	if status := reconcileNow(t, controller); status.State != StateRevoked {
		t.Fatalf("status = %+v, want still revoked", status)
	}
	if runners.count() != 1 {
		t.Fatalf("agent restarted after a revocation: %d starts", runners.count())
	}

	// Turning it on again is a new enrollment.
	second := enable(t, controller)
	if second.DeviceID == "" || second.DeviceID == first.DeviceID || runners.count() != 2 {
		t.Fatalf("re-enable = %+v with %d starts, want a new device", second, runners.count())
	}
}

func TestAccountChangeStopsExposureForGood(t *testing.T) {
	host := newFakeHost()
	runners := &runnerLog{}
	controller := newTestController(t, host, runners)
	enable(t, controller)
	runner := runners.last(t)

	host.set(func(h *fakeHost) { h.account = Account{URL: "https://nexterm.example.com", Username: "mallory"} })
	status := reconcileNow(t, controller)
	if status.State != StateOff {
		t.Fatalf("status = %+v, want off after the account changed", status)
	}
	if status.Reason == "" {
		t.Fatal("account change stopped exposure without saying why")
	}
	select {
	case <-runner.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("agent of the earlier account still runs")
	}
	snapshot := host.snapshot()
	if snapshot.enabled || snapshot.enrollment != nil || len(snapshot.enrolls) != 1 || runners.count() != 1 {
		t.Fatalf("host = %+v, want exposure off, the old enrollment dropped and nothing enrolled for the new account", snapshot)
	}
}

func TestStartupReusesEnrollmentOfSameAccount(t *testing.T) {
	host := newFakeHost()
	host.enabled = true
	host.enrollment = &Enrollment{Account: Account{URL: "https://NEXTERM.example.com/", Username: "Alice"}, Config: agent.Config{
		Version: agent.ConfigVersion, DeviceID: "kept", Secret: "s", BaseURLs: []agent.BaseURLEntry{{URL: "https://nexterm.example.com"}},
	}}
	runners := &runnerLog{}
	controller := newTestController(t, host, runners)
	status := waitStatus(t, controller, "agent started", stateIs(StateConnecting))
	if status.DeviceID != "kept" || len(host.snapshot().enrolls) != 0 {
		t.Fatalf("status = %+v, want the stored device reused without enrolling", status)
	}
}

func TestStartupWithEnrollmentOfAnotherAccountDoesNotExpose(t *testing.T) {
	host := newFakeHost()
	host.enabled = true
	host.enrollment = &Enrollment{Account: Account{URL: "https://nexterm.example.com", Username: "bob"}, Config: agent.Config{
		Version: agent.ConfigVersion, DeviceID: "bobs", Secret: "s", BaseURLs: []agent.BaseURLEntry{{URL: "https://nexterm.example.com"}},
	}}
	runners := &runnerLog{}
	controller := newTestController(t, host, runners)
	if status := reconcileNow(t, controller); status.State != StateOff || status.Reason == "" {
		t.Fatalf("status = %+v, want off with the account change named", status)
	}
	if snapshot := host.snapshot(); snapshot.enabled || snapshot.enrollment != nil || len(snapshot.enrolls) != 0 || runners.count() != 0 {
		t.Fatalf("host = %+v runners=%d, want nothing exposed to either account", snapshot, runners.count())
	}
}

func TestBlockedPausesAndResumesExposure(t *testing.T) {
	host := newFakeHost()
	runners := &runnerLog{}
	controller := newTestController(t, host, runners)
	enable(t, controller)
	runner := runners.last(t)

	host.set(func(h *fakeHost) { h.blocked = "本机终端为普通模式" })
	if status := reconcileNow(t, controller); status.State != StateUnavailable {
		t.Fatalf("status = %+v, want unavailable", status)
	}
	<-runner.stopped

	host.set(func(h *fakeHost) { h.blocked = "" })
	status := reconcileNow(t, controller)
	if status.State != StateConnecting {
		t.Fatalf("status = %+v, want running again", status)
	}
	if runners.count() != 2 || len(host.snapshot().enrolls) != 1 || runners.last(t).config.DeviceID != status.DeviceID {
		t.Fatalf("resume started %d agents with %d enrolls, want the same device again", runners.count(), len(host.snapshot().enrolls))
	}
}

func TestDisableStopsAgentAndKeepsEnrollment(t *testing.T) {
	host := newFakeHost()
	runners := &runnerLog{}
	controller := newTestController(t, host, runners)
	enable(t, controller)
	runner := runners.last(t)
	status, err := controller.SetEnabled(context.Background(), false)
	if err != nil || status.State != StateOff || status.Enabled {
		t.Fatalf("disable = %+v %v", status, err)
	}
	select {
	case <-runner.stopped:
	default:
		t.Fatal("agent still runs after exposure was turned off")
	}
	if snapshot := host.snapshot(); snapshot.enabled || snapshot.enrollment == nil {
		t.Fatalf("host = %+v, want the setting off and the enrollment kept", snapshot)
	}
}

func TestLockedVaultKeepsRunningAgent(t *testing.T) {
	host := newFakeHost()
	runners := &runnerLog{}
	controller := newTestController(t, host, runners)
	enable(t, controller)
	host.set(func(h *fakeHost) { h.accountErr = ErrLocked })
	if status := reconcileNow(t, controller); status.State != StateConnecting {
		t.Fatalf("status = %+v, want the agent kept while the vault is locked", status)
	}
	select {
	case <-runners.last(t).stopped:
		t.Fatal("a locked vault stopped the agent")
	default:
	}
}

func TestAgentFailureHoldsUntilReenabled(t *testing.T) {
	host := newFakeHost()
	runners := &runnerLog{}
	controller := newTestController(t, host, runners)
	enable(t, controller)
	runners.last(t).exit <- agent.ErrProtocolMismatch
	waitStatus(t, controller, "failed", stateIs(StateFailed))
	if status := reconcileNow(t, controller); status.State != StateFailed {
		t.Fatalf("status = %+v, want still failed", status)
	}
	if runners.count() != 1 {
		t.Fatalf("agent restarted %d times without the user", runners.count()-1)
	}
	enable(t, controller)
	if runners.count() != 2 || len(host.snapshot().enrolls) != 1 {
		t.Fatal("re-enable did not restart the agent with the same enrollment")
	}
}
