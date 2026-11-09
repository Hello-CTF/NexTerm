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

// Target is what the service needs to know about the session it samples.
type Target struct {
	Runner Runner
	// Unsupported, when set, is why the host cannot be sampled without running anything (a PowerShell host).
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
		s.mu.Unlock()

		snapshot, raw, outcome := s.run(ctx, target, prev, prevAt)

		s.mu.Lock()
		state.running = nil
		close(done)
		finished := s.now()
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

func (s *Service) run(ctx context.Context, target Target, prev *Raw, prevAt time.Time) (Snapshot, Raw, outcome) {
	ctx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()
	res, err := target.Runner.Exec(ctx, SampleCommand(), base.ExecOptions{Timeout: sampleTimeout, Limits: base.OutputLimits{Stdout: maxOutput}})
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return Snapshot{}, Raw{}, outcomeCanceled
		}
		return failedSnapshot(fmt.Sprintf("采样失败：%v", err)), Raw{}, outcomeFailed
	}
	raw, err := ParseOutput(dropCutLine(res.Stdout, res.Truncated))
	switch {
	case errors.Is(err, ErrNoSentinel):
		return withInterval(unsupportedSnapshot("主机没有可用的 POSIX sh（例如 Windows OpenSSH），无法采集指标", ""), 0), Raw{}, outcomeUnsupported
	case err == nil && raw.OS != "Linux":
		return withInterval(unsupportedSnapshot("主机监控目前只支持 Linux 主机", raw.OS), 0), Raw{}, outcomeUnsupported
	case err != nil:
		return failedSnapshot("无法读取主机指标：/proc 不可读"), Raw{}, outcomeFailed
	}
	var wall time.Duration
	if prev != nil {
		wall = s.now().Sub(prevAt)
	}
	snapshot := Compute(prev, raw, wall)
	snapshot.SampledAt = s.now().UnixMilli()
	return withInterval(snapshot, DefaultInterval), raw, outcomeOK
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
