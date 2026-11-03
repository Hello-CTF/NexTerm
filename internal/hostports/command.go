package hostports

import (
	"errors"
	"fmt"
	"strings"
)

// Shell is the command language of the exec channel of a host.
type Shell string

const (
	ShellPOSIX      Shell = "posix"
	ShellPowerShell Shell = "powershell"
	// ShellUnknown means the shell is not known up front. Windows OpenSSH hosts that run cmd as the default
	// shell also end up here once the platform probe has found no POSIX shell.
	ShellUnknown Shell = ""
)

const sentinelPrefix = "@@nexterm-ports "

// ErrNoSentinel reports output that did not come from the POSIX listing script, which is how a
// Windows host reveals itself.
var ErrNoSentinel = errors.New("port listing sentinel missing")

// ListCommand returns a POSIX script that prints a sentinel line naming the tool it used, followed by that
// tool's output.
func ListCommand(withProcess bool) string {
	ss, netstat, mac := "ss -ltnH", "netstat -ltn", "netstat -anv -p tcp"
	if withProcess {
		ss, netstat = "ss -ltnpH", "netstat -ltnp"
	}
	macBranch := "out=$(" + mac + " 2>/dev/null)"
	macFormat := "netstat-bsd"
	if withProcess {
		macBranch = "command -v lsof >/dev/null 2>&1 && out=$(lsof -nP -iTCP -sTCP:LISTEN 2>/dev/null)"
		macFormat = "lsof"
	}
	return strings.Join([]string{
		"if command -v ss >/dev/null 2>&1 && out=$(" + ss + " 2>/dev/null); then printf '%s\\n%s\\n' '" + sentinelPrefix + "ss' \"$out\"",
		"elif [ \"$(uname -s 2>/dev/null)\" = Linux ] && command -v netstat >/dev/null 2>&1 && out=$(" + netstat + " 2>/dev/null); then printf '%s\\n%s\\n' '" + sentinelPrefix + "netstat-linux' \"$out\"",
		"elif [ \"$(uname -s 2>/dev/null)\" = Darwin ] && " + macBranch + "; then printf '%s\\n%s\\n' '" + sentinelPrefix + macFormat + "' \"$out\"",
		"else printf '%s\\n' '" + sentinelPrefix + "none'",
		"fi",
	}, "\n")
}

// ParseListOutput reads the sentinel written by ListCommand and parses the rest of the output.
func ParseListOutput(stdout string) ([]Listener, Format, error) {
	text := strings.ReplaceAll(stdout, "\r", "")
	index := strings.Index(text, sentinelPrefix)
	if index < 0 {
		return nil, "", ErrNoSentinel
	}
	text = text[index+len(sentinelPrefix):]
	name, rest, _ := strings.Cut(text, "\n")
	format := Format(strings.TrimSpace(name))
	if format == "none" {
		return nil, "", errors.New("no supported port listing tool (ss, netstat or lsof) found on the host")
	}
	listeners, err := Parse(format, rest)
	if err != nil {
		return nil, format, fmt.Errorf("parse %s output: %w", format, err)
	}
	return listeners, format, nil
}

// Commands for Windows hosts; PowerShell and cmd both run them unchanged.
const (
	windowsNetstatCommand   = "netstat -ano -p tcp"
	windowsNetstat6Command  = "netstat -ano -p tcpv6"
	windowsTasklistCommand  = "tasklist /FO CSV /NH"
	posixPlatformProbeShell = "printf '%s\\n' '" + sentinelPrefix + "posix'"
)
