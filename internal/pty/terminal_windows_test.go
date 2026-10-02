//go:build windows

package pty

import (
	"os/exec"
	"strings"
	"testing"
)

func TestWindowsCommandLineUsesEscapedResolvedPath(t *testing.T) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", "printf 'hello world'")
	cmd.Path = `C:\Program Files\PowerShell\pwsh.exe`
	line := windowsCommandLine(cmd)
	if !strings.Contains(line, `"C:\Program Files\PowerShell\pwsh.exe"`) || !strings.Contains(line, `"printf 'hello world'"`) {
		t.Fatalf("command line = %q", line)
	}
}
