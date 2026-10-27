// Package localhelper keeps the supervisor helper of this machine available
// for local terminals. The helper is this application's own executable started
// with the supervisor-helper command, detached so that quitting the app never
// ends a local terminal. A helper is started only when a durable local
// terminal needs one; one that is already running is used as it is, even when
// another build started it. The package never stops a helper that runs
// sessions without the user's consent, and it stops respawning a helper that
// keeps exiting right after it starts.
package localhelper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/durable"
	"github.com/Hello-CTF/NexTerm/internal/supervisor"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// State is the lifecycle state of the local helper.
type State string

const (
	// StateDisabled means this build or mode has no local helper.
	StateDisabled State = "disabled"
	// StateIdle means no helper runs; one starts with the first durable local
	// terminal. No local terminal of an earlier run can be alive.
	StateIdle     State = "idle"
	StateStarting State = "starting"
	// StateReady means a helper of this protocol serves local terminals. It
	// may come from another build (UpdateAvailable).
	StateReady State = "ready"
	// StateOutdated means a helper of another protocol runs sessions, or
	// cannot tell how many: new local terminals stay regular until the user
	// agrees to restart it.
	StateOutdated State = "outdated"
	// StateFailed means the helper cannot start, or kept exiting right after
	// it started and respawning stopped.
	StateFailed State = "failed"
)

// Status is the user-visible state of the local helper.
type Status struct {
	State  State  `json:"state"`
	Reason string `json:"reason,omitempty"`
	// UpdateAvailable marks a ready helper from another build. It keeps
	// serving; a restart onto this build needs consent.
	UpdateAvailable bool `json:"updateAvailable,omitempty"`
	// ActiveSessions counts the PTY and exec sessions a restart would end;
	// ActivityKnown is false when the helper did not report them.
	ActiveSessions int  `json:"activeSessions,omitempty"`
	ActivityKnown  bool `json:"activityKnown,omitempty"`
	// HelperProtocol and HelperVersion describe a helper of another build.
	HelperProtocol int    `json:"helperProtocol,omitempty"`
	HelperVersion  string `json:"helperVersion,omitempty"`
	// ConsentToken names the exact helper and session count a restart was
	// approved for.
	ConsentToken string `json:"consentToken,omitempty"`
	// Warning is set when the helper runs but may not outlive the app.
	Warning string `json:"warning,omitempty"`
}

// Restart outcomes reported to callers of Restart.
var (
	ErrNotOutdated   = errors.New("the local helper does not need a restart")
	ErrStaleConsent  = errors.New("the local helper changed since the restart was approved")
	ErrNotSupported  = errors.New("this build has no local helper")
	ErrServiceClosed = errors.New("the local helper service is closed")
	// ErrNotRunning is returned for existing terminals when no helper runs,
	// so none of them can be alive.
	ErrNotRunning = fmt.Errorf("%w: no local helper is running", durable.ErrUnavailable)
)

// UnavailableError explains why local terminals cannot be durable right now.
type UnavailableError struct {
	Status Status
}

func (e *UnavailableError) Error() string {
	if e.Status.Reason != "" {
		return e.Status.Reason
	}
	return "本机持久组件不可用"
}

func (e *UnavailableError) Unwrap() error { return durable.ErrUnavailable }

// Starting reports a helper that is still starting rather than unavailable.
func (e *UnavailableError) Starting() bool { return e.Status.State == StateStarting }

// Config configures the local helper service.
type Config struct {
	// StateDir is the helper state directory; it names the helper endpoint.
	StateDir string
	// Executable is the binary started as the helper; empty means this one.
	Executable string
	Limits     supervisor.Limits
	Logger     *slog.Logger
	// OnChange is called after every status change, outside any lock.
	OnChange func(Status)

	// QuickExit is how soon after becoming ready an exit counts as a crash.
	QuickExit time.Duration
	// BreakerThreshold is how many quick exits in a row stop respawning.
	BreakerThreshold int
	// Backoff is the wait before each respawn; the last entry repeats.
	Backoff []time.Duration
	// PollInterval is how often a helper this process did not spawn, or one
	// of another build, is checked.
	PollInterval time.Duration

	connect    func(context.Context, supervisor.HelperConfig) (helperHandle, error)
	inspect    func(context.Context, string) (supervisor.RunningHelper, error)
	stop       func(context.Context, string, supervisor.RunningHelper) error
	selfDigest func() (string, error)
	now        func() time.Time
}

// helperHandle is the part of *supervisor.Helper the lifecycle uses.
type helperHandle interface {
	Client() *supervisor.Client
	Exited() <-chan struct{}
	Spawned() bool
	PID() int
	JobBound() bool
}

func connectHelper(ctx context.Context, config supervisor.HelperConfig) (helperHandle, error) {
	helper, err := supervisor.ConnectHelper(ctx, config)
	if err != nil {
		return nil, err
	}
	return helper, nil
}

// Service runs the local helper lifecycle in one goroutine.
type Service struct {
	config Config

	mu       sync.Mutex
	status   Status
	provider *supervisor.RemoteProvider
	changed  chan struct{}
	// wanted is set when a durable local terminal asked for a helper.
	wanted bool
	// checked is closed once the first inspection decided the state.
	checked chan struct{}

	ensure   chan struct{}
	requests chan request
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	start    sync.Once
	started  bool
}

type requestKind int

const (
	requestRestart requestKind = iota
	requestRetry
)

type request struct {
	kind  requestKind
	token string
	reply chan error
}

// New returns a stopped service. Start, or the first provider request,
// launches the lifecycle.
func New(config Config) *Service {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.QuickExit <= 0 {
		config.QuickExit = 10 * time.Second
	}
	if config.BreakerThreshold <= 0 {
		config.BreakerThreshold = 3
	}
	if len(config.Backoff) == 0 {
		config.Backoff = []time.Duration{500 * time.Millisecond, 2 * time.Second, 5 * time.Second}
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 5 * time.Second
	}
	if config.connect == nil {
		config.connect = connectHelper
	}
	if config.inspect == nil {
		config.inspect = supervisor.InspectHelper
	}
	if config.stop == nil {
		config.stop = supervisor.StopHelper
	}
	if config.selfDigest == nil {
		config.selfDigest = selfDigest
	}
	if config.now == nil {
		config.now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	initial := Status{State: StateIdle}
	if !Supported || config.StateDir == "" {
		initial = Status{State: StateDisabled, Reason: "此平台没有本机持久组件"}
	}
	return &Service{
		config:   config,
		status:   initial,
		changed:  make(chan struct{}),
		checked:  make(chan struct{}),
		ensure:   make(chan struct{}, 1),
		requests: make(chan request),
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
}

// selfDigest hashes this executable once; the app binary does not change while it runs.
var selfDigest = sync.OnceValues(func() (string, error) {
	metadata, err := supervisor.CurrentRuntimeMetadata()
	if err != nil {
		return "", err
	}
	return metadata.SHA256, nil
})

// Start launches the lifecycle goroutine and returns at once. It attaches to
// a helper that already runs, so terminals of an earlier run stay listable,
// but spawns none until a durable local terminal needs one.
func (s *Service) Start() {
	s.mu.Lock()
	disabled := s.status.State == StateDisabled
	s.mu.Unlock()
	if disabled {
		return
	}
	s.start.Do(func() {
		s.mu.Lock()
		s.started = true
		s.mu.Unlock()
		go s.run()
	})
}

// Close stops the lifecycle. It never stops the helper or its sessions.
func (s *Service) Close() {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	s.cancel()
	if started {
		<-s.done
	}
}

// Status returns the current status.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Provider returns the durable provider of a ready helper, or an
// *UnavailableError that explains why there is none.
func (s *Service) Provider() (base.DurableProvider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State == StateReady && s.provider != nil {
		return s.provider, nil
	}
	return nil, &UnavailableError{Status: s.status}
}

func (s *Service) existingProvider() (base.DurableProvider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State == StateReady && s.provider != nil {
		return s.provider, nil
	}
	if s.status.State == StateIdle {
		return nil, ErrNotRunning
	}
	return nil, &UnavailableError{Status: s.status}
}

// WaitExisting returns the provider for listing, attaching to and ending
// terminals that already exist. It never starts a helper: with none running
// it returns ErrNotRunning, as no earlier terminal can be alive.
func (s *Service) WaitExisting(ctx context.Context, limit time.Duration) (base.DurableProvider, error) {
	s.Start()
	return s.wait(ctx, limit, false, s.existingProvider)
}

// WaitReady returns the provider for a new durable local terminal. It starts
// a helper when none runs and waits up to limit while it is starting; other
// states return at once with an *UnavailableError.
func (s *Service) WaitReady(ctx context.Context, limit time.Duration) (base.DurableProvider, error) {
	s.Start()
	s.mu.Lock()
	if s.status.State == StateDisabled {
		s.mu.Unlock()
		return nil, &UnavailableError{Status: s.status}
	}
	s.wanted = true
	s.mu.Unlock()
	select {
	case s.ensure <- struct{}{}:
	default:
	}
	return s.wait(ctx, limit, true, s.Provider)
}

func (s *Service) wait(ctx context.Context, limit time.Duration, wanted bool, provider func() (base.DurableProvider, error)) (base.DurableProvider, error) {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for {
		s.mu.Lock()
		status, changed := s.status, s.changed
		s.mu.Unlock()
		state := status.State
		// An idle helper of another protocol is replaced for a new terminal.
		replaceable := state == StateOutdated && status.ActivityKnown && status.ActiveSessions == 0
		pending := state == StateStarting || wanted && (state == StateIdle || replaceable)
		select {
		case <-s.checked:
		default:
			if state != StateDisabled {
				pending = true
			}
		}
		if !pending {
			return provider()
		}
		select {
		case <-changed:
		case <-s.checked:
		case <-timer.C:
			select {
			case <-s.checked:
			default:
				// Not inspected yet: never answer "no helper" for a helper
				// that may still turn out to run.
				return nil, &UnavailableError{Status: Status{State: StateStarting, Reason: "正在检查本机持久组件"}}
			}
			if wanted && state == StateIdle {
				return nil, &UnavailableError{Status: Status{State: StateStarting, Reason: "本机持久组件正在启动"}}
			}
			return provider()
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}

// Restart replaces a helper of another build, ending its sessions. token
// must be the ConsentToken the user saw; a helper that changed since, or runs
// more sessions now, is refused.
func (s *Service) Restart(ctx context.Context, token string) error {
	return s.send(ctx, request{kind: requestRestart, token: token})
}

// Retry clears a failure, including an open crash-loop breaker, and starts
// the helper again.
func (s *Service) Retry(ctx context.Context) error {
	return s.send(ctx, request{kind: requestRetry})
}

func (s *Service) send(ctx context.Context, req request) error {
	s.Start()
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return ErrNotSupported
	}
	req.reply = make(chan error, 1)
	select {
	case s.requests <- req:
	case <-s.done:
		return ErrServiceClosed
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	select {
	case err := <-req.reply:
		return err
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (s *Service) setStatus(status Status, provider *supervisor.RemoteProvider) {
	s.mu.Lock()
	if status == s.status && provider == s.provider {
		s.mu.Unlock()
		return
	}
	s.status = status
	s.provider = provider
	close(s.changed)
	s.changed = make(chan struct{})
	onChange := s.config.OnChange
	s.mu.Unlock()
	if onChange != nil {
		onChange(status)
	}
}

func (s *Service) isWanted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wanted
}

func (s *Service) setWanted(wanted bool) {
	s.mu.Lock()
	s.wanted = wanted
	s.mu.Unlock()
}

func (s *Service) markChecked() {
	select {
	case <-s.checked:
	default:
		close(s.checked)
	}
}

func (s *Service) helperExecutable() (string, error) {
	executable := s.config.Executable
	if executable == "" {
		resolved, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("resolve the helper executable: %w", err)
		}
		executable = resolved
	}
	if err := supervisor.PrepareStateDir(s.config.StateDir); err != nil {
		return "", err
	}
	staged, err := stageExecutable(s.config.StateDir, executable)
	if err != nil {
		return "", fmt.Errorf("stage the helper executable: %w", err)
	}
	return staged, nil
}

func (s *Service) logPath() string {
	return filepath.Join(s.config.StateDir, "helper.log")
}

// watchOutcome is how a watched helper stopped serving.
type watchOutcome int

const (
	watchClosed watchOutcome = iota
	// watchExited: the helper went away on its own.
	watchExited
	// watchRestarted: the user approved a restart and this service stopped it.
	watchRestarted
)

// run is the single actor that inspects, spawns, watches and replaces the
// helper. quickExits counts consecutive helpers this service spawned that
// exited within QuickExit of becoming ready, or failed to start; exits it
// caused itself and helpers replaced by a peer do not count.
func (s *Service) run() {
	defer close(s.done)
	defer s.markChecked()
	quickExits := 0
	for s.ctx.Err() == nil {
		running, inspectErr := s.config.inspect(s.ctx, s.config.StateDir)
		if s.ctx.Err() != nil {
			return
		}
		if inspectErr == nil && !running.Current() {
			if running.ActivityKnown && running.Activity.Total() == 0 && s.isWanted() {
				// Nothing runs in it, so replacing it ends no session.
				s.config.Logger.Info("replacing an idle local helper of another protocol", "protocol", running.Protocol, "pid", running.PID)
				if err := s.config.stop(s.ctx, s.config.StateDir, running); err != nil && s.ctx.Err() == nil {
					s.setStatus(Status{State: StateFailed, Reason: "无法替换旧版本地持久组件：" + err.Error()}, nil)
					s.markChecked()
					if !s.awaitRetry(nil) {
						return
					}
				}
				continue
			}
			status := s.legacyStatus(running)
			s.setStatus(status, nil)
			s.markChecked()
			if !s.awaitOutdated(running, status.ConsentToken) {
				return
			}
			continue
		}
		if inspectErr != nil && !errors.Is(inspectErr, supervisor.ErrHelperNotRunning) {
			// Something serves the endpoint but cannot be inspected: a hello
			// that times out, a pid that does not match, a protocol error. Its
			// terminals may be alive, so this is reported, never taken for
			// "no helper", and nothing is spawned beside it.
			s.config.Logger.Warn("local helper cannot be inspected", "error", inspectErr)
			s.setStatus(Status{State: StateFailed, Reason: "本机持久组件无响应或无法确认，暂时无法列出或新建本机持久终端：" + inspectErr.Error()}, nil)
			s.markChecked()
			if !s.pauseFor(s.config.PollInterval) {
				return
			}
			continue
		}
		if inspectErr != nil && !s.isWanted() {
			// Nothing runs and nothing asked for a helper yet.
			s.setStatus(Status{State: StateIdle}, nil)
			s.markChecked()
			if !s.awaitEnsure() {
				return
			}
			continue
		}
		// This start answers every durable request made so far; a stale one
		// must not restart a helper after it exits.
		select {
		case <-s.ensure:
		default:
		}
		s.setStatus(Status{State: StateStarting, Reason: "本机持久组件正在启动"}, nil)
		executable, err := s.helperExecutable()
		var helper helperHandle
		if err == nil {
			// Without a durable terminal asking for one, this only attaches to
			// the helper just inspected; it never spawns one.
			helper, err = s.config.connect(s.ctx, supervisor.HelperConfig{
				StateDir: s.config.StateDir, Executable: executable, Limits: s.config.Limits, NoSpawn: !s.isWanted(),
			})
		}
		if s.ctx.Err() != nil {
			return
		}
		if errors.Is(err, supervisor.ErrHelperNotRunning) {
			// The attach found the helper gone; inspect again, which settles
			// on idle without spawning.
			continue
		}
		if err != nil {
			s.config.Logger.Warn("local helper did not start", "error", err)
			if errors.Is(err, supervisor.ErrLegacyHelperRunning) || errors.Is(err, supervisor.ErrStateMismatch) || errors.Is(err, supervisor.ErrProtocol) {
				// Another helper answers: inspect it again after a pause.
				if !s.pause(1) {
					return
				}
				continue
			}
			quickExits++
			if quickExits >= s.config.BreakerThreshold {
				s.setStatus(Status{State: StateFailed, Reason: fmt.Sprintf("本机持久组件连续 %d 次未能启动，已停止自动重试（日志：%s）：%v", quickExits, s.logPath(), err)}, nil)
				s.markChecked()
				if !s.awaitRetry(nil) {
					return
				}
				quickExits = 0
				continue
			}
			if !s.pause(quickExits) {
				return
			}
			continue
		}
		readyAt := s.config.now()
		outcome := s.watch(helper)
		switch outcome {
		case watchClosed:
			return
		case watchRestarted:
			// The user restarted it onto this build: start the new one now.
			s.setWanted(true)
			continue
		}
		lived := s.config.now().Sub(readyAt)
		if helper.Spawned() && lived < s.config.QuickExit && !s.replacedBy(helper) {
			quickExits++
		} else {
			quickExits = 0
		}
		s.config.Logger.Warn("local helper exited", "spawned", helper.Spawned(), "uptime", lived, "quick_exits", quickExits)
		if quickExits >= s.config.BreakerThreshold {
			s.setStatus(Status{State: StateFailed, Reason: fmt.Sprintf("本机持久组件连续 %d 次启动后立即退出，已停止自动重启（日志：%s）", quickExits, s.logPath())}, nil)
			if !s.awaitRetry(nil) {
				return
			}
			quickExits = 0
			continue
		}
		// Its terminals ended with it; the next durable terminal starts a
		// new helper.
		s.setWanted(false)
		if quickExits > 0 && !s.pause(quickExits) {
			return
		}
	}
}

// replacedBy reports whether another process now serves the endpoint the
// exited helper served, which is not a crash of that helper.
func (s *Service) replacedBy(helper helperHandle) bool {
	running, err := s.config.inspect(s.ctx, s.config.StateDir)
	return err == nil && running.PID != helper.PID()
}

// legacyStatus describes a helper of another protocol that runs sessions or
// cannot tell how many.
func (s *Service) legacyStatus(running supervisor.RunningHelper) Status {
	status := Status{
		State:          StateOutdated,
		ActiveSessions: running.Activity.Total(),
		ActivityKnown:  running.ActivityKnown,
		HelperProtocol: running.Protocol,
		ConsentToken:   consentToken(running),
	}
	if running.Runtime != nil {
		status.HelperVersion = running.Runtime.Version
	}
	sessions := "数量未知的会话"
	if running.ActivityKnown {
		sessions = fmt.Sprintf(" %d 个会话", status.ActiveSessions)
	} else if running.PTYListed {
		sessions = fmt.Sprintf("至少 %d 个终端会话（后台命令数未知）", status.ActiveSessions)
	}
	status.Reason = fmt.Sprintf("本机持久组件为旧版本（协议 v%d），其中有%s在运行；新开的本机终端暂用普通模式。重启组件会结束这些会话", running.Protocol, sessions)
	return status
}

// readyStatus describes a ready helper of this protocol and whether it comes
// from another build, which can be restarted onto this one with consent.
func (s *Service) readyStatus(helper helperHandle, running supervisor.RunningHelper, inspected bool) Status {
	status := Status{State: StateReady}
	if helper.JobBound() {
		status.Warning = "本机持久组件无法脱离当前进程所在的 Windows 作业对象，退出 NexTerm 时可能被一并结束"
	}
	if !inspected || running.Runtime == nil {
		return status
	}
	digest, err := s.config.selfDigest()
	if err != nil || digest == running.Runtime.SHA256 {
		return status
	}
	status.UpdateAvailable = true
	status.ActiveSessions = running.Activity.Total()
	status.ActivityKnown = running.ActivityKnown
	status.HelperProtocol = running.Protocol
	status.HelperVersion = running.Runtime.Version
	status.ConsentToken = consentToken(running)
	version := status.HelperVersion
	if version == "" {
		version = "未知"
	}
	status.Reason = fmt.Sprintf("本机持久组件来自其他版本（%s），可更新；其中有 %d 个会话在运行，重启组件会结束这些会话", version, status.ActiveSessions)
	return status
}

func consentToken(running supervisor.RunningHelper) string {
	return supervisor.HelperConsentToken(running)
}

// restartWith stops the helper a consent token names. It reports whether the
// helper was stopped.
// The token must name the helper as it is now; only a session count that
// dropped since consent is accepted, so a hand-made token cannot skip the
// count check.
func (s *Service) restartWith(token string) (bool, error) {
	expected, ok := supervisor.ParseHelperConsent(token)
	if !ok {
		return false, ErrStaleConsent
	}
	current, err := s.config.inspect(s.ctx, s.config.StateDir)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrStaleConsent, err)
	}
	if !supervisor.ConsentCovers(expected, current) {
		return false, ErrStaleConsent
	}
	s.config.Logger.Info("stopping a local helper with consent", "protocol", expected.Protocol, "pid", expected.PID, "sessions", expected.Activity.Total())
	err = s.config.stop(s.ctx, s.config.StateDir, expected)
	if errors.Is(err, supervisor.ErrHelperChanged) {
		return false, fmt.Errorf("%w: %v", ErrStaleConsent, err)
	}
	return err == nil, err
}

// awaitOutdated waits for consent while a helper of another protocol runs.
// It re-checks the helper on every poll: one whose sessions all ended, or
// that exited by itself, needs no consent anymore. It returns false when the
// service closes.
func (s *Service) awaitOutdated(running supervisor.RunningHelper, token string) bool {
	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return false
		case <-s.ensure:
			s.setWanted(true)
			if running.ActivityKnown && running.Activity.Total() == 0 {
				// An idle helper can now be replaced for the new terminal.
				return true
			}
		case <-ticker.C:
			current, err := s.config.inspect(s.ctx, s.config.StateDir)
			if err != nil || consentToken(current) != token {
				return s.ctx.Err() == nil
			}
		case req := <-s.requests:
			switch req.kind {
			case requestRetry:
				req.reply <- nil
				return true
			case requestRestart:
				stopped, err := s.restartWith(req.token)
				req.reply <- err
				if stopped {
					s.setWanted(true)
				}
				if stopped || errors.Is(err, ErrStaleConsent) {
					return true
				}
			}
		}
	}
}

// awaitEnsure waits in the idle state until a durable terminal asks for a
// helper. A retry starts one as well.
func (s *Service) awaitEnsure() bool {
	for {
		select {
		case <-s.ctx.Done():
			return false
		case <-s.ensure:
			s.setWanted(true)
			return true
		case req := <-s.requests:
			if req.kind == requestRetry {
				s.setWanted(true)
				req.reply <- nil
				return true
			}
			req.reply <- ErrNotOutdated
		}
	}
}

// awaitRetry blocks a failed helper until Retry, or until ticks fires. A
// restart request is answered with ErrNotOutdated.
func (s *Service) awaitRetry(ticks <-chan time.Time) bool {
	for {
		select {
		case <-s.ctx.Done():
			return false
		case <-ticks:
			return true
		case <-s.ensure:
			// A failed helper is not respawned for a terminal; Retry is needed.
		case req := <-s.requests:
			if req.kind == requestRetry {
				s.setWanted(true)
				req.reply <- nil
				return true
			}
			req.reply <- ErrNotOutdated
		}
	}
}

// pauseFor waits d, answering requests; a retry ends it early.
func (s *Service) pauseFor(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	return s.awaitRetry(timer.C)
}

// pause waits the backoff for the given attempt, answering requests.
func (s *Service) pause(attempt int) bool {
	index := attempt - 1
	if index < 0 {
		index = 0
	}
	if index >= len(s.config.Backoff) {
		index = len(s.config.Backoff) - 1
	}
	timer := time.NewTimer(s.config.Backoff[index])
	defer timer.Stop()
	return s.awaitRetry(timer.C)
}

// watch serves the ready helper until it goes away, the user restarts it,
// or the service closes. A helper this process spawned is watched through
// its process; one that was already running is inspected on every poll,
// which also keeps the session count of a helper from another build current.
func (s *Service) watch(helper helperHandle) watchOutcome {
	provider := supervisor.NewRemoteProvider(helper.Client())
	running, err := s.config.inspect(s.ctx, s.config.StateDir)
	inspected := err == nil && running.Current()
	status := s.readyStatus(helper, running, inspected && !helper.Spawned())
	s.setStatus(status, provider)
	s.markChecked()
	exited := helper.Exited()
	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return watchClosed
		case <-exited:
			return watchExited
		case <-s.ensure:
		case <-ticker.C:
			if exited != nil {
				continue
			}
			current, err := s.config.inspect(s.ctx, s.config.StateDir)
			if s.ctx.Err() != nil {
				return watchClosed
			}
			if err != nil && !errors.Is(err, supervisor.ErrHelperNotRunning) {
				// A helper that does not answer one inspection is retried,
				// not declared gone.
				s.config.Logger.Warn("local helper did not answer an inspection", "error", err)
				continue
			}
			// InspectHelper probes this protocol and older ones. A newer
			// build's helper serves another endpoint, so a peer replacing
			// this helper with a newer protocol reads as an exit here, and
			// the run loop counts it only if this service spawned the helper.
			if err != nil || !current.Current() || current.PID != running.PID && inspected {
				return watchExited
			}
			running, inspected = current, true
			status = s.readyStatus(helper, running, true)
			s.setStatus(status, provider)
		case req := <-s.requests:
			switch req.kind {
			case requestRetry:
				req.reply <- nil
			case requestRestart:
				if !status.UpdateAvailable {
					req.reply <- ErrNotOutdated
					continue
				}
				stopped, err := s.restartWith(req.token)
				req.reply <- err
				if stopped {
					return watchRestarted
				}
				if current, err := s.config.inspect(s.ctx, s.config.StateDir); err == nil {
					running = current
					status = s.readyStatus(helper, running, true)
					s.setStatus(status, provider)
				}
			}
		}
	}
}
