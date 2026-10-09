//go:build !windows

package update

import (
	"os"
	"os/exec"
	"syscall"
)

// StartDetached 启动独立的新进程 (新会话), 供桌面端重启应用。
func StartDetached(path string, args []string) error {
	command := exec.Command(path, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
