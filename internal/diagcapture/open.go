package diagcapture

import (
	"errors"
	"os/exec"
	"path/filepath"
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
	return startDetached(shellCommand(runtime.GOOS, dir, false))
}

// RevealFile shows file selected in the file manager; platforms that cannot select fall back to its folder.
func RevealFile(file string) error {
	return startDetached(shellCommand(runtime.GOOS, file, true))
}

// shellCommand builds the file manager invocation for goos, or nil when the platform has none.
func shellCommand(goos, target string, reveal bool) *exec.Cmd {
	switch goos {
	case "darwin":
		if reveal {
			return exec.Command("open", "-R", target)
		}
		return exec.Command("open", target)
	case "windows":
		if reveal {
			return exec.Command("explorer", "/select,"+target)
		}
		return exec.Command("explorer", target)
	case "linux", "freebsd", "openbsd", "netbsd":
		if reveal {
			target = filepath.Dir(target)
		}
		return exec.Command("xdg-open", target)
	}
	return nil
}

func startDetached(cmd *exec.Cmd) error {
	if cmd == nil {
		return errors.New("opening folders is not supported on this platform")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
