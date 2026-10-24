package diagcapture

import (
	"errors"
	"os/exec"
	"runtime"
)

// CanOpenFolder reports whether OpenFolder has a file manager to call on this platform.
func CanOpenFolder() bool {
	switch runtime.GOOS {
	case "darwin", "windows", "linux", "freebsd", "openbsd", "netbsd":
		return true
	}
	return false
}

// OpenFolder asks the desktop shell to show dir in the file manager.
func OpenFolder(dir string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", dir)
	case "windows":
		cmd = exec.Command("explorer", dir)
	case "linux", "freebsd", "openbsd", "netbsd":
		cmd = exec.Command("xdg-open", dir)
	default:
		return errors.New("opening folders is not supported on this platform")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
