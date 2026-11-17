package execbatch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/platform/psquote"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// Shell is the command language a batch is written in.
type Shell string

const (
	ShellPOSIX      Shell = "posix"
	ShellPowerShell Shell = "powershell"
	ShellCmd        Shell = "cmd"
)

// NormalizeShell maps the empty shell of older requests and history rows to POSIX and rejects unknown names.
func NormalizeShell(shell Shell) (Shell, error) {
	switch shell {
	case "", ShellPOSIX:
		return ShellPOSIX, nil
	case ShellPowerShell, ShellCmd:
		return shell, nil
	}
	return "", fmt.Errorf("%w: shell must be posix, powershell or cmd", ErrInvalid)
}

// TargetExec is how a target's transport runs the command string it is given.
type TargetExec string

const (
	// ExecPOSIX passes the string to a POSIX shell (sh -c); it is the zero value.
	ExecPOSIX TargetExec = ""
	// ExecSSH passes the string to the remote default shell of an SSH host whose platform is not known. It is a
	// POSIX shell unless the host turns out to be Windows OpenSSH.
	ExecSSH TargetExec = "ssh"
	// ExecWindowsSSH is an SSH host known to be Windows OpenSSH; its default shell is cmd or PowerShell.
	ExecWindowsSSH TargetExec = "windows-ssh"
	// ExecPowerShellScript embeds the string in a PowerShell script that maps $LASTEXITCODE to the exit code (WinRM).
	ExecPowerShellScript TargetExec = "powershell-script"
	// ExecPowerShellCommand passes the string to powershell -Command (Windows managed devices and local sessions).
	ExecPowerShellCommand TargetExec = "powershell-command"
)

// Platform names reported for targets.
const (
	PlatformPOSIX   = "posix"
	PlatformWindows = "windows"
	PlatformUnknown = "unknown"
)

// Shell support verdicts.
const (
	ShellSupported   = "supported"
	ShellUnsupported = "unsupported"
	// ShellDetect means the target is probed when the batch runs; it is skipped if the probe fails.
	ShellDetect = "detect"
)

const (
	// windowsCommandLineLimit is the longest command line cmd.exe accepts; Windows OpenSSH runs exec requests
	// through the default shell, so a longer encoded command would be refused by the host.
	windowsCommandLineLimit = 8191
	// createProcessLimit bounds the command line of a process started with powershell -Command.
	createProcessLimit  = 32767
	windowsProbeTimeout = 15 * time.Second
	// maxCapabilitySessions bounds one capability lookup; the selector asks for every connected session at once.
	maxCapabilitySessions = 1024
	windowsProbeMarker    = "NEXTERM-EXECBATCH-WINDOWS"
)

// powerShellPrelude keeps progress records off stderr and non-ASCII output intact.
const powerShellPrelude = "$ProgressPreference='SilentlyContinue'\n" +
	"try{[Console]::OutputEncoding=[Text.Encoding]::UTF8}catch{}\n"

// windowsProbeCommand prints the marker only on Windows; it fails on a host without PowerShell.
var windowsProbeCommand = psquote.ShortCommand(powerShellPrelude +
	"if ($env:OS -eq 'Windows_NT') { [Console]::Out.Write('" + windowsProbeMarker + "') }")

// ShellSupport is whether a target can run one shell, with the reason when it cannot or must be probed.
type ShellSupport struct {
	Shell   Shell  `json:"shell"`
	Support string `json:"support"`
	Reason  string `json:"reason,omitempty"`
}

// TargetCapability describes a connected target for the shell selector, without probing it.
type TargetCapability struct {
	SessionID string         `json:"sessionId"`
	Platform  string         `json:"platform"`
	Shells    []ShellSupport `json:"shells"`
	Error     string         `json:"error,omitempty"`
}

func targetPlatform(exec TargetExec) string {
	switch exec {
	case ExecPOSIX:
		return PlatformPOSIX
	case ExecSSH:
		return PlatformUnknown
	default:
		return PlatformWindows
	}
}

// shellSupport decides whether a target can run shell before any probe.
func shellSupport(exec TargetExec, shell Shell) ShellSupport {
	support := ShellSupport{Shell: shell, Support: ShellSupported}
	switch shell {
	case ShellPOSIX:
		if exec == ExecPowerShellScript || exec == ExecPowerShellCommand {
			support.Support = ShellUnsupported
			support.Reason = "Windows 目标不能执行 POSIX shell 命令，请选择 PowerShell 或 cmd"
		}
	default:
		switch exec {
		case ExecPOSIX:
			support.Support = ShellUnsupported
			support.Reason = "目标不是 Windows，不能执行 " + shellLabel(shell) + " 命令"
		case ExecSSH:
			support.Support = ShellDetect
			support.Reason = "执行前检测该 SSH 主机是否为 Windows"
		}
	}
	return support
}

func shellLabel(shell Shell) string {
	switch shell {
	case ShellPowerShell:
		return "PowerShell"
	case ShellCmd:
		return "cmd"
	}
	return "POSIX shell"
}

// capabilityFor reports what a resolved target supports.
func capabilityFor(target Target) TargetCapability {
	capability := TargetCapability{SessionID: target.SessionID, Platform: targetPlatform(target.Exec)}
	for _, shell := range []Shell{ShellPOSIX, ShellPowerShell, ShellCmd} {
		capability.Shells = append(capability.Shells, shellSupport(target.Exec, shell))
	}
	return capability
}

// TargetCapabilities resolves each session and reports which shells it can run. Resolution failures are
// reported per target instead of failing the call, so the selector can still show the other targets.
func (s *Service) TargetCapabilities(ctx context.Context, sessionIDs []string) ([]TargetCapability, error) {
	if len(sessionIDs) > maxCapabilitySessions {
		return nil, fmt.Errorf("%w: at most %d sessions", ErrInvalid, maxCapabilitySessions)
	}
	if s.resolver == nil {
		return nil, fmt.Errorf("%w: no session resolver", ErrUnsupported)
	}
	capabilities := make([]TargetCapability, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		target, err := s.resolver.Resolve(ctx, sessionID)
		if err != nil {
			capabilities = append(capabilities, TargetCapability{SessionID: sessionID, Platform: PlatformUnknown, Shells: []ShellSupport{}, Error: err.Error()})
			continue
		}
		target.SessionID = sessionID
		capabilities = append(capabilities, capabilityFor(target))
	}
	return capabilities, nil
}

// targetPlan is what one batch does on each target: the command string to send, or why it is skipped.
type targetPlan struct {
	command string
	skip    string
}

// planTargets builds the per-target command for shell, probing SSH hosts of unknown platform when a Windows
// shell is chosen. A raw plan (WithPowerShellTargets) sends the command unchanged to every target.
func planTargets(ctx context.Context, shell Shell, raw bool, targets []Target, command string, concurrency int) []targetPlan {
	plans := make([]targetPlan, len(targets))
	var probe []int
	for index, target := range targets {
		if raw {
			plans[index] = targetPlan{command: command}
			continue
		}
		support := shellSupport(target.Exec, shell)
		switch support.Support {
		case ShellUnsupported:
			plans[index] = targetPlan{skip: support.Reason}
		case ShellDetect:
			probe = append(probe, index)
		default:
			plans[index] = commandPlan(shell, target.Exec, command)
		}
	}
	if len(probe) == 0 {
		return plans
	}
	if concurrency < 1 {
		concurrency = 1
	}
	jobs := make(chan int, len(probe))
	for _, index := range probe {
		jobs <- index
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(concurrency, len(probe)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				windows, reason := probeWindows(ctx, targets[index])
				if !windows {
					plans[index] = targetPlan{skip: reason}
					continue
				}
				plans[index] = commandPlan(shell, ExecWindowsSSH, command)
			}
		}()
	}
	workers.Wait()
	return plans
}

// probeWindows reports whether an SSH host runs Windows with PowerShell available.
func probeWindows(ctx context.Context, target Target) (bool, string) {
	if target.Transport == nil {
		return false, "目标没有可用的连接"
	}
	probeCtx, cancel := context.WithTimeout(ctx, windowsProbeTimeout)
	defer cancel()
	result, err := target.Transport.Exec(probeCtx, windowsProbeCommand, base.ExecOptions{
		Timeout: windowsProbeTimeout, ExpectedGeneration: target.Generation,
		Limits: base.OutputLimits{Stdout: 4 << 10, Stderr: 4 << 10},
	})
	if strings.Contains(result.Stdout, windowsProbeMarker) {
		return true, ""
	}
	if ctx.Err() != nil {
		return false, "平台检测已取消"
	}
	if err != nil {
		if _, remoteExit := base.ExitCode(err); !remoteExit {
			return false, "平台检测失败，未执行：" + err.Error()
		}
	}
	return false, "该 SSH 主机不是 Windows（未检测到 Windows PowerShell），未执行"
}

// commandPlan wraps command for the way exec runs strings. The caller has already decided that exec supports shell.
func commandPlan(shell Shell, exec TargetExec, command string) targetPlan {
	if shell == ShellPOSIX {
		return targetPlan{command: command}
	}
	body := command
	if shell == ShellCmd {
		body = cmdInvocation(command)
	}
	script := powerShellBody(body)
	switch exec {
	case ExecPowerShellScript:
		// The WinRM wrapper exits with $LASTEXITCODE, which the body has already set.
		return targetPlan{command: script}
	case ExecPowerShellCommand:
		wrapped := script + "\nexit $global:LASTEXITCODE"
		if len(wrapped) > createProcessLimit-64 {
			return targetPlan{skip: fmt.Sprintf("命令包装后超过 Windows 命令行长度上限（%d 字符），未执行", createProcessLimit)}
		}
		return targetPlan{command: wrapped}
	default:
		// The remote default shell may be cmd or PowerShell; an encoded command runs the same under both.
		wrapped := psquote.ShortCommand(script + "\nexit $global:LASTEXITCODE")
		if len(wrapped) > windowsCommandLineLimit {
			return targetPlan{skip: fmt.Sprintf("命令编码后超过 Windows 命令行长度上限（%d 字符），请缩短命令；未执行", windowsCommandLineLimit)}
		}
		return targetPlan{command: wrapped}
	}
}

// powerShellBody runs command and leaves its outcome in $LASTEXITCODE: 0 when the last statement succeeded, the
// native exit code when a native command failed, and 1 when a cmdlet failed.
func powerShellBody(command string) string {
	return powerShellPrelude +
		"$global:LASTEXITCODE = $null\n" +
		command + "\n" +
		"if ($?) { $global:LASTEXITCODE = 0 } elseif (-not $global:LASTEXITCODE) { $global:LASTEXITCODE = 1 }"
}

// cmdInvocation runs command through cmd.exe from PowerShell. With /s cmd strips only the outer quotes and runs
// the rest verbatim, so command needs no escaping. The leading space makes PowerShell's legacy native argument
// passing, which cmd.exe always gets, wrap the argument in exactly one pair of quotes.
func cmdInvocation(command string) string {
	return "$PSNativeCommandArgumentPassing = 'Legacy'\n" +
		"& cmd.exe /d /s /c " + psquote.Quote(" "+command)
}
