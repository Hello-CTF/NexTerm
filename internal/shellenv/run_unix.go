//go:build unix

package shellenv

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

// runShell runs shell with args in its own session with stdin from /dev/null, killing the whole process
// group when ctx ends so a hanging rc file cannot leave stragglers.
func runShell(ctx context.Context, shell string, args, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, shell, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	return cmd.Output()
}
