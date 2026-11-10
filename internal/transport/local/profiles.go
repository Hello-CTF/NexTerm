package local

import (
	"bufio"
	"path"
	"strings"
)

// ShellProfile is a shell found on this host that a local asset can run: Path is the executable and Args the
// arguments it starts with, so a profile such as "wsl.exe -d Ubuntu" fills both asset fields.
type ShellProfile struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Path string   `json:"path"`
	Args []string `json:"args"`
}

// ShellProfiles lists the shells installed on this host, in the order a picker shows them. On Windows these are
// PowerShell 7, Windows PowerShell, cmd, each installed WSL distribution and Git Bash; elsewhere the login shells
// listed in /etc/shells. A source that cannot be read contributes nothing.
func ShellProfiles() []ShellProfile {
	profiles := detectShellProfiles()
	for index := range profiles {
		if profiles[index].Args == nil {
			profiles[index].Args = []string{}
		}
	}
	return profiles
}

// WSLDistribution is one entry under HKCU\Software\Microsoft\Windows\CurrentVersion\Lxss.
type WSLDistribution struct {
	Name string
	// State is the Lxss State value; 1 is installed, other values are installing, uninstalling or converting.
	State   uint64
	Default bool
}

const wslStateInstalled = 1

// windowsShellSources is what Windows profile detection reads from the host, injected so the assembly is testable
// on any platform.
type windowsShellSources struct {
	LookPath     func(string) (string, error)
	Exists       func(string) bool
	SystemRoot   string
	ComSpec      string
	ProgramFiles []string
	WSL          []WSLDistribution
	// GitInstallPaths are Git for Windows install directories, from the registry first.
	GitInstallPaths []string
}

func windowsShellProfiles(sources windowsShellSources) []ShellProfile {
	exists := sources.Exists
	if exists == nil {
		exists = func(string) bool { return false }
	}
	var profiles []ShellProfile
	seen := map[string]bool{}
	add := func(profile ShellProfile) {
		key := strings.ToLower(profile.Path + "\x00" + strings.Join(profile.Args, "\x00"))
		if profile.Path == "" || seen[key] {
			return
		}
		seen[key] = true
		profiles = append(profiles, profile)
	}

	pwsh := ""
	if sources.LookPath != nil {
		if found, err := sources.LookPath("pwsh.exe"); err == nil {
			pwsh = found
		}
	}
	for _, root := range sources.ProgramFiles {
		if pwsh != "" {
			break
		}
		if candidate := windowsJoin(root, "PowerShell", "7", "pwsh.exe"); root != "" && exists(candidate) {
			pwsh = candidate
		}
	}
	if pwsh != "" {
		add(ShellProfile{ID: "pwsh", Name: "PowerShell", Path: pwsh})
	}
	for _, root := range sources.ProgramFiles {
		if candidate := windowsJoin(root, "PowerShell", "7-preview", "pwsh.exe"); root != "" && exists(candidate) {
			add(ShellProfile{ID: "pwsh-preview", Name: "PowerShell Preview", Path: candidate})
		}
	}
	if sources.SystemRoot != "" {
		if candidate := windowsJoin(sources.SystemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"); exists(candidate) {
			add(ShellProfile{ID: "powershell", Name: "Windows PowerShell", Path: candidate})
		}
	}
	cmd := ""
	if sources.ComSpec != "" && exists(sources.ComSpec) {
		cmd = sources.ComSpec
	} else if sources.SystemRoot != "" && exists(windowsJoin(sources.SystemRoot, "System32", "cmd.exe")) {
		cmd = windowsJoin(sources.SystemRoot, "System32", "cmd.exe")
	}
	if cmd != "" {
		add(ShellProfile{ID: "cmd", Name: "Command Prompt", Path: cmd})
	}
	if wsl := windowsJoin(sources.SystemRoot, "System32", "wsl.exe"); sources.SystemRoot != "" && exists(wsl) {
		for _, distro := range orderedWSLDistributions(sources.WSL) {
			add(ShellProfile{ID: "wsl:" + distro.Name, Name: "WSL: " + distro.Name, Path: wsl, Args: []string{"-d", distro.Name}})
		}
	}
	installs := append([]string(nil), sources.GitInstallPaths...)
	for _, root := range sources.ProgramFiles {
		if root != "" {
			installs = append(installs, windowsJoin(root, "Git"))
		}
	}
	for _, install := range installs {
		if bash := windowsJoin(install, "bin", "bash.exe"); strings.TrimSpace(install) != "" && exists(bash) {
			add(ShellProfile{ID: "git-bash", Name: "Git Bash", Path: bash, Args: []string{"--login", "-i"}})
			break
		}
	}
	return profiles
}

// orderedWSLDistributions keeps installed distributions with a usable name, the default one first. Docker
// Desktop's internal distributions are not shells a user runs and are left out.
func orderedWSLDistributions(distributions []WSLDistribution) []WSLDistribution {
	var ordered []WSLDistribution
	for _, distro := range distributions {
		name := strings.TrimSpace(distro.Name)
		if distro.State != wslStateInstalled || name == "" || strings.HasPrefix(strings.ToLower(name), "docker-desktop") ||
			strings.ContainsAny(name, "\x00\r\n") {
			continue
		}
		distro.Name = name
		if distro.Default {
			ordered = append([]WSLDistribution{distro}, ordered...)
		} else {
			ordered = append(ordered, distro)
		}
	}
	return ordered
}

func windowsJoin(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for index, part := range parts {
		if index > 0 {
			part = strings.TrimLeft(part, `\/`)
		}
		if index < len(parts)-1 {
			part = strings.TrimRight(part, `\/`)
		}
		cleaned = append(cleaned, part)
	}
	return strings.Join(cleaned, `\`)
}

// parseEtcShells reads /etc/shells: absolute paths, one per line, with # comments. Shells that refuse logins
// (nologin, false) and paths that do not exist are skipped, and canonical resolves aliases such as /bin/bash and
// /usr/bin/bash on a merged /usr to a single entry, the first listed.
func parseEtcShells(content string, exists func(string) bool, canonical func(string) string) []ShellProfile {
	var profiles []ShellProfile
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line, _, _ := strings.Cut(scanner.Text(), "#")
		shell := strings.TrimSpace(line)
		if shell == "" || !strings.HasPrefix(shell, "/") || strings.ContainsAny(shell, " \t") {
			continue
		}
		switch path.Base(shell) {
		case "nologin", "false", "true":
			continue
		}
		if exists != nil && !exists(shell) {
			continue
		}
		key := shell
		if canonical != nil {
			key = canonical(shell)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		profiles = append(profiles, ShellProfile{ID: shell, Name: path.Base(shell), Path: shell})
	}
	return profiles
}
