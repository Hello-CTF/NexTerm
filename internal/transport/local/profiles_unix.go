//go:build unix

package local

import (
	"os"
	"path/filepath"
)

func detectShellProfiles() []ShellProfile {
	content, err := os.ReadFile("/etc/shells")
	if err != nil {
		return nil
	}
	return parseEtcShells(string(content), func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
	}, func(path string) string {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return path
	})
}
