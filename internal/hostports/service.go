package hostports

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	listTimeout    = 10 * time.Second
	probeTimeout   = 15 * time.Second
	maxProbeOutput = 8 << 10

	platformPOSIX   = "posix"
	platformWindows = "windows"
)

// Runner executes one command on the host through a channel that does not touch the terminal.
type Runner interface {
	Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error)
}

// Target is what the service needs to know about the session it works on.
type Target struct {
	Runner Runner
	Shell  Shell
	// LastOutputAgo reports how long ago any terminal of the session last produced output; false means unknown.
	LastOutputAgo func() (time.Duration, bool)
	// AssetID names the asset behind the session, for audit records.
	AssetID string
}

// AuditEvent describes one connectivity test that was actually executed on a host.
type AuditEvent struct {
	Args    ProbeArgs
	AssetID string
	Result  ProbeResult
}

// AuditFunc receives an AuditEvent after each executed connectivity test. Dry runs and rejected
// requests are never reported.
type AuditFunc func(context.Context, AuditEvent)

// Resolver authorizes the caller for sessionID and returns its target.
type Resolver func(ctx context.Context, sessionID string) (Target, error)

// ListResult is the answer to a listing request.
type ListResult struct {
	Listeners []Listener `json:"listeners"`
	Format    string     `json:"format"`
	Platform  string     `json:"platform"`
	Truncated bool       `json:"truncated,omitempty"`
	// Skipped is set when the terminal was not quiet long enough and no command was run.
	Skipped bool `json:"skipped,omitempty"`
}

// ProbeArgs is a connectivity test request; DryRun only returns the command that would run.
type ProbeArgs struct {
	SessionID string `json:"sessionId"`
	ProbeRequest
	DryRun bool `json:"dryRun,omitempty"`
}

// ProbeResult carries the exact command and, unless it was a dry run, its outcome.
type ProbeResult struct {
	Command string `json:"command"`
	// Script is the PowerShell source behind an -EncodedCommand wrapper; empty when Command is sent as written.
	Script     string `json:"script,omitempty"`
	Ran        bool   `json:"ran"`
	OK         bool   `json:"ok"`
	ExitCode   *int   `json:"exitCode,omitempty"`
	Output     string `json:"output"`
	DurationMs int64  `json:"durationMs"`
}

type Service struct {
	resolve Resolver

	mu        sync.Mutex
	platforms map[string]string
	audit     AuditFunc
}

// SetAudit installs the callback that records executed connectivity tests; call it before serving requests.
func (s *Service) SetAudit(fn AuditFunc) {
	s.mu.Lock()
	s.audit = fn
	s.mu.Unlock()
}

func (s *Service) auditFunc() AuditFunc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.audit
}

func NewService(resolver Resolver) *Service {
	return &Service{resolve: resolver, platforms: make(map[string]string)}
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
		return Target{}, ipc.NewError(ipc.CodeUnsupported, "端口发现未配置")
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

// List lists listening TCP ports of the session's host.
func (s *Service) List(ctx context.Context, sessionID string, withProcess bool) (ListResult, error) {
	return s.ListWithQuiet(ctx, sessionID, withProcess, 0)
}

// ListWithQuiet is List, but runs nothing and returns Skipped when the terminal produced output within quiet.
func (s *Service) ListWithQuiet(ctx context.Context, sessionID string, withProcess bool, quiet time.Duration) (ListResult, error) {
	target, err := s.target(ctx, sessionID)
	if err != nil {
		return ListResult{}, err
	}
	if quiet > 0 && target.LastOutputAgo != nil {
		if ago, known := target.LastOutputAgo(); known && ago < quiet {
			return ListResult{Listeners: []Listener{}, Skipped: true}, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()

	platform := platformFor(target.Shell)
	if platform == "" {
		platform = s.cachedPlatform(sessionID)
	}
	if platform != platformWindows {
		result, err := s.listPOSIX(ctx, target, withProcess)
		switch {
		case err == nil:
			s.rememberPlatform(sessionID, platformPOSIX)
			return result, nil
		case !errors.Is(err, ErrNoSentinel) || platform == platformPOSIX || target.Shell == ShellPOSIX:
			return ListResult{}, listError(err)
		}
	}
	result, err := s.listWindows(ctx, target, withProcess)
	if err != nil {
		return ListResult{}, listError(err)
	}
	s.rememberPlatform(sessionID, platformWindows)
	return result, nil
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

func listError(err error) error {
	if errors.Is(err, ErrNoSentinel) {
		return ipc.NewError(ipc.CodeUnsupported, "无法识别该主机的端口列表命令输出")
	}
	var ipcErr *ipc.Error
	if errors.As(err, &ipcErr) {
		return err
	}
	return ipc.WrapError(ipc.CodeIO, "端口发现失败: "+err.Error(), err)
}

func (s *Service) listPOSIX(ctx context.Context, target Target, withProcess bool) (ListResult, error) {
	res, err := target.Runner.Exec(ctx, ListCommand(withProcess), base.ExecOptions{Timeout: listTimeout})
	if err != nil {
		return ListResult{}, err
	}
	listeners, format, err := ParseListOutput(dropCutLine(res.Stdout, res.Truncated))
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{Listeners: nonNil(listeners), Format: string(format), Platform: platformPOSIX, Truncated: res.Truncated}, nil
}

func (s *Service) listWindows(ctx context.Context, target Target, withProcess bool) (ListResult, error) {
	res, err := target.Runner.Exec(ctx, windowsNetstatCommand, base.ExecOptions{Timeout: listTimeout})
	if err != nil {
		return ListResult{}, err
	}
	listeners, err := Parse(FormatNetstatWindows, dropCutLine(res.Stdout, res.Truncated))
	if err != nil {
		return ListResult{}, fmt.Errorf("%w: %v", ErrNoSentinel, err)
	}
	truncated := res.Truncated
	// "-p tcp" covers IPv4 only; the IPv6 table is best effort.
	if v6, err := target.Runner.Exec(ctx, windowsNetstat6Command, base.ExecOptions{Timeout: listTimeout}); err == nil {
		if more, err := Parse(FormatNetstatWindows, dropCutLine(v6.Stdout, v6.Truncated)); err == nil {
			listeners = Normalize(append(listeners, more...))
			truncated = truncated || v6.Truncated
		}
	}
	if withProcess {
		if tasks, err := target.Runner.Exec(ctx, windowsTasklistCommand, base.ExecOptions{Timeout: listTimeout}); err == nil {
			AttachProcessNames(listeners, ParseTasklistCSV(tasks.Stdout))
		}
	}
	return ListResult{Listeners: nonNil(listeners), Format: string(FormatNetstatWindows), Platform: platformWindows, Truncated: truncated}, nil
}

// dropCutLine removes the last line of truncated output, which the cut may have left incomplete. The
// first line is kept so the sentinel survives.
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

func nonNil(l []Listener) []Listener {
	if l == nil {
		return []Listener{}
	}
	return l
}

// detectShell resolves ShellUnknown to the shell probe commands must be written for.
func (s *Service) detectShell(ctx context.Context, sessionID string, target Target) Shell {
	if target.Shell != ShellUnknown {
		return target.Shell
	}
	platform := s.cachedPlatform(sessionID)
	if platform == "" {
		res, err := target.Runner.Exec(ctx, posixPlatformProbeShell, base.ExecOptions{Timeout: listTimeout})
		if err != nil {
			return ShellUnknown
		}
		platform = platformWindows
		if strings.Contains(res.Stdout, sentinelPrefix+"posix") {
			platform = platformPOSIX
		}
		s.rememberPlatform(sessionID, platform)
	}
	if platform == platformPOSIX {
		return ShellPOSIX
	}
	return ShellUnknown
}

// Probe runs, or with DryRun only builds, a connectivity test on the session's host.
func (s *Service) Probe(ctx context.Context, args ProbeArgs) (ProbeResult, error) {
	if err := ValidateProbe(args.ProbeRequest); err != nil {
		return ProbeResult{}, ipc.BadParam(err)
	}
	target, err := s.target(ctx, args.SessionID)
	if err != nil {
		return ProbeResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	command, script, err := probeCommandAndScript(s.detectShell(ctx, args.SessionID, target), args.ProbeRequest)
	if err != nil {
		return ProbeResult{}, ipc.BadParam(err)
	}
	if args.DryRun {
		return ProbeResult{Command: command, Script: script}, nil
	}
	started := time.Now()
	res, err := target.Runner.Exec(ctx, command, base.ExecOptions{Timeout: probeTimeout})
	result := ProbeResult{Command: command, Script: script, Ran: true, DurationMs: time.Since(started).Milliseconds()}
	if err != nil {
		result.Output = truncateOutput(err.Error())
		s.recordProbe(ctx, args, target, result)
		return result, nil
	}
	result.ExitCode = res.ExitCode
	result.OK = res.ExitCode != nil && *res.ExitCode == 0
	result.Output = truncateOutput(strings.TrimSpace(res.Stdout + "\n" + res.Stderr))
	s.recordProbe(ctx, args, target, result)
	return result, nil
}

func (s *Service) recordProbe(ctx context.Context, args ProbeArgs, target Target, result ProbeResult) {
	if audit := s.auditFunc(); audit != nil {
		audit(ctx, AuditEvent{Args: args, AssetID: target.AssetID, Result: result})
	}
}

func truncateOutput(text string) string {
	return TruncateOutput(text, maxProbeOutput)
}

// TruncateOutput cuts text to at most max bytes without splitting a UTF-8 character.
func TruncateOutput(text string, max int) string {
	if len(text) <= max {
		return text
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}
