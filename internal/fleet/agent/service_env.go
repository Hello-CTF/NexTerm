package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/applog"
	"github.com/Hello-CTF/NexTerm/internal/shellenv"
)

// ServiceEnvironment is a SHELL and PATH found in an installed service definition. Current installs record
// neither: the shell and environment are resolved at runtime. Values here come from older installs, are no
// longer authoritative, and disappear when the service is installed again.
type ServiceEnvironment struct {
	Shell string
	Path  string
}

// EnvironmentReporter is implemented by service managers that can read the environment stored in the
// service definition. It reports false when nothing is installed.
type EnvironmentReporter interface {
	RecordedEnvironment() (ServiceEnvironment, bool)
}

func (e ServiceEnvironment) describe() []string {
	if e.Shell == "" && e.Path == "" {
		return []string{"服务定义环境: 未记录 SHELL/PATH (运行时解析)"}
	}
	lines := make([]string, 0, 2)
	if e.Shell != "" {
		lines = append(lines, "服务定义 SHELL="+e.Shell+" (旧版安装记录, 不再采用; 重新执行 install 可清除)")
	}
	if e.Path != "" {
		lines = append(lines, "服务定义 PATH="+e.Path+" (旧版安装记录, 仅作为解析失败时的补充; 重新执行 install 可清除)")
	}
	return lines
}

// shellFile is the explicit shell setting inside the data directory.
func shellFile(dataDir string) string { return filepath.Join(dataDir, shellenv.ShellFileName) }

// useDataDirShell points the process shell resolution at the explicit shell setting of dataDir, so the
// supervisor helper this process starts inherits it.
func useDataDirShell(dataDir string) {
	_ = os.Setenv(shellenv.ShellFileEnv, shellFile(dataDir))
}

// applyShellFlag stores the explicit shell requested with --shell; "auto" clears it. An empty value leaves
// the current setting untouched.
func applyShellFlag(dataDir, value string) (string, error) {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return "", nil
	case value == "auto":
		if err := shellenv.WriteShellFile(dataDir, ""); err != nil {
			return "", err
		}
		return "已清除显式 shell 设置, 使用账户登录 shell", nil
	case !shellenv.ValidateShell(value):
		return "", fmt.Errorf("--shell %q 不是可用的 shell (需为可执行的绝对路径, 且不能是 nologin/false)", value)
	}
	if err := shellenv.WriteShellFile(dataDir, value); err != nil {
		return "", err
	}
	return "显式 shell 已设置: " + value, nil
}

// printRuntimeShell reports the shell new terminals would use and the environment resolved from it, plus the
// last result recorded by the running helper.
func printRuntimeShell(stdout io.Writer, dataDir string, getenv func(string) string, resolveWait time.Duration) {
	resolver := shellenv.New(shellenv.Options{
		Explicit: func() string {
			if value := strings.TrimSpace(getenv(shellenv.ShellEnv)); value != "" {
				return value
			}
			return shellenv.ReadShellFile(shellFile(dataDir))
		},
		Getenv: getenv,
	})
	choice := resolver.Shell("")
	fmt.Fprintf(stdout, "新终端 shell: %s (来源: %s)\n", choice.Path, choice.Source)
	if choice.Note != "" {
		fmt.Fprintf(stdout, "shell 选择提示: %s\n", applog.RedactText(choice.Note))
	}
	resolver.Env(resolveWait)
	status := resolver.Status()
	if status.ResolvedAt.IsZero() {
		fmt.Fprintln(stdout, "环境解析: 超时未完成, 终端将使用当时可得的最佳环境")
	} else {
		printShellStatus(stdout, "环境解析", status)
	}
	if last, ok := shellenv.LoadStatus(dataDir); ok {
		printShellStatus(stdout, "运行中 helper 最近一次解析", last)
	}
}

func printShellStatus(stdout io.Writer, label string, status shellenv.Status) {
	fmt.Fprintf(stdout, "%s: shell=%s (%s) PATH 来源=%s 方法=%s 时间=%s 耗时=%dms\n",
		label, status.Shell, status.ShellSource, status.PathSource, status.Method,
		status.ResolvedAt.Format(time.RFC3339), status.DurationMS)
	if status.Error != "" {
		fmt.Fprintf(stdout, "%s 错误: %s\n", label, applog.RedactText(status.Error))
	}
}
