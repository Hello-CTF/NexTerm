// Package usershell resolves the account's login shell when $SHELL is unset, as in
// launchd and systemd --user services.
package usershell

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Lookup returns the absolute path of the current user's login shell from the account
// database, or "" when it cannot be determined or is not a usable interactive shell.
func Lookup() string {
	current, err := user.Current()
	if err != nil {
		return ""
	}
	var shell string
	if runtime.GOOS == "darwin" {
		shell = fromDscl(current.Username)
	} else {
		shell = fromGetent(current.Username)
		if shell == "" {
			shell = fromPasswdFile("/etc/passwd", current.Username)
		}
	}
	if Usable(shell) {
		return shell
	}
	return ""
}

func fromDscl(name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/dscl", ".", "-read", "/Users/"+name, "UserShell")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return ParseDscl(string(out))
}

// ParseDscl extracts the shell from `dscl . -read /Users/<name> UserShell` output.
func ParseDscl(output string) string {
	_, value, ok := strings.Cut(strings.TrimSpace(output), "UserShell:")
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func fromGetent(name string) string {
	path, err := exec.LookPath("getent")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "passwd", name)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return ParsePasswdLine(strings.TrimSpace(string(out)))
}

func fromPasswdFile(path, name string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, name+":") {
			return ParsePasswdLine(line)
		}
	}
	return ""
}

// ParsePasswdLine returns the shell (last field) of a passwd(5) entry.
func ParsePasswdLine(line string) string {
	fields := strings.Split(line, ":")
	if len(fields) < 7 {
		return ""
	}
	return strings.TrimSpace(fields[len(fields)-1])
}

// Usable reports whether shell is an absolute path to an executable file that is not a
// non-interactive placeholder such as nologin.
func Usable(shell string) bool {
	if !filepath.IsAbs(shell) {
		return false
	}
	switch filepath.Base(shell) {
	case "nologin", "false", "true":
		return false
	}
	info, err := os.Stat(shell)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
