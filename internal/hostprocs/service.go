package hostprocs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	// maxIdentityBytes caps the name and start time a caller can send.
	maxIdentityBytes = 1 << 10
	auditTimeout     = 5 * time.Second
)

type Service struct {
	resolve Resolver
	auditor Auditor
	logger  *slog.Logger

	mu        sync.Mutex
	platforms map[string]string
}

// NewService builds the service; auditor may be nil, in which case kills are not recorded.
func NewService(resolver Resolver, auditor Auditor) *Service {
	return &Service{resolve: resolver, auditor: auditor, platforms: make(map[string]string)}
}

// WithLogger sets where failed audit writes are reported; the default is slog.Default().
func (s *Service) WithLogger(logger *slog.Logger) *Service {
	s.logger = logger
	return s
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
func (s *Service) List(ctx context.Context, sessionID string) (ProcessListResult, error) {
	target, err := s.target(ctx, sessionID)
	if err != nil {
		return ProcessListResult{}, err
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
			return ProcessListResult{}, listError(err)
		}
	}
	result, err := s.run(ctx, target, windowsListCommand)
	if err != nil {
		return ProcessListResult{}, listError(err)
	}
	s.rememberPlatform(sessionID, platformWindows)
	return result, nil
}

func (s *Service) run(ctx context.Context, target Target, command string) (ProcessListResult, error) {
	res, err := target.Runner.Exec(ctx, command, base.ExecOptions{Timeout: listTimeout})
	if err != nil {
		return ProcessListResult{}, err
	}
	out, err := ParseOutput(res.Stdout)
	if err != nil {
		return ProcessListResult{}, err
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

// errSystemProcess is returned for processes the platform policy never lets a caller end.
var errSystemProcess = errors.New("不允许结束该系统进程")

func validPID(platform string, pid int) bool {
	if pid <= 1 || pid > math.MaxInt32 {
		return false
	}
	return !(platform == platformWindows && pid == 4)
}

// Kill asks the process to end, or with DryRun only returns the command that would run. The PID is
// re-checked against the confirmed name, parent and start time right before the command, since PIDs are
// reused.
func (s *Service) Kill(ctx context.Context, args ProcessKillArgs) (ProcessKillResult, error) {
	if args.PID <= 1 || args.PID > math.MaxInt32 {
		return ProcessKillResult{}, ipc.BadParam(errors.New("进程 ID 无效"))
	}
	if strings.TrimSpace(args.Name) == "" {
		return ProcessKillResult{}, ipc.BadParam(errors.New("进程名不能为空"))
	}
	if len(args.Name) > maxIdentityBytes || len(args.Started) > maxIdentityBytes {
		return ProcessKillResult{}, ipc.BadParam(errors.New("进程名或启动时间过长"))
	}
	if args.PPID < 0 {
		return ProcessKillResult{}, ipc.BadParam(errors.New("父进程 ID 无效"))
	}
	target, err := s.target(ctx, args.SessionID)
	if err != nil {
		return ProcessKillResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, killTimeout)
	defer cancel()
	platform, err := s.killPlatform(ctx, args.SessionID, target)
	if err != nil {
		return ProcessKillResult{}, err
	}
	command, signal := killCommand(platform, args.PID, args.Force)
	event := AuditEvent{
		SessionID: args.SessionID, AssetID: target.AssetID, Host: target.Host,
		PID: args.PID, Name: args.Name, Signal: signal, Command: command,
	}
	started := time.Now()
	if !validPID(platform, args.PID) {
		if !args.DryRun {
			event.Result, event.Output = "refused", errSystemProcess.Error()
			return ProcessKillResult{}, s.refuse(ctx, event, ipc.CodeBadParam, errSystemProcess.Error(), started)
		}
		return ProcessKillResult{}, ipc.BadParam(errSystemProcess)
	}
	result := ProcessKillResult{Command: command, Signal: signal}
	if args.DryRun {
		return result, nil
	}

	checked, err := target.Runner.Exec(ctx, identityCommand(platform, args.PID), base.ExecOptions{Timeout: killTimeout})
	if err != nil {
		event.Result, event.Output = "error", truncateOutput(err.Error())
		return ProcessKillResult{}, s.refuse(ctx, event, ipc.CodeIO, "复核进程失败: "+err.Error(), started)
	}
	current, found := parseIdentity(checked.Stdout)
	if found && platform == platformWindows && isWindowsCritical(current.Name) {
		event.Result, event.Output = "refused", errSystemProcess.Error()
		return ProcessKillResult{}, s.refuse(ctx, event, ipc.CodeBadParam, errSystemProcess.Error(), started)
	}
	if !found || !identityMatches(platform, current, args) {
		event.Result = "refused"
		return ProcessKillResult{}, s.refuse(ctx, event, ipc.CodeNotFound, "进程已结束或 PID 已被其他进程占用，请刷新后重试", started)
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

// identityMatches compares what the recheck found with what the caller confirmed. Names are compared as in
// the listing: argv basename on POSIX, case-insensitive image name on Windows. Start times only count when
// both sides have one.
func identityMatches(platform string, current identity, args ProcessKillArgs) bool {
	if platform == platformWindows {
		if !strings.EqualFold(current.Name, args.Name) {
			return false
		}
	} else if current.Name != args.Name {
		return false
	}
	if current.PPID != args.PPID {
		return false
	}
	return current.Started == "" || args.Started == "" || current.Started == args.Started
}

// refuse audits a kill that did not run and builds the error for the caller; a failed audit write is
// appended to the message so the missing record stays visible.
func (s *Service) refuse(ctx context.Context, event AuditEvent, code ipc.Code, message string, started time.Time) error {
	event.Duration = time.Since(started)
	if auditErr := s.audit(ctx, event); auditErr != nil {
		message += fmt.Sprintf("（审计记录写入失败：%s）", auditErr)
	}
	return ipc.NewError(code, message)
}

func (s *Service) audit(ctx context.Context, event AuditEvent) error {
	if s.auditor == nil {
		return nil
	}
	// The kill may have used up the request deadline; the record must still be written for the same owner.
	auditCtx, cancel := context.WithTimeout(ipc.WithUserID(context.WithoutCancel(ctx), ipc.OwnerID(ctx)), auditTimeout)
	defer cancel()
	err := s.auditor.Audit(auditCtx, event)
	if err != nil {
		logger := s.logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn("host process kill audit write failed", "session", event.SessionID, "pid", event.PID, "result", event.Result, "error", err)
	}
	return err
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
