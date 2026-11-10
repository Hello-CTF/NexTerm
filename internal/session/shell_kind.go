package session

import (
	"encoding/json"
	"strings"
)

// remoteShellDetector is a connection that may have learned the default shell of the host (an SSH connection whose
// file browser probed a Windows host). It answers from what it already knows and never runs a probe of its own.
type remoteShellDetector interface {
	DetectedShell() string
}

// ClassifyShellProgram names the command language of a shell given as a program name or path. Paths may use either
// separator; Windows names are matched without regard to case or an .exe suffix.
func ClassifyShellProgram(program string) Shell {
	name := strings.TrimSpace(program)
	if index := strings.LastIndexAny(name, `/\`); index >= 0 {
		name = name[index+1:]
	}
	lower := strings.TrimSuffix(strings.ToLower(name), ".exe")
	switch lower {
	case "powershell", "pwsh":
		return ShellPowerShell
	case "cmd":
		return ShellCmd
	}
	switch strings.TrimSuffix(name, ".exe") {
	case "sh", "bash", "zsh", "dash", "ksh":
		return ShellPOSIX
	}
	return ShellUnknown
}

// assetShellOption is the shell an asset's options name, or "" when they name none.
func assetShellOption(options any) string {
	if options == nil {
		return ""
	}
	if typed, ok := options.(map[string]any); ok {
		value, _ := typed["shell"].(string)
		return value
	}
	var decoded struct {
		Shell string `json:"shell"`
	}
	raw, err := json.Marshal(options)
	if err != nil || json.Unmarshal(raw, &decoded) != nil {
		return ""
	}
	return decoded.Shell
}
