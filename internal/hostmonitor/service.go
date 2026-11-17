// Package hostmonitor samples live resource usage of a session's host over the session's exec channel.
package hostmonitor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	// DefaultInterval is how often a caller asks for a sample.
	DefaultInterval = 3 * time.Second
	// minSpacing is the shortest time between two commands on one host; callers that ask sooner, such as a
	// second window on the same session, share the last sample.
	minSpacing = time.Second
	// maxBackoff caps the wait after repeated failures.
	maxBackoff    = time.Minute
	sampleTimeout = 8 * time.Second
	maxOutput     = 256 << 10
)

// Runner executes one command on the host through a channel that does not touch the terminal.
type Runner interface {
	Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error)
}

// Platform is the command language a host is sampled with.
type Platform string

const (
	// PlatformUnknown is a host whose platform is learned from the first sample: the POSIX script runs, and a host
	// that turns out to have no sh, such as Windows OpenSSH with cmd, is sampled through PowerShell instead.
	PlatformUnknown Platform = ""
	PlatformPOSIX   Platform = "posix"
	PlatformWindows Platform = "windows"
)

// Target is what the service needs to know about the session it samples.
type Target struct {
	Runner Runner
	// Platform is what is known about the host before anything runs on it.
	Platform Platform
	// Unsupported, when set, is why the host cannot be sampled without running anything (a managed device).
	Unsupported string
}

// Resolver authorizes the caller for sessionID and returns its target.
type Resolver func(ctx context.Context, sessionID string) (Target, error)

type sessionState struct {
	raw   *Raw
	rawAt time.Time
	// last is the latest result and lastAt when it was produced.
	last   Snapshot
	lastAt time.Time
	// failures counts consecutive failed samples; retryAt is when the next command may run.
	failures int
	retryAt  time.Time
	// unsupported is set once the host turned out not to be sampleable; it is kept until the session ends.
	unsupported *Snapshot
	// running is closed when the sample in flight finishes.
	running chan struct{}
	// noDisks is set once df ran too long on the host (or the whole sample timed out while it was part of it);
	// df is then left out for the rest of the session so a hung mount cannot pin a remote process per sample.
	noDisks bool
	// platform is what the first sample that ran its script found out about a host the target left unknown.
	platform Platform
}

type Service struct {
	resolve Resolver
	now     func() time.Time

	mu     sync.Mutex
	states map[string]*sessionState
}

func NewService(resolver Resolver) *Service {
	return &Service{resolve: resolver, now: time.Now, states: make(map[string]*sessionState)}
}

// Forget drops what was learned about a session, for when it disconnects.
func (s *Service) Forget(sessionID string) {
	s.mu.Lock()
	delete(s.states, sessionID)
	s.mu.Unlock()
}

func (s *Service) target(ctx context.Context, sessionID string) (Target, error) {
	if strings.TrimSpace(sessionID) == "" {
		return Target{}, ipc.BadParam(errors.New("会话 ID 不能为空"))
	}
	if s.resolve == nil {
		return Target{}, ipc.NewError(ipc.CodeUnsupported, "主机监控未配置")
	}
	target, err := s.resolve(ctx, sessionID)
	if err != nil {
		return Target{}, err
	}
	if target.Runner == nil && target.Unsupported == "" {
		target.Unsupported = "当前会话不支持独立命令通道"
	}
	return target, nil
}

func unsupportedSnapshot(reason, platform string) Snapshot {
	return Snapshot{Status: StatusUnsupported, Reason: reason, Platform: platform, Network: []NetInterface{}, Disks: []Disk{}}
}

// Sample returns the current metrics of the session's host. At most one command runs per session at a time
// and at most one per minSpacing; concurrent callers share its result. After a failure no command runs until
// the backoff has passed, and a host found unsupported is never sampled again for the session.
func (s *Service) Sample(ctx context.Context, sessionID string) (Snapshot, error) {
	target, err := s.target(ctx, sessionID)
	if err != nil {
		return Snapshot{}, err
	}
	if target.Unsupported != "" {
		return withInterval(unsupportedSnapshot(target.Unsupported, ""), 0), nil
	}
	for {
		s.mu.Lock()
		state := s.states[sessionID]
		if state == nil {
			state = &sessionState{}
			s.states[sessionID] = state
		}
		if state.unsupported != nil {
			snapshot := *state.unsupported
			s.mu.Unlock()
			return snapshot, nil
		}
		if running := state.running; running != nil {
			s.mu.Unlock()
			select {
			case <-running:
				continue
			case <-ctx.Done():
				return Snapshot{}, ctx.Err()
			}
		}
		now := s.now()
		if state.failures > 0 && now.Before(state.retryAt) {
			snapshot := state.last
			snapshot.RetryAfterMs = state.retryAt.Sub(now).Milliseconds()
			s.mu.Unlock()
			return snapshot, nil
		}
		if state.failures == 0 && !state.lastAt.IsZero() && now.Sub(state.lastAt) < minSpacing {
			snapshot := state.last
			s.mu.Unlock()
			return snapshot, nil
		}
		done := make(chan struct{})
		state.running = done
		prev, prevAt := state.raw, state.rawAt
		withDisks := !state.noDisks
		platform := target.Platform
		if platform == PlatformUnknown {
			platform = state.platform
		}
		s.mu.Unlock()

		result := s.run(ctx, target.Runner, platform, prev, prevAt, withDisks)
		snapshot, raw, outcome := result.snapshot, result.raw, result.outcome

		s.mu.Lock()
		state.running = nil
		close(done)
		finished := s.now()
		if withDisks && (result.dfTimedOut || raw.DisksTimedOut) {
			state.noDisks = true
		}
		if result.platform != PlatformUnknown {
			state.platform = result.platform
		}
		switch outcome {
		case outcomeCanceled:
			s.mu.Unlock()
			return Snapshot{}, ctx.Err()
		case outcomeUnsupported:
			state.unsupported = &snapshot
		case outcomeFailed:
			state.failures++
			wait := backoff(state.failures)
			state.retryAt = finished.Add(wait)
			snapshot.RetryAfterMs = wait.Milliseconds()
		case outcomeOK:
			state.failures = 0
			state.raw, state.rawAt = &raw, finished
			snapshot.DisksUnavailable = state.noDisks
			if state.noDisks {
				snapshot.Disks = []Disk{}
			}
		}
		state.last, state.lastAt = snapshot, finished
		s.mu.Unlock()
		return snapshot, nil
	}
}

type outcome int

const (
	outcomeOK outcome = iota
	outcomeFailed
	outcomeUnsupported
	outcomeCanceled
)

type runResult struct {
	snapshot Snapshot
	raw      Raw
	outcome  outcome
	// platform is the script that produced the sample.
	platform Platform
	// dfTimedOut reports that the POSIX script ran out of time, which is how a host without a usable timeout
	// command shows a hung df.
	dfTimedOut bool
}

const (
	reasonNoShell      = "主机上既没有可用的 POSIX sh，也没有可用的 PowerShell，无法采集指标"
	reasonNoPOSIXSh    = "主机没有可用的 POSIX sh，无法采集指标"
	reasonNoPowerShell = "主机上没有可用的 PowerShell，无法采集指标"
)

// run executes one sample with the script for platform. An unknown platform runs the POSIX script first and falls
// back to PowerShell when the host has no sh or its sh is a POSIX layer on Windows.
func (s *Service) run(ctx context.Context, runner Runner, platform Platform, prev *Raw, prevAt time.Time, withDisks bool) runResult {
	if platform != PlatformWindows {
		result, fallback := s.runPOSIX(ctx, runner, platform, prev, prevAt, withDisks)
		if !fallback {
			return result
		}
	}
	return s.runWindows(ctx, runner, platform == PlatformWindows, prev, prevAt, withDisks)
}

// runPOSIX reports fallback when the host may still be sampled through PowerShell.
func (s *Service) runPOSIX(ctx context.Context, runner Runner, platform Platform, prev *Raw, prevAt time.Time, withDisks bool) (runResult, bool) {
	res, failed := s.exec(ctx, runner, SampleCommand(withDisks))
	if failed != nil {
		return *failed, false
	}
	raw, err := ParseOutput(res)
	switch {
	case errors.Is(err, ErrNoSentinel):
		if platform == PlatformPOSIX {
			return unsupportedResult(reasonNoPOSIXSh, ""), false
		}
		return runResult{}, true
	case err == nil && windowsOSName(raw.OS) && platform != PlatformPOSIX:
		return runResult{}, true
	case err == nil && raw.OS != "Linux":
		return unsupportedResult("主机监控目前只支持 Linux 和 Windows 主机", raw.OS), false
	case err != nil:
		return runResult{snapshot: failedSnapshot("无法读取主机指标：/proc 不可读"), raw: raw, outcome: outcomeFailed, platform: PlatformPOSIX}, false
	}
	return runResult{snapshot: s.compute(prev, prevAt, raw), raw: raw, outcome: outcomeOK, platform: PlatformPOSIX}, false
}

// runWindows samples through PowerShell; known says the target named the host as Windows rather than the POSIX
// script finding no sh. A sample that times out does not drop the drives, which never block like a hung mount.
func (s *Service) runWindows(ctx context.Context, runner Runner, known bool, prev *Raw, prevAt time.Time, withDisks bool) runResult {
	res, failed := s.exec(ctx, runner, WindowsSampleCommand(withDisks))
	if failed != nil {
		failed.dfTimedOut = false
		return *failed
	}
	raw, err := ParseWindowsOutput(res)
	switch {
	case errors.Is(err, ErrNoSentinel):
		if known {
			return unsupportedResult(reasonNoPowerShell, windowsOS)
		}
		return unsupportedResult(reasonNoShell, "")
	case err != nil:
		return runResult{snapshot: failedSnapshot(windowsFailure(err)), outcome: outcomeFailed, platform: PlatformWindows}
	}
	return runResult{snapshot: s.compute(prev, prevAt, raw), raw: raw, outcome: outcomeOK, platform: PlatformWindows}
}

// exec runs command and returns its output, or the result of a command that did not complete.
func (s *Service) exec(ctx context.Context, runner Runner, command string) (string, *runResult) {
	ctx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()
	res, err := runner.Exec(ctx, command, base.ExecOptions{Timeout: sampleTimeout, Limits: base.OutputLimits{Stdout: maxOutput}})
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return "", &runResult{outcome: outcomeCanceled}
		}
		timedOut := errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)
		return "", &runResult{snapshot: failedSnapshot(fmt.Sprintf("采样失败：%v", err)), outcome: outcomeFailed, dfTimedOut: timedOut}
	}
	return dropCutLine(res.Stdout, res.Truncated), nil
}

func unsupportedResult(reason, platform string) runResult {
	return runResult{snapshot: withInterval(unsupportedSnapshot(reason, platform), 0), outcome: outcomeUnsupported}
}

func (s *Service) compute(prev *Raw, prevAt time.Time, raw Raw) Snapshot {
	var wall time.Duration
	if prev != nil {
		wall = s.now().Sub(prevAt)
	}
	snapshot := Compute(prev, raw, wall)
	snapshot.SampledAt = s.now().UnixMilli()
	return withInterval(snapshot, DefaultInterval)
}

func failedSnapshot(reason string) Snapshot {
	return withInterval(Snapshot{Status: StatusError, Reason: reason, Network: []NetInterface{}, Disks: []Disk{}}, DefaultInterval)
}

func withInterval(snapshot Snapshot, interval time.Duration) Snapshot {
	snapshot.IntervalMs = interval.Milliseconds()
	return snapshot
}

// backoff doubles the wait from DefaultInterval with each consecutive failure, up to maxBackoff.
func backoff(failures int) time.Duration {
	wait := DefaultInterval
	for range failures {
		wait *= 2
		if wait >= maxBackoff {
			return maxBackoff
		}
	}
	return wait
}

// dropCutLine removes the last line of truncated output, which the cut may have left incomplete.
func dropCutLine(stdout string, truncated bool) string {
	if !truncated {
		return stdout
	}
	trimmed := strings.TrimRight(stdout, "\r\n")
	i := strings.LastIndexByte(trimmed, '\n')
	if i < 0 {
		return stdout
	}
	return trimmed[:i+1]
}
