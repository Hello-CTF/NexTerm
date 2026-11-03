package hostprocs

import (
	"context"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// Shell is the command language of the exec channel of a host. Values match hostports.Shell.
type Shell string

const (
	ShellPOSIX      Shell = "posix"
	ShellPowerShell Shell = "powershell"
	// ShellUnknown means the shell is probed; Windows OpenSSH hosts defaulting to cmd also end up here.
	ShellUnknown Shell = ""
)

const (
	platformPOSIX   = "posix"
	platformWindows = "windows"
)

// Runner executes one command on the host through a channel that does not touch the terminal.
type Runner interface {
	Exec(context.Context, string, base.ExecOptions) (base.ExecResult, error)
}

// Target is what the service needs to know about the session it works on.
type Target struct {
	Runner  Runner
	Shell   Shell
	AssetID string
	// Host is the display name or address of the host, used for audit records.
	Host string
}

// Resolver authorizes the caller for sessionID and returns its target.
type Resolver func(ctx context.Context, sessionID string) (Target, error)

// HostProcess is one row of the host process table. The type is not named Process because the IPC contract
// generator derives TypeScript names from Go type names and services.Process already owns that one.
type HostProcess struct {
	PID  int    `json:"pid"`
	PPID int    `json:"ppid"`
	User string `json:"user,omitempty"`
	// CPU is the percent of a single core on POSIX (ps %cpu, may exceed 100); on Windows the perf counter
	// divided by the core count, which is the share of total CPU.
	CPU float64 `json:"cpu"`
	// Mem is the percent of physical memory.
	Mem        float64 `json:"mem"`
	RSSKiB     int64   `json:"rssKiB"`
	ElapsedSec int64   `json:"elapsedSec"` // -1 when unknown
	Elapsed    string  `json:"elapsed,omitempty"`
	Stat       string  `json:"stat,omitempty"`
	// Name is the display name: comm on POSIX (falls back to ArgName), the image name on Windows.
	Name string `json:"name"`
	// ArgName is the basename of the first token of the command line on POSIX; kill rechecks against it.
	ArgName string `json:"argName,omitempty"`
	Command string `json:"command"`
	// Started is an opaque start-time identifier (POSIX lstart, Windows ISO-8601 UTC); empty when unavailable.
	Started string `json:"started,omitempty"`
	Zombie  bool   `json:"zombie,omitempty"`
}

// ProcessListResult is the answer to a listing request.
type ProcessListResult struct {
	Processes []HostProcess `json:"processes"`
	Platform  string        `json:"platform"`
	Format    string        `json:"format"`
	Truncated bool          `json:"truncated,omitempty"`
	// Partial lists columns the host could not provide.
	Partial []string `json:"partial,omitempty"`
	// SkippedRows counts table rows that could not be parsed and were left out.
	SkippedRows int `json:"skippedRows,omitempty"`
}

// ProcessKillArgs is the input of host_process_kill.
type ProcessKillArgs struct {
	SessionID string `json:"sessionId"`
	PID       int    `json:"pid"`
	// Name is the name the user confirmed: HostProcess.ArgName on POSIX, HostProcess.Name on Windows. The kill
	// is refused when the PID now belongs to another name. At most 1 KiB.
	Name string `json:"name"`
	// PPID and Started come from the listing; the kill is refused when the PID now has another parent, or
	// another start time when both sides have one (busybox hosts report none). Started is at most 1 KiB.
	PPID    int    `json:"ppid"`
	Started string `json:"started,omitempty"`
	Force   bool   `json:"force,omitempty"`
	DryRun  bool   `json:"dryRun,omitempty"`
}

// ProcessKillResult carries the exact command and, unless it was a dry run, its outcome.
type ProcessKillResult struct {
	Command    string `json:"command"`
	Signal     string `json:"signal"`
	Ran        bool   `json:"ran"`
	OK         bool   `json:"ok"`
	ExitCode   *int   `json:"exitCode,omitempty"`
	Output     string `json:"output"`
	DurationMs int64  `json:"durationMs"`
	// AuditError is set when the audit record could not be written; the kill result itself is unaffected.
	AuditError string `json:"auditError,omitempty"`
}

// AuditEvent describes one kill attempt. Result is "ok", "failed", "refused" or "error".
type AuditEvent struct {
	SessionID string
	AssetID   string
	Host      string
	PID       int
	Name      string
	Signal    string
	Command   string
	Result    string
	ExitCode  *int
	Duration  time.Duration
	Output    string
}

// Auditor records kill attempts.
type Auditor interface {
	Audit(context.Context, AuditEvent) error
}

// AuditorFunc adapts a function to Auditor.
type AuditorFunc func(context.Context, AuditEvent) error

func (f AuditorFunc) Audit(ctx context.Context, e AuditEvent) error { return f(ctx, e) }
