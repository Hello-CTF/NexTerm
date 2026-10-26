// Package localhelper keeps the supervisor helper of this machine running for
// local terminals. The helper is this application's own executable started
// with the supervisor-helper command, detached so that quitting the app never
// ends a local terminal. The package never stops a helper that runs sessions
// without the user's consent, and it stops respawning a helper that keeps
// exiting right after it starts.
package localhelper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	StateStarting State = "starting"
	StateReady    State = "ready"
	// StateOutdated means a helper from another build serves the state
	// directory and runs sessions: replacing it needs the user's consent.
	StateOutdated State = "outdated"
	// StateFailed means the helper cannot start, or kept exiting right after
	// it started and respawning stopped.
	StateFailed State = "failed"
)

// Status is the user-visible state of the local helper.
type Status struct {
	State  State  `json:"state"`
	Reason string `json:"reason,omitempty"`
	// ActiveSessions counts the PTY and exec sessions of an outdated helper
	// that a restart would end.
	ActiveSessions int `json:"activeSessions,omitempty"`
	// HelperProtocol and HelperVersion describe an outdated helper.
	HelperProtocol int    `json:"helperProtocol,omitempty"`
	HelperVersion  string `json:"helperVersion,omitempty"`
	// ConsentToken names the exact outdated helper a restart was approved for.
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
	// PollInterval is how often a helper this process did not spawn, or an
	// outdated one, is checked.
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
	// existing reaches the sessions of an outdated helper that speaks this
	// build's protocol, so they can still be listed, attached and ended.
	existing *supervisor.RemoteProvider
	readyCh  chan struct{}
	changed  chan struct{}

	requests chan request
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
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

// New returns a stopped service. Start launches the lifecycle.
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
	initial := Status{State: StateStarting, Reason: "本机持久组件正在启动"}
	if !Supported || config.StateDir == "" {
		initial = Status{State: StateDisabled, Reason: "此平台没有本机持久组件"}
	}
	return &Service{
		config:   config,
		status:   initial,
		readyCh:  make(chan struct{}),
		changed:  make(chan struct{}),
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

// Start launches the lifecycle goroutine. It returns at once.
func (s *Service) Start() {
	s.mu.Lock()
	if s.started || s.status.State == StateDisabled {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	go s.run()
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

// existingProvider returns a provider for sessions that already exist: the
// ready helper's, or an outdated helper's when it speaks this protocol.
func (s *Service) existingProvider() (base.DurableProvider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State == StateReady && s.provider != nil {
		return s.provider, nil
	}
	if s.status.State == StateOutdated && s.existing != nil {
		return s.existing, nil
	}
	return nil, &UnavailableError{Status: s.status}
}

// WaitExisting is WaitReady for listing, attaching to and ending sessions
// that already exist, which an outdated helper of this protocol still serves.
func (s *Service) WaitExisting(ctx context.Context, limit time.Duration) (base.DurableProvider, error) {
	return s.wait(ctx, limit, s.existingProvider)
}

// WaitReady waits up to limit while the helper is still starting, then
// returns the provider like Provider. Other states return at once.
func (s *Service) WaitReady(ctx context.Context, limit time.Duration) (base.DurableProvider, error) {
	return s.wait(ctx, limit, s.Provider)
}

func (s *Service) wait(ctx context.Context, limit time.Duration, provider func() (base.DurableProvider, error)) (base.DurableProvider, error) {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for {
		s.mu.Lock()
		state, changed := s.status.State, s.changed
		s.mu.Unlock()
		if state != StateStarting {
			return provider()
		}
		select {
		case <-changed:
		case <-timer.C:
			return provider()
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}

// Restart replaces an outdated helper, ending its sessions. token must be
// the ConsentToken the user saw; a helper that changed since is refused.
func (s *Service) Restart(ctx context.Context, token string) error {
	return s.send(ctx, request{kind: requestRestart, token: token})
}

// Retry clears a failure, including an open crash-loop breaker, and starts
// the helper again.
func (s *Service) Retry(ctx context.Context) error {
	return s.send(ctx, request{kind: requestRetry})
}

func (s *Service) send(ctx context.Context, req request) error {
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
	s.setStatusWith(status, provider, nil)
}

func (s *Service) setStatusWith(status Status, provider, existing *supervisor.RemoteProvider) {
	s.mu.Lock()
	if status == s.status && provider == s.provider && existing == s.existing {
		s.mu.Unlock()
		return
	}
	s.status = status
	s.provider = provider
	s.existing = existing
	close(s.changed)
	s.changed = make(chan struct{})
	onChange := s.config.OnChange
	s.mu.Unlock()
	if onChange != nil {
		onChange(status)
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
	staged, err := stageExecutable(s.config.StateDir, executable)
	if err != nil {
		return "", fmt.Errorf("stage the helper executable: %w", err)
	}
	return staged, nil
}

func (s *Service) logPath() string {
	return filepath.Join(s.config.StateDir, "helper.log")
}

// run is the single actor that inspects, spawns, watches and replaces the
// helper. quickExits counts consecutive helpers that exited within
// QuickExit of starting, or failed to start at all.
func (s *Service) run() {
	defer close(s.done)
	quickExits := 0
	for s.ctx.Err() == nil {
		running, inspectErr := s.config.inspect(s.ctx, s.config.StateDir)
		if s.ctx.Err() != nil {
			return
		}
		if inspectErr == nil {
			if outdated, status := s.outdated(running); outdated {
				if running.Activity.Total() == 0 {
					// Nothing runs in it, so replacing it ends no session.
					s.config.Logger.Info("replacing an idle local helper from another build", "protocol", running.Protocol, "pid", running.PID)
					if err := s.config.stop(s.ctx, s.config.StateDir, running); err != nil && s.ctx.Err() == nil {
						s.setStatus(Status{State: StateFailed, Reason: "无法替换旧版本地持久组件：" + err.Error()}, nil)
						if !s.awaitRetry(nil) {
							return
						}
					}
					continue
				}
				s.setStatusWith(status, nil, s.existingFor(running))
				if !s.awaitOutdated(running, status.ConsentToken) {
					return
				}
				continue
			}
		}
		s.setStatus(Status{State: StateStarting, Reason: "本机持久组件正在启动"}, nil)
		started := s.config.now()
		executable, err := s.helperExecutable()
		var helper helperHandle
		if err == nil {
			helper, err = s.config.connect(s.ctx, supervisor.HelperConfig{StateDir: s.config.StateDir, Executable: executable, Limits: s.config.Limits})
		}
		if s.ctx.Err() != nil {
			return
		}
		if err != nil {
			quickExits++
			s.config.Logger.Warn("local helper did not start", "error", err, "attempt", quickExits)
			if errors.Is(err, supervisor.ErrLegacyHelperRunning) || errors.Is(err, supervisor.ErrStateMismatch) || errors.Is(err, supervisor.ErrProtocol) {
				// Another helper answers; inspect it again after a pause.
				if !s.pause(quickExits) {
					return
				}
				continue
			}
			if quickExits >= s.config.BreakerThreshold {
				s.setStatus(Status{State: StateFailed, Reason: fmt.Sprintf("本机持久组件连续 %d 次未能启动，已停止自动重试（日志：%s）：%v", quickExits, s.logPath(), err)}, nil)
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
		status := Status{State: StateReady}
		if helper.JobBound() {
			status.Warning = "本机持久组件无法脱离当前进程所在的 Windows 作业对象，退出 NexTerm 时可能被一并结束"
		}
		s.setStatus(status, supervisor.NewRemoteProvider(helper.Client()))
		readyAt := s.config.now()
		if !s.watch(helper) {
			return
		}
		lived := s.config.now().Sub(readyAt)
		if helper.Spawned() && lived < s.config.QuickExit {
			quickExits++
		} else {
			quickExits = 0
		}
		s.config.Logger.Warn("local helper exited", "spawned", helper.Spawned(), "uptime", lived, "started", started, "quick_exits", quickExits)
		if quickExits >= s.config.BreakerThreshold {
			s.setStatus(Status{State: StateFailed, Reason: fmt.Sprintf("本机持久组件连续 %d 次启动后立即退出，已停止自动重启（日志：%s）", quickExits, s.logPath())}, nil)
			if !s.awaitRetry(nil) {
				return
			}
			quickExits = 0
			continue
		}
		s.setStatus(Status{State: StateStarting, Reason: "本机持久组件已退出，正在重新启动"}, nil)
		if quickExits > 0 && !s.pause(quickExits) {
			return
		}
	}
}

// outdated reports a helper that is not this build: an older protocol, or
// the current protocol running another executable.
func (s *Service) outdated(running supervisor.RunningHelper) (bool, Status) {
	status := Status{
		State:          StateOutdated,
		ActiveSessions: running.Activity.Total(),
		HelperProtocol: running.Protocol,
		ConsentToken:   consentToken(running),
	}
	if running.Runtime != nil {
		status.HelperVersion = running.Runtime.Version
	}
	if !running.Current() {
		status.Reason = fmt.Sprintf("本机持久组件为旧版本（协议 v%d），有 %d 个会话在运行；新开的本机终端暂用普通模式。重启组件会结束这些会话", running.Protocol, status.ActiveSessions)
		return true, status
	}
	if running.Runtime == nil {
		// The helper cannot prove which build it runs; it speaks this
		// protocol, so it is used rather than replaced.
		return false, Status{}
	}
	digest, err := s.config.selfDigest()
	if err != nil || digest == running.Runtime.SHA256 {
		return false, Status{}
	}
	version := running.Runtime.Version
	if version == "" {
		version = "未知"
	}
	status.Reason = fmt.Sprintf("本机持久组件为其他版本（%s），有 %d 个会话在运行；新开的本机终端暂用普通模式。重启组件会结束这些会话", version, status.ActiveSessions)
	return true, status
}

func (s *Service) existingFor(running supervisor.RunningHelper) *supervisor.RemoteProvider {
	if !running.Current() {
		return nil
	}
	endpoint, err := supervisor.HelperEndpoint(s.config.StateDir)
	if err != nil {
		return nil
	}
	return supervisor.NewRemoteProvider(supervisor.NewClient(endpoint, s.config.StateDir))
}

func consentToken(running supervisor.RunningHelper) string {
	return fmt.Sprintf("%d:%d:%d", running.Protocol, running.PID, running.Activity.Total())
}

func parseConsentToken(token string) (protocol, pid, total int, ok bool) {
	parts := strings.Split(token, ":")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	values := make([]int, 3)
	for index, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return 0, 0, 0, false
		}
		values[index] = value
	}
	return values[0], values[1], values[2], true
}

// awaitOutdated waits for consent while an outdated helper runs. It re-checks
// the helper on every poll: one whose sessions all ended, or that exited by
// itself, needs no consent anymore. It returns false when the service closes.
func (s *Service) awaitOutdated(running supervisor.RunningHelper, token string) bool {
	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return false
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
				protocol, pid, total, ok := parseConsentToken(req.token)
				if !ok || req.token != token {
					req.reply <- ErrStaleConsent
					continue
				}
				expected := supervisor.RunningHelper{Protocol: protocol, PID: pid, Activity: supervisor.ActivitySnapshot{PTY: total}}
				s.config.Logger.Info("stopping the outdated local helper with consent", "protocol", protocol, "pid", pid, "sessions", total)
				err := s.config.stop(s.ctx, s.config.StateDir, expected)
				if errors.Is(err, supervisor.ErrHelperChanged) {
					req.reply <- fmt.Errorf("%w: %v", ErrStaleConsent, err)
					return true
				}
				req.reply <- err
				return true
			}
		}
	}
}

// awaitRetry blocks a failed helper until Retry. A restart request is
// answered with ErrNotOutdated.
func (s *Service) awaitRetry(ticks <-chan time.Time) bool {
	for {
		select {
		case <-s.ctx.Done():
			return false
		case <-ticks:
			return true
		case req := <-s.requests:
			if req.kind == requestRetry {
				req.reply <- nil
				return true
			}
			req.reply <- ErrNotOutdated
		}
	}
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

// watch blocks while the helper runs. A helper this process spawned is
// watched through its process; one that was already running is polled. It
// returns false when the service closes.
func (s *Service) watch(helper helperHandle) bool {
	exited := helper.Exited()
	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return false
		case <-exited:
			return true
		case <-ticker.C:
			if exited != nil {
				continue
			}
			probeCtx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
			_, err := helper.Client().Activity(probeCtx)
			cancel()
			if err != nil && s.ctx.Err() == nil {
				return true
			}
		case req := <-s.requests:
			if req.kind == requestRetry {
				req.reply <- nil
				continue
			}
			req.reply <- ErrNotOutdated
		}
	}
}
