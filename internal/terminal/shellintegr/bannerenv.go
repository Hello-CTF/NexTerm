package shellintegr

import (
	"os"
	"path/filepath"
	"strings"
)

const bashSilenceKey = "BASH_SILENCE_DEPRECATION_WARNING"

// BashBannerEnv returns the entry that silences macOS's "default interactive shell is
// now zsh" banner when goos is darwin and shellPath is bash. It returns nil when the
// user already sets the variable, either in env or in the process environment.
func BashBannerEnv(goos, shellPath string, env []string) []string {
	if goos != "darwin" || filepath.Base(shellPath) != string(ShellBash) {
		return nil
	}
	if _, set := os.LookupEnv(bashSilenceKey); set {
		return nil
	}
	for _, entry := range env {
		if key, _, found := strings.Cut(entry, "="); found && key == bashSilenceKey {
			return nil
		}
	}
	return []string{bashSilenceKey + "=1"}
}
