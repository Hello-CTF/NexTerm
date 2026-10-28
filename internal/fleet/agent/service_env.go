package agent

import (
	"path/filepath"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/usershell"
)

// ServiceEnvironment is the SHELL and PATH recorded in the service definition at install time, so the
// agent and the terminals it opens do not start with the service manager's minimal environment.
type ServiceEnvironment struct {
	Shell      string
	ShellFrom  string // "SHELL" (install environment) or "account" (login shell database)
	Path       string
	PathSource string // "install environment" when recorded, empty when omitted
}

// EnvironmentReporter is implemented by service managers that persist an environment in the service
// definition. It reports what the installed definition holds, or false when nothing is installed.
type EnvironmentReporter interface {
	RecordedEnvironment() (ServiceEnvironment, bool)
}

// captureServiceEnvironment derives the environment to persist. lookupShell is the account shell
// fallback (usershell.Lookup in production).
func captureServiceEnvironment(getenv func(string) string, lookupShell func() string) ServiceEnvironment {
	var env ServiceEnvironment
	if shell := strings.TrimSpace(getenv("SHELL")); usershell.Usable(shell) && saneEnvValue(shell) {
		env.Shell, env.ShellFrom = shell, "SHELL"
	} else if lookupShell != nil {
		if shell := lookupShell(); usershell.Usable(shell) && saneEnvValue(shell) {
			env.Shell, env.ShellFrom = shell, "account"
		}
	}
	if path := sanePATH(getenv("PATH")); path != "" {
		env.Path, env.PathSource = path, "install environment"
	}
	return env
}

func saneEnvValue(value string) bool {
	return value != "" && !strings.ContainsAny(value, "\n\r\x00")
}

// sanePATH keeps the absolute entries of path; a value with a line break or no absolute entry yields "".
func sanePATH(path string) string {
	path = strings.TrimSpace(path)
	if !saneEnvValue(path) {
		return ""
	}
	var kept []string
	for _, entry := range strings.Split(path, string(filepath.ListSeparator)) {
		if filepath.IsAbs(entry) {
			kept = append(kept, entry)
		}
	}
	return strings.Join(kept, string(filepath.ListSeparator))
}

func (e ServiceEnvironment) describe() []string {
	lines := make([]string, 0, 2)
	if e.Shell != "" {
		lines = append(lines, "服务环境 SHELL="+e.Shell+" (来源: "+e.ShellFrom+")")
	} else {
		lines = append(lines, "服务环境 SHELL: 未记录 (运行时回退到账户登录 shell)")
	}
	if e.Path != "" {
		lines = append(lines, "服务环境 PATH 来源: "+e.PathSource+" ("+e.Path+")")
	} else {
		lines = append(lines, "服务环境 PATH: 未记录 (使用服务管理器默认 PATH)")
	}
	return lines
}

// finishRecorded labels a definition read back from disk; the original source is not stored there.
func finishRecorded(env *ServiceEnvironment) {
	if env.Shell != "" {
		env.ShellFrom = "service definition"
	}
	if env.Path != "" {
		env.PathSource = "service definition"
	}
}
