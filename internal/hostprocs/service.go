package hostprocs

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	listTimeout = 15 * time.Second
	killTimeout = 15 * time.Second
	maxOutput   = 8 << 10
)

type Service struct {
	resolve Resolver
	auditor Auditor

	mu        sync.Mutex
	platforms map[string]string
}

// NewService builds the service; auditor may be nil, in which case kills are not recorded.
func NewService(resolver Resolver, auditor Auditor) *Service {
	return &Service{resolve: resolver, auditor: auditor, platforms: make(map[string]string)}
}

// Forget drops what was learned about a session, for when it disconnects.
func (s *Service) Forget(sessionID string) {
	s.mu.Lock()
	delete(s.platforms, sessionID)
	s.mu.Unlock()
}

func (s *Service) cachedPlatform(sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.platforms[sessionID]
}

func (s *Service) rememberPlatform(sessionID, platform string) {
	s.mu.Lock()
	s.platforms[sessionID] = platform
	s.mu.Unlock()
}

func (s *Service) target(ctx context.Context, sessionID string) (Target, error) {
	if strings.TrimSpace(sessionID) == "" {
		return Target{}, ipc.BadParam(errors.New("会话 ID 不能为空"))
	}
	if s.resolve == nil {
		return Target{}, ipc.NewError(ipc.CodeUnsupported, "进程视图未配置")
	}
	target, err := s.resolve(ctx, sessionID)
	if err != nil {
		return Target{}, err
	}
	if target.Runner == nil {
		return Target{}, ipc.NewError(ipc.CodeUnsupported, "当前会话不支持独立命令通道")
	}
	return target, nil
}

func platformFor(shell Shell) string {
	switch shell {
	case ShellPOSIX:
		return platformPOSIX
	case ShellPowerShell:
		return platformWindows
	}
	return ""
}

// List lists the processes of the session's host.
func (s *Service) List(ctx context.Context, sessionID string) (ListResult, error) {
	target, err := s.target(ctx, sessionID)
	if err != nil {
		return ListResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()

	platform := platformFor(target.Shell)
	if platform == "" {
		platform = s.cachedPlatform(sessionID)
	}
	if platform != platformWindows {
		result, err := s.run(ctx, target, listScript)
		switch {
		case err == nil:
			s.rememberPlatform(sessionID, platformPOSIX)
			return result, nil
		case !errors.Is(err, ErrNoSentinel) || platform == platformPOSIX || target.Shell == ShellPOSIX:
			return ListResult{}, listError(err)
		}
	}
	result, err := s.run(ctx, target, windowsListCommand)
	if err != nil {
		return ListResult{}, listError(err)
	}
	s.rememberPlatform(sessionID, platformWindows)
	return result, nil
}

func (s *Service) run(ctx context.Context, target Target, command string) (ListResult, error) {
	res, err := target.Runner.Exec(ctx, command, base.ExecOptions{Timeout: listTimeout})
	if err != nil {
		return ListResult{}, err
	}
	out, err := ParseOutput(res.Stdout)
	if err != nil {
		return ListResult{}, err
	}
	out.Truncated = res.Truncated
	return out, nil
}

func listError(err error) error {
	if errors.Is(err, ErrNoSentinel) {
		return ipc.NewError(ipc.CodeUnsupported, "无法识别该主机的进程列表命令输出")
	}
	var ipcErr *ipc.Error
	if errors.As(err, &ipcErr) {
		return err
	}
	return ipc.WrapError(ipc.CodeIO, "获取进程列表失败: "+err.Error(), err)
}

// killPlatform resolves the platform the kill commands must be written for.
func (s *Service) killPlatform(ctx context.Context, sessionID string, target Target) (string, error) {
	if p := platformFor(target.Shell); p != "" {
		return p, nil
	}
	if p := s.cachedPlatform(sessionID); p != "" {
		return p, nil
	}
	res, err := target.Runner.Exec(ctx, posixPlatformProbe, base.ExecOptions{Timeout: killTimeout})
	if err != nil {
		return "", ipc.WrapError(ipc.CodeIO, "探测主机平台失败: "+err.Error(), err)
	}
	platform := platformWindows
	if strings.Contains(res.Stdout, sentinelPrefix+"posix") {
		platform = platformPOSIX
	}
	s.rememberPlatform(sessionID, platform)
	return platform, nil
}

func validPID(platform string, pid int) bool {
	if pid <= 1 || pid > math.MaxInt32 {
		return false
	}
	return !(platform == platformWindows && pid == 4)
}

// Kill asks the process to end, or with DryRun only returns the command that would run. The PID is
// re-checked against the confirmed name right before the command, since PIDs are reused.
func (s *Service) Kill(ctx context.Context, args KillArgs) (KillResult, error) {
	if args.PID <= 1 || args.PID > math.MaxInt32 {
		return KillResult{}, ipc.BadParam(errors.New("进程 ID 无效"))
	}
	if strings.TrimSpace(args.Name) == "" {
		return KillResult{}, ipc.BadParam(errors.New("进程名不能为空"))
	}
	target, err := s.target(ctx, args.SessionID)
	if err != nil {
		return KillResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, killTimeout)
	defer cancel()
	platform, err := s.killPlatform(ctx, args.SessionID, target)
	if err != nil {
		return KillResult{}, err
	}
	if !validPID(platform, args.PID) {
		return KillResult{}, ipc.BadParam(errors.New("不允许结束该系统进程"))
	}
	command, signal := killCommand(platform, args.PID, args.Force)
	result := KillResult{Command: command, Signal: signal}
	if args.DryRun {
		return result, nil
	}
	event := AuditEvent{
		SessionID: args.SessionID, AssetID: target.AssetID, Host: target.Host,
		PID: args.PID, Name: args.Name, Signal: signal, Command: command,
	}
	started := time.Now()

	identity, err := target.Runner.Exec(ctx, identityCommand(platform, args.PID), base.ExecOptions{Timeout: killTimeout})
	if err != nil {
		event.Result, event.Output, event.Duration = "error", truncateOutput(err.Error()), time.Since(started)
		s.audit(ctx, event)
		return KillResult{}, ipc.WrapError(ipc.CodeIO, "复核进程失败: "+err.Error(), err)
	}
	matches := posixNameMatches
	if platform == platformWindows {
		matches = windowsImageMatches
	}
	if !matches(identity.Stdout, args.Name) {
		event.Result, event.Duration = "refused", time.Since(started)
		s.audit(ctx, event)
		return KillResult{}, ipc.NewError(ipc.CodeNotFound, "进程已结束或 PID 已被其他进程占用，请刷新后重试")
	}

	started = time.Now()
	res, err := target.Runner.Exec(ctx, command, base.ExecOptions{Timeout: killTimeout})
	result.Ran = true
	result.DurationMs = time.Since(started).Milliseconds()
	event.Duration = time.Since(started)
	switch {
	case err != nil:
		result.Output = truncateOutput(err.Error())
		event.Result = "error"
	default:
		result.ExitCode = res.ExitCode
		result.OK = res.ExitCode != nil && *res.ExitCode == 0
		result.Output = truncateOutput(strings.TrimSpace(res.Stdout + "\n" + res.Stderr))
		event.Result = "failed"
		if result.OK {
			event.Result = "ok"
		}
	}
	event.ExitCode, event.Output = result.ExitCode, result.Output
	if auditErr := s.audit(ctx, event); auditErr != nil {
		result.AuditError = auditErr.Error()
	}
	return result, nil
}

func (s *Service) audit(ctx context.Context, event AuditEvent) error {
	if s.auditor == nil {
		return nil
	}
	// The kill may have used up the request deadline; the record must still be written.
	return s.auditor.Audit(context.WithoutCancel(ctx), event)
}

func truncateOutput(text string) string {
	if len(text) <= maxOutput {
		return text
	}
	cut := maxOutput
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}
