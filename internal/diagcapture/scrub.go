package diagcapture

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// homeScrubber rewrites the user's home directory to "~" before a log line reaches the capture folder.
// A capture is meant to be shared, and the home path carries the local account name.
type homeScrubber struct {
	w       io.Writer
	pattern *regexp.Regexp
}

// newHomeScrubber returns w unchanged when home is unknown or too short to match safely.
func newHomeScrubber(w io.Writer, home string) io.Writer {
	pattern := homePattern(home)
	if pattern == nil {
		return w
	}
	return &homeScrubber{w: w, pattern: pattern}
}

// homePattern matches home only as a whole path element, so /Users/ann does not rewrite /Users/anna.
// The text log handler escapes backslashes inside quoted values, so Windows homes are matched in both forms.
func homePattern(home string) *regexp.Regexp {
	home = strings.TrimRight(filepath.Clean(strings.TrimSpace(home)), `/\`)
	if len(home) < 3 || home == "." {
		return nil
	}
	forms := []string{regexp.QuoteMeta(home)}
	if strings.Contains(home, `\`) {
		forms = append(forms, regexp.QuoteMeta(strings.ReplaceAll(home, `\`, `\\`)))
	}
	flags := ""
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		flags = "(?i)" // case-insensitive file systems by default
	}
	return regexp.MustCompile(flags + `(?:` + strings.Join(forms, "|") + `)([/\\"'\s,;:)\]]|$)`)
}

func scrubHome(pattern *regexp.Regexp, p []byte) []byte {
	if pattern == nil {
		return p
	}
	return pattern.ReplaceAll(p, []byte("~$1"))
}

func (h *homeScrubber) Write(p []byte) (int, error) {
	if _, err := h.w.Write(scrubHome(h.pattern, p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func userHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
