package session

import "strings"

// remoteShellDetector is a connection that may have learned the default shell of the host (an SSH connection whose
// file browser probed a Windows host). It answers from what it already knows and never runs a probe of its own.
type remoteShellDetector interface {
	DetectedShell() string
}

func wslLauncher(program string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(program, "/", `\`))
	base := normalized[strings.LastIndex(normalized, `\`)+1:]
	base = strings.TrimSuffix(base, ".exe")
	if base == "wsl" {
		return true
	}
	if normalized == "bash.exe" {
		// Windows resolves a bare bash.exe to System32's WSL launcher first.
		return true
	}
	return base == "bash" && (strings.Contains(normalized, `\system32\`) || strings.Contains(normalized, `\sysnative\`) || strings.Contains(normalized, `\windowsapps\`))
}

// ClassifyShellProgram names the command language of a shell given as a program name or path. Paths may use either
// separator; Windows names are matched without regard to case or an .exe suffix.
func ClassifyShellProgram(program string) Shell {
	name := strings.TrimSpace(program)
	// WSL's launchers run a Linux shell that cannot change into the Windows paths the file browser shows.
	if wslLauncher(name) {
		return ShellUnknown
	}
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
