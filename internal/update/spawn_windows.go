//go:build windows

package update

import "os/exec"

// StartDetached 启动独立的新进程, 供桌面端重启应用。
func StartDetached(path string, args []string) error {
	command := exec.Command(path, args...)
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
