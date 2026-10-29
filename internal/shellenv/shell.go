// Package shellenv decides which shell NexTerm runs for a user and resolves the environment that shell
// would give an interactive terminal. Services started by launchd or systemd and GUI apps launched from
// Finder inherit a minimal environment, so the environment is read from the user's own login shell at
// runtime instead of being recorded at install time.
package shellenv

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/usershell"
)

// Source says where the chosen shell came from.
type Source string

const (
	SourceRequest  Source = "request"  // the caller of a single session asked for it
	SourceExplicit Source = "explicit" // NEXTERM_SHELL or the shell file written by `install --shell`
	SourceAccount  Source = "account"  // login shell from the account database (dscl, getent, passwd)
	SourceEnv      Source = "env"      // inherited $SHELL, only trusted after the account shell
	SourceFallback Source = "fallback" // /bin/sh
)

// Environment variables that configure the explicit shell.
const (
	// ShellEnv names the shell directly.
	ShellEnv = "NEXTERM_SHELL"
	// ShellFileEnv points at a file whose first line is the shell path; the agent sets it from its data dir.
	ShellFileEnv = "NEXTERM_SHELL_FILE"
	// ResolvingEnv is set while the resolver runs the user's shell, so rc files can detect it.
	ResolvingEnv = "NEXTERM_RESOLVING_ENV"
	// ShellFileName is the file inside a data directory that holds the explicit shell.
	ShellFileName = "shell"
)

// Choice is the shell to run.
type Choice struct {
	Path   string
	Source Source
	// Login reports whether the shell accepts -l; the /bin/sh fallback may be dash, which rejects it.
	Login bool
	// Note lists higher-priority candidates that were configured but rejected.
	Note string
}

// Inputs are the candidates Choose considers, highest priority first.
type Inputs struct {
	Request  string
	Explicit string
	Account  func() string
	Env      string
}

// Choose applies the precedence request > explicit > account > $SHELL > /bin/sh. A candidate must be an
// absolute executable that is not a placeholder such as nologin or false.
func Choose(in Inputs) Choice {
	var notes []string
	try := func(label string, source Source, path string) (Choice, bool) {
		path = strings.TrimSpace(path)
		if path == "" {
			return Choice{}, false
		}
		if usable(path) {
			return Choice{Path: path, Source: source, Login: true, Note: strings.Join(notes, "; ")}, true
		}
		notes = append(notes, label+" "+sanitizeNote(path)+" is not a usable shell")
		return Choice{}, false
	}
	if c, ok := try("requested shell", SourceRequest, in.Request); ok {
		return c
	}
	if c, ok := try("explicit shell", SourceExplicit, in.Explicit); ok {
		return c
	}
	if in.Account != nil {
		if c, ok := try("account shell", SourceAccount, in.Account()); ok {
			return c
		}
	}
	if c, ok := try("$SHELL", SourceEnv, in.Env); ok {
		return c
	}
	return Choice{Path: "/bin/sh", Source: SourceFallback, Note: strings.Join(notes, "; ")}
}

func usable(path string) bool {
	return !strings.ContainsAny(path, "\n\r\x00") && usershell.Usable(path)
}

func sanitizeNote(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, value)
	if len(value) > 120 {
		value = value[:120] + "..."
	}
	return value
}

// ReadShellFile returns the first non-empty line of the shell file, or "" when it is missing.
func ReadShellFile(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return ""
}

// WriteShellFile stores shell as the explicit shell in dataDir. An empty shell removes the setting so the
// account shell applies again.
func WriteShellFile(dataDir, shell string) error {
	path := filepath.Join(dataDir, ShellFileName)
	if shell == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(shell+"\n"), 0o600)
}

// ValidateShell reports whether shell can be stored as the explicit shell.
func ValidateShell(shell string) bool { return usable(strings.TrimSpace(shell)) }

// ExplicitFromEnv returns NEXTERM_SHELL, else the shell file named by NEXTERM_SHELL_FILE.
func ExplicitFromEnv(getenv func(string) string) string {
	if value := strings.TrimSpace(getenv(ShellEnv)); value != "" {
		return value
	}
	return ReadShellFile(getenv(ShellFileEnv))
}
