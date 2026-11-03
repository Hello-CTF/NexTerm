package platform

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// ErrNoSharedHelperDir reports a platform without a helper shared between the
// desktop app and the device agent of one user (mobile).
var ErrNoSharedHelperDir = errors.New("no shared supervisor helper directory on this platform")

// SharedHelperStateDir returns the supervisor helper state directory shared by
// the desktop app and the NexTerm agent of the current OS user, so both reach
// one helper and one set of persistent terminals. It lies outside the data
// directory of either app:
//
//	linux, BSD  ${XDG_STATE_HOME:-~/.local/state}/nexterm/supervisor
//	darwin      ~/Library/Application Support/NexTerm/shared/supervisor
//	windows     %LOCALAPPDATA%\NexTerm\shared\supervisor
//
// The desktop data directory is ~/.config/nexterm and the agent one
// ~/.local/share/NexTerm (Windows %LOCALAPPDATA%\NexTerm); the agent never
// creates a "shared" entry in its data directory. The unix socket of a helper
// is named from a hash under the temp directory, so these paths do not count
// against the socket path length limit.
func SharedHelperStateDir(goos string, getenv func(string) string, home string) (string, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	switch goos {
	case "android", "ios":
		return "", ErrNoSharedHelperDir
	case "windows":
		if base := getenv("LOCALAPPDATA"); filepath.IsAbs(base) {
			return filepath.Join(base, "NexTerm", "shared", "supervisor"), nil
		}
		if home == "" {
			return "", errors.New("home directory is empty")
		}
		return filepath.Join(home, "AppData", "Local", "NexTerm", "shared", "supervisor"), nil
	case "darwin":
		if home == "" {
			return "", errors.New("home directory is empty")
		}
		return filepath.Join(home, "Library", "Application Support", "NexTerm", "shared", "supervisor"), nil
	default:
		// XDG requires an absolute path; a relative value is ignored.
		if base := getenv("XDG_STATE_HOME"); filepath.IsAbs(base) {
			return filepath.Join(base, "nexterm", "supervisor"), nil
		}
		if home == "" {
			return "", errors.New("home directory is empty")
		}
		return filepath.Join(home, ".local", "state", "nexterm", "supervisor"), nil
	}
}

// DefaultSharedHelperStateDir is SharedHelperStateDir for this process.
func DefaultSharedHelperStateDir() (string, error) {
	home, _ := os.UserHomeDir()
	return SharedHelperStateDir(runtime.GOOS, os.Getenv, home)
}
